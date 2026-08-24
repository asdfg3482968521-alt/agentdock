package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
	"github.com/uvwt/agentdock/internal/redact"
)

type managedAgent struct {
	record  Record
	request *StartRequest
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

type Manager struct {
	options     ManagerOptions
	persistence *agentPersistence
	rootCtx     context.Context
	rootCancel  context.CancelFunc

	mu             sync.Mutex
	agents         map[string]*managedAgent
	queue          []string
	activeGlobal   int
	activeProvider map[string]int
	activeSession  map[string]int
	closed         bool
	crashed        bool
}

func NewManager(ctx context.Context, options ManagerOptions) (*Manager, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Sessions == nil || options.Credentials == nil {
		return nil, fmt.Errorf("%w: session and credential resolvers are required", ErrInvalidArguments)
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.IDFactory == nil {
		options.IDFactory = randomAgentID
	}
	limits := options.Limits
	defaults := DefaultAgentLimits()
	if limits.MaxConcurrent <= 0 {
		limits.MaxConcurrent = defaults.MaxConcurrent
	}
	if limits.MaxConcurrentPerProvider <= 0 {
		limits.MaxConcurrentPerProvider = defaults.MaxConcurrentPerProvider
	}
	if limits.MaxConcurrentPerSession <= 0 {
		limits.MaxConcurrentPerSession = defaults.MaxConcurrentPerSession
	}
	if limits.MaxQueued <= 0 {
		limits.MaxQueued = defaults.MaxQueued
	}
	if limits.MaxOutputBytesPerRead <= 0 {
		limits.MaxOutputBytesPerRead = defaults.MaxOutputBytesPerRead
	}
	if limits.MaxOutputBytesPerAgent <= 0 {
		limits.MaxOutputBytesPerAgent = defaults.MaxOutputBytesPerAgent
	}
	options.Limits = limits
	store, err := newAgentPersistence(options.StorageRoot)
	if err != nil {
		return nil, err
	}
	rootCtx, cancel := context.WithCancel(ctx)
	manager := &Manager{
		options: options, persistence: store, rootCtx: rootCtx, rootCancel: cancel,
		agents: map[string]*managedAgent{}, activeProvider: map[string]int{}, activeSession: map[string]int{},
	}
	if err := manager.recover(); err != nil {
		cancel()
		return nil, err
	}
	return manager, nil
}

func randomAgentID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	buf := make([]byte, 32)
	hex.Encode(buf, value[:])
	return "agent_" + string(buf)
}

func (m *Manager) StorageRoot() string { return m.persistence.root }

func (m *Manager) Start(ctx context.Context, request StartRequest) (Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	request.Model = strings.TrimSpace(request.Model)
	request.Role = strings.TrimSpace(request.Role)
	request.CredentialRef = strings.TrimSpace(request.CredentialRef)
	if request.SessionID == "" || request.Provider == "" || request.MaxRequests < 1 {
		return Record{}, fmt.Errorf("%w: session_id, provider, and budget.max_requests >= 1 are required", ErrInvalidArguments)
	}
	if request.Role == "" {
		request.Role = "implementation"
	}
	if !validAgentRole(request.Role) {
		return Record{}, fmt.Errorf("%w: invalid role", ErrInvalidArguments)
	}
	if request.Timeout <= 0 {
		request.Timeout = 2 * time.Minute
	}
	if request.Timeout > 24*time.Hour {
		return Record{}, fmt.Errorf("%w: timeout_ms must be <= 86400000", ErrInvalidArguments)
	}
	if err := credential.ValidateRef(request.CredentialRef); err != nil {
		return Record{}, err
	}
	provider := m.options.Providers[request.Provider]
	if provider == nil {
		return Record{}, ErrProviderUnsupported
	}
	resolvedModel, err := resolveProviderModel(provider, request.Model)
	if err != nil {
		return Record{}, err
	}
	request.Model = resolvedModel
	binding, err := m.options.Sessions.ResolveAgentSession(ctx, request.SessionID)
	if err != nil {
		return Record{}, err
	}
	if binding.SessionID != request.SessionID || strings.TrimSpace(binding.Workspace) == "" {
		return Record{}, ErrAgentSessionClosed
	}
	sessionPermission, err := normalizeAgentPermission(binding.PermissionMode)
	if err != nil {
		return Record{}, err
	}
	permission := strings.ToLower(strings.TrimSpace(request.PermissionMode))
	if permission == "" {
		permission = sessionPermission
	}
	if permission != "read-only" && permission != "project" {
		return Record{}, fmt.Errorf("%w: permission must be read-only or project", ErrInvalidArguments)
	}
	if permission == "project" && sessionPermission != "project" {
		return Record{}, ErrAgentWriteDenied
	}
	request.PermissionMode = permission

	m.mu.Lock()
	if m.closed || m.crashed {
		m.mu.Unlock()
		return Record{}, ErrAgentSessionClosed
	}
	queued := 0
	for _, id := range m.queue {
		if managed := m.agents[id]; managed != nil && managed.record.State == AgentStateQueued {
			queued++
		}
	}
	if queued >= m.options.Limits.MaxQueued {
		m.mu.Unlock()
		return Record{}, ErrResourceLimit
	}
	id := m.allocateIDLocked()
	now := m.options.Now().UTC()
	record := Record{
		AgentID: id, SessionID: request.SessionID, Workspace: binding.Workspace, RuntimeScope: binding.RuntimeScope,
		PermissionMode: permission, Provider: request.Provider, Model: request.Model, ResolvedModel: request.Model,
		Role: request.Role, CredentialRef: request.CredentialRef, TimeoutMS: request.Timeout.Milliseconds(),
		Budget: AgentBudget{MaxRequests: request.MaxRequests}, MaxRequests: request.MaxRequests,
		State: AgentStateQueued, CreatedAt: now, UpdatedAt: now,
	}
	copyRequest := request
	managed := &managedAgent{record: record, request: &copyRequest, done: make(chan struct{})}
	m.agents[id] = managed
	m.mu.Unlock()

	if err := m.persistence.save(record); err != nil {
		m.mu.Lock()
		delete(m.agents, id)
		m.mu.Unlock()
		return Record{}, err
	}
	m.mu.Lock()
	m.queue = append(m.queue, id)
	m.scheduleLocked()
	result := cloneAgentRecord(managed.record)
	m.mu.Unlock()
	return result, nil
}

func normalizeAgentPermission(sessionPermission string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(sessionPermission)) {
	case "read-only":
		return "read-only", nil
	case "project", "full":
		return "project", nil
	default:
		return "", ErrAgentSessionClosed
	}
}

func validAgentRole(role string) bool {
	if len(role) < 1 || len(role) > 128 {
		return false
	}
	for i := 0; i < len(role); i++ {
		c := role[i]
		alphaNum := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 {
			if !alphaNum {
				return false
			}
			continue
		}
		if !alphaNum && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func resolveProviderModel(provider ProviderAdapter, raw string) (string, error) {
	if resolver, ok := provider.(ProviderModelResolver); ok {
		model, err := resolver.ResolveModel(raw)
		if err != nil {
			return "", err
		}
		model = strings.TrimSpace(model)
		if model == "" || model == "default" {
			return "", &providerError{code: ProviderErrorModelUnavailable, err: errors.New("provider did not resolve a concrete model")}
		}
		return model, nil
	}
	model := strings.TrimSpace(raw)
	if model == "" || model == "default" {
		return "", &providerError{code: ProviderErrorModelUnavailable, err: errors.New("provider has no deterministic model resolver")}
	}
	return model, nil
}

func (m *Manager) allocateIDLocked() string {
	for i := 0; i < 64; i++ {
		id := strings.TrimSpace(m.options.IDFactory())
		if id != "" {
			if _, exists := m.agents[id]; !exists {
				return id
			}
		}
	}
	panic("unable to allocate agent id")
}

func (m *Manager) scheduleLocked() {
	if m.closed || m.crashed {
		return
	}
	for {
		index := -1
		for i, id := range m.queue {
			managed := m.agents[id]
			if managed != nil && managed.record.State == AgentStateQueued && m.canRunLocked(managed.record) {
				index = i
				break
			}
		}
		if index < 0 {
			return
		}
		id := m.queue[index]
		m.queue = append(m.queue[:index], m.queue[index+1:]...)
		managed := m.agents[id]
		now := m.options.Now().UTC()
		managed.record.State = AgentStateStarting
		managed.record.StartedAt = &now
		managed.record.UpdatedAt = now
		m.activeGlobal++
		m.activeProvider[managed.record.Provider]++
		m.activeSession[managed.record.SessionID]++
		runCtx := m.rootCtx
		if managed.request != nil && managed.request.Timeout > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(m.rootCtx, managed.request.Timeout)
			managed.cancel = cancel
		} else {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithCancel(m.rootCtx)
			managed.cancel = cancel
		}
		_ = m.persistence.save(managed.record)
		go m.runAgent(runCtx, id)
	}
}

func (m *Manager) canRunLocked(record Record) bool {
	return m.activeGlobal < m.options.Limits.MaxConcurrent &&
		m.activeProvider[record.Provider] < m.options.Limits.MaxConcurrentPerProvider &&
		m.activeSession[record.SessionID] < m.options.Limits.MaxConcurrentPerSession
}

func (m *Manager) runAgent(ctx context.Context, id string) {
	m.mu.Lock()
	managed := m.agents[id]
	if managed == nil {
		m.mu.Unlock()
		return
	}
	if m.crashed {
		m.releaseRunLocked(managed)
		m.mu.Unlock()
		return
	}
	managed.record.State = AgentStateRunning
	managed.record.UpdatedAt = m.options.Now().UTC()
	request := *managed.request
	record := cloneAgentRecord(managed.record)
	m.mu.Unlock()
	_ = m.persistence.save(record)

	secret, err := m.options.Credentials.Resolve(ctx, request.CredentialRef)
	if err != nil {
		m.finishAgent(id, AgentStateFailed, ProviderResult{}, err, nil)
		return
	}
	defer credential.Zero(secret)
	provider := m.options.Providers[request.Provider]
	messages := make([]ProviderMessage, 0)
	prompt := request.Prompt
	var lastResult ProviderResult

	for {
		if err := ctx.Err(); err != nil {
			m.finishAgentContextError(id, lastResult, err, secret)
			return
		}
		if err := m.reserveProviderRequest(id); err != nil {
			m.finishAgent(id, AgentStateFailed, lastResult, ErrBudgetExceeded, secret)
			return
		}

		tools := []ProviderTool(nil)
		if m.options.Tools != nil {
			tools = m.options.Tools.Tools(record.PermissionMode)
		}
		providerRequest := ProviderRequest{
			AgentID: id, SessionID: request.SessionID, Model: request.Model, Role: request.Role, Prompt: prompt,
			Messages: messages, Tools: tools, Credential: secret, MaxTurns: request.MaxRequests,
		}
		var toolCalls []ToolCall
		var assistantText strings.Builder
		result, runErr := provider.Run(ctx, providerRequest, func(event ProviderEvent) error {
			if event.Text != "" {
				assistantText.WriteString(event.Text)
			}
			if event.Tool != nil {
				toolCalls = append(toolCalls, *event.Tool)
			}
			return m.appendEvent(id, event, secret)
		})
		result.Summary = sanitizeAgentText(result.Summary, secret)
		lastResult = result
		m.recordProviderUsage(id, result.Usage)
		if runErr != nil {
			m.finishAgentContextError(id, result, runErr, secret)
			return
		}
		if len(toolCalls) == 0 {
			m.finishAgent(id, AgentStateSucceeded, result, nil, secret)
			return
		}
		if m.options.Tools == nil {
			m.finishAgent(id, AgentStateFailed, result, ErrToolError, secret)
			return
		}

		if len(messages) == 0 && strings.TrimSpace(request.Prompt) != "" {
			messages = append(messages, ProviderMessage{Role: "user", Text: request.Prompt})
		}
		messages = append(messages, ProviderMessage{Role: "assistant", Text: assistantText.String(), ToolCalls: append([]ToolCall(nil), toolCalls...)})
		for _, call := range toolCalls {
			if err := ctx.Err(); err != nil {
				m.finishAgentContextError(id, result, err, secret)
				return
			}
			arguments, err := decodeToolArguments(call.Arguments)
			if err != nil {
				m.finishAgent(id, AgentStateFailed, result, ErrToolError, secret)
				return
			}
			toolResult, err := m.options.Tools.Execute(ctx, ToolExecutionRequest{
				AgentID: id, SessionID: record.SessionID, Workspace: record.Workspace, RuntimeScope: record.RuntimeScope,
				PermissionMode: record.PermissionMode, Tool: call.Name, Arguments: arguments,
			})
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					m.finishAgentContextError(id, result, err, secret)
					return
				}
				m.finishAgent(id, AgentStateFailed, result, err, secret)
				return
			}
			text := sanitizeAgentText(toolResult.Text, secret)
			messages = append(messages, ProviderMessage{Role: "tool", ToolCallID: call.ID, Text: text})
			_ = m.appendEvent(id, ProviderEvent{Kind: ProviderEventText, Text: "[tool_result] " + text}, secret)
		}
		prompt = ""
	}
}

func (m *Manager) reserveProviderRequest(id string) error {
	m.mu.Lock()
	managed := m.agents[id]
	if managed == nil || managed.record.RequestsUsed >= managed.record.MaxRequests {
		m.mu.Unlock()
		return ErrBudgetExceeded
	}
	managed.record.RequestsUsed++
	managed.record.Usage.Requests = managed.record.RequestsUsed
	managed.record.UpdatedAt = m.options.Now().UTC()
	record := cloneAgentRecord(managed.record)
	m.mu.Unlock()
	return m.persistence.save(record)
}

func (m *Manager) recordProviderUsage(id string, usage Usage) {
	if !usage.Known {
		return
	}
	m.mu.Lock()
	managed := m.agents[id]
	if managed == nil {
		m.mu.Unlock()
		return
	}
	input := usage.InputTokens
	output := usage.OutputTokens
	if managed.record.Usage.InputTokens != nil {
		input += *managed.record.Usage.InputTokens
	}
	if managed.record.Usage.OutputTokens != nil {
		output += *managed.record.Usage.OutputTokens
	}
	managed.record.Usage.InputTokens = &input
	managed.record.Usage.OutputTokens = &output
	managed.record.UpdatedAt = m.options.Now().UTC()
	record := cloneAgentRecord(managed.record)
	m.mu.Unlock()
	_ = m.persistence.save(record)
}

func (m *Manager) finishAgentContextError(id string, result ProviderResult, runErr error, secret []byte) {
	state := AgentStateFailed
	switch {
	case errors.Is(runErr, context.Canceled):
		state = AgentStateCancelled
	case errors.Is(runErr, context.DeadlineExceeded):
		state = AgentStateTimedOut
	}
	m.finishAgent(id, state, result, runErr, secret)
}

func (m *Manager) appendEvent(id string, event ProviderEvent, secret credential.Secret) error {
	var text string
	if event.Text != "" {
		text = event.Text
	}
	if event.Tool != nil {
		text += "\n[tool] " + event.Tool.Name + " " + string(event.Tool.Arguments)
	}
	text = sanitizeAgentText(text, secret)
	if text == "" {
		return nil
	}
	text += "\n"
	m.mu.Lock()
	managed := m.agents[id]
	if managed == nil || managed.record.State.Terminal() || m.crashed {
		m.mu.Unlock()
		return context.Canceled
	}
	remaining := m.options.Limits.MaxOutputBytesPerAgent - managed.record.OutputCursor
	if remaining <= 0 {
		m.mu.Unlock()
		return nil
	}
	data := []byte(text)
	if int64(len(data)) > remaining {
		data = data[:remaining]
	}
	path := m.persistence.outputPath(id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = file.Write(data)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		managed.record.OutputCursor += int64(len(data))
		managed.record.UpdatedAt = m.options.Now().UTC()
	}
	record := cloneAgentRecord(managed.record)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.persistence.save(record)
}

func sanitizeAgentText(text string, secret []byte) string {
	if len(secret) > 0 {
		text = strings.ReplaceAll(text, string(secret), "[REDACTED]")
	}
	return redact.Text(text)
}

func (m *Manager) finishAgent(id string, state AgentState, result ProviderResult, failure error, secret []byte) {
	m.mu.Lock()
	managed := m.agents[id]
	if managed == nil {
		m.mu.Unlock()
		return
	}
	if m.crashed {
		m.releaseRunLocked(managed)
		managed.once.Do(func() { close(managed.done) })
		m.mu.Unlock()
		return
	}
	current := managed.record.State
	if current == AgentStateCancelRequested && state == AgentStateFailed {
		state = AgentStateCancelled
	}
	if !state.Terminal() || (!validAgentTransition(current, state) && current != state) {
		state = AgentStateFailed
		if failure == nil {
			failure = ErrInvalidAgentTransition
		}
	}
	now := m.options.Now().UTC()
	managed.record.State = state
	managed.record.FinishedAt = &now
	managed.record.UpdatedAt = now
	if strings.TrimSpace(result.ResolvedModel) != "" {
		managed.record.ResolvedModel = result.ResolvedModel
		managed.record.Model = result.ResolvedModel
	}
	managed.record.StopReason = result.StopReason
	managed.record.ResultSummary = sanitizeAgentText(result.Summary, secret)
	managed.record.FailureReason = nil
	managed.record.Error = ""
	if failure != nil {
		envelope := AgentErrorFrom(failure, secret)
		managed.record.FailureReason = &envelope
		managed.record.Error = envelope.Message
	}
	managed.request = nil
	if managed.cancel != nil {
		managed.cancel()
		managed.cancel = nil
	}
	m.releaseRunLocked(managed)
	record := cloneAgentRecord(managed.record)
	managed.once.Do(func() { close(managed.done) })
	m.scheduleLocked()
	m.mu.Unlock()
	_ = m.persistence.save(record)
}

func (m *Manager) releaseRunLocked(managed *managedAgent) {
	if managed.record.StartedAt == nil {
		return
	}
	if m.activeGlobal > 0 {
		m.activeGlobal--
	}
	if m.activeProvider[managed.record.Provider] > 0 {
		m.activeProvider[managed.record.Provider]--
	}
	if m.activeSession[managed.record.SessionID] > 0 {
		m.activeSession[managed.record.SessionID]--
	}
	managed.record.StartedAt = managed.record.StartedAt
}

func (m *Manager) Get(sessionID, agentID string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	managed := m.agents[strings.TrimSpace(agentID)]
	if managed == nil {
		return Record{}, ErrAgentNotFound
	}
	if managed.record.SessionID != strings.TrimSpace(sessionID) {
		return Record{}, ErrAgentSessionOwnershipMismatch
	}
	return cloneAgentRecord(managed.record), nil
}

func (m *Manager) Wait(ctx context.Context, agentID string) (Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	managed := m.agents[strings.TrimSpace(agentID)]
	if managed == nil {
		m.mu.Unlock()
		return Record{}, ErrAgentNotFound
	}
	done := managed.done
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return Record{}, ctx.Err()
	case <-done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return cloneAgentRecord(managed.record), nil
	}
}

func (m *Manager) Cancel(ctx context.Context, sessionID, agentID, reason string) (Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	managed := m.agents[strings.TrimSpace(agentID)]
	if managed == nil {
		m.mu.Unlock()
		return Record{}, ErrAgentNotFound
	}
	if managed.record.SessionID != strings.TrimSpace(sessionID) {
		m.mu.Unlock()
		return Record{}, ErrAgentSessionOwnershipMismatch
	}
	if managed.record.State.Terminal() {
		result := cloneAgentRecord(managed.record)
		m.mu.Unlock()
		return result, nil
	}
	if managed.record.State == AgentStateQueued {
		m.removeQueuedLocked(managed.record.AgentID)
		now := m.options.Now().UTC()
		managed.record.State = AgentStateCancelled
		managed.record.FinishedAt = &now
		managed.record.UpdatedAt = now
		managed.record.Error = redact.Text(strings.TrimSpace(reason))
		managed.request = nil
		record := cloneAgentRecord(managed.record)
		managed.once.Do(func() { close(managed.done) })
		m.mu.Unlock()
		if err := m.persistence.save(record); err != nil {
			return Record{}, err
		}
		return record, nil
	}
	if managed.record.State == AgentStateStarting || managed.record.State == AgentStateRunning {
		managed.record.State = AgentStateCancelRequested
		managed.record.UpdatedAt = m.options.Now().UTC()
	}
	cancel := managed.cancel
	done := managed.done
	record := cloneAgentRecord(managed.record)
	m.mu.Unlock()
	_ = m.persistence.save(record)
	if cancel != nil {
		cancel()
	}
	select {
	case <-ctx.Done():
		return Record{}, ctx.Err()
	case <-done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return cloneAgentRecord(managed.record), nil
	}
}

func (m *Manager) removeQueuedLocked(agentID string) {
	for i, id := range m.queue {
		if id == agentID {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return
		}
	}
}

func (m *Manager) Output(sessionID, agentID string, cursor int64, limit int) (OutputResult, error) {
	record, err := m.Get(sessionID, agentID)
	if err != nil {
		return OutputResult{}, err
	}
	if cursor < 0 {
		return OutputResult{}, ErrInvalidArguments
	}
	if limit <= 0 || limit > m.options.Limits.MaxOutputBytesPerRead {
		limit = m.options.Limits.MaxOutputBytesPerRead
	}
	path := m.persistence.outputPath(agentID)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return OutputResult{NextCursor: cursor, Complete: record.State.Terminal()}, nil
	}
	if err != nil {
		return OutputResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return OutputResult{}, err
	}
	if cursor > info.Size() {
		return OutputResult{}, ErrInvalidArguments
	}
	if _, err := file.Seek(cursor, 0); err != nil {
		return OutputResult{}, err
	}
	buffer := make([]byte, limit)
	n, readErr := file.Read(buffer)
	if readErr != nil && !errors.Is(readErr, os.ErrClosed) && readErr.Error() != "EOF" {
		return OutputResult{}, readErr
	}
	next := cursor + int64(n)
	return OutputResult{Text: string(buffer[:n]), NextCursor: next, Truncated: next < info.Size(), Complete: record.State.Terminal() && next >= info.Size()}, nil
}

func (m *Manager) Result(sessionID, agentID string) (Result, error) {
	record, err := m.Get(sessionID, agentID)
	if err != nil {
		return Result{}, err
	}
	if !record.State.Terminal() {
		return Result{}, ErrAgentNotTerminal
	}
	return Result{AgentID: record.AgentID, State: record.State, Summary: record.ResultSummary, ResolvedModel: record.ResolvedModel, StopReason: record.StopReason}, nil
}

func (m *Manager) List(filter ListFilter) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]Record, 0)
	for _, managed := range m.agents {
		if filter.SessionID != "" && managed.record.SessionID != filter.SessionID {
			continue
		}
		if filter.State != "" && managed.record.State != filter.State {
			continue
		}
		items = append(items, cloneAgentRecord(managed.record))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].AgentID < items[j].AgentID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func (m *Manager) recover() error {
	records, err := m.persistence.loadAll()
	if err != nil {
		return err
	}
	for _, record := range records {
		now := m.options.Now().UTC()
		recovered := recoveredAgentState(record.State)
		if recovered != record.State {
			record.State = recovered
			record.UpdatedAt = now
			if recovered.Terminal() {
				record.FinishedAt = &now
				record.Error = "AgentDock restarted while agent was nonterminal"
			}
			if err := m.persistence.save(record); err != nil {
				return err
			}
		}
		managed := &managedAgent{record: record, done: make(chan struct{})}
		if record.State.Terminal() {
			managed.once.Do(func() { close(managed.done) })
		}
		m.agents[record.AgentID] = managed
		// Recovered QUEUED records deliberately remain durable but are not replayed.
	}
	return nil
}

func (m *Manager) crashForTest() {
	m.mu.Lock()
	m.crashed = true
	m.rootCancel()
	for _, managed := range m.agents {
		if managed.cancel != nil {
			managed.cancel()
		}
	}
	m.mu.Unlock()
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.rootCancel()
	items := make([]*managedAgent, 0, len(m.agents))
	for _, managed := range m.agents {
		if !managed.record.State.Terminal() {
			items = append(items, managed)
		}
	}
	m.mu.Unlock()
	for _, managed := range items {
		_, _ = m.Cancel(context.Background(), managed.record.SessionID, managed.record.AgentID, "manager closed")
	}
	return nil
}

func cloneAgentRecord(record Record) Record {
	cloned := record
	if record.Usage.InputTokens != nil {
		value := *record.Usage.InputTokens
		cloned.Usage.InputTokens = &value
	}
	if record.Usage.OutputTokens != nil {
		value := *record.Usage.OutputTokens
		cloned.Usage.OutputTokens = &value
	}
	if record.FailureReason != nil {
		envelope := *record.FailureReason
		if record.FailureReason.ProviderCode != nil {
			value := *record.FailureReason.ProviderCode
			envelope.ProviderCode = &value
		}
		if record.FailureReason.Details != nil {
			envelope.Details = make(map[string]any, len(record.FailureReason.Details))
			for key, value := range record.FailureReason.Details {
				envelope.Details[key] = value
			}
		}
		cloned.FailureReason = &envelope
	}
	return cloned
}
