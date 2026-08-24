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
	request.CredentialRef = strings.TrimSpace(request.CredentialRef)
	if request.SessionID == "" || request.Provider == "" || request.MaxRequests < 1 {
		return Record{}, fmt.Errorf("%w: session_id, provider, and budget.max_requests >= 1 are required", ErrInvalidArguments)
	}
	if err := credential.ValidateRef(request.CredentialRef); err != nil {
		return Record{}, err
	}
	provider := m.options.Providers[request.Provider]
	if provider == nil {
		return Record{}, ErrProviderUnsupported
	}
	binding, err := m.options.Sessions.ResolveAgentSession(ctx, request.SessionID)
	if err != nil {
		return Record{}, err
	}
	if binding.SessionID != request.SessionID || strings.TrimSpace(binding.Workspace) == "" {
		return Record{}, ErrAgentSessionClosed
	}
	permission, err := normalizeAgentPermission(binding.PermissionMode)
	if err != nil {
		return Record{}, err
	}

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
		PermissionMode: permission, Provider: request.Provider, Model: request.Model, CredentialRef: request.CredentialRef,
		MaxRequests: request.MaxRequests, State: AgentStateQueued, CreatedAt: now, UpdatedAt: now,
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
		m.finishAgent(id, AgentStateFailed, ProviderResult{}, "credential resolution failed")
		return
	}
	defer credential.Zero(secret)
	m.mu.Lock()
	managed = m.agents[id]
	if managed == nil || managed.record.RequestsUsed >= managed.record.MaxRequests {
		m.mu.Unlock()
		m.finishAgent(id, AgentStateFailed, ProviderResult{}, ErrBudgetExceeded.Error())
		return
	}
	managed.record.RequestsUsed++
	managed.record.UpdatedAt = m.options.Now().UTC()
	record = cloneAgentRecord(managed.record)
	m.mu.Unlock()
	_ = m.persistence.save(record)
	provider := m.options.Providers[request.Provider]
	providerRequest := ProviderRequest{
		AgentID: id, SessionID: request.SessionID, Model: request.Model, Role: request.Role, Prompt: request.Prompt,
		Credential: secret, MaxTurns: request.MaxRequests,
	}
	result, runErr := provider.Run(ctx, providerRequest, func(event ProviderEvent) error {
		return m.appendEvent(id, event, secret)
	})
	result.Summary = sanitizeAgentText(result.Summary, secret)
	if runErr != nil {
		state := AgentStateFailed
		switch {
		case errors.Is(runErr, context.Canceled):
			state = AgentStateCancelled
		case errors.Is(runErr, context.DeadlineExceeded):
			state = AgentStateTimedOut
		}
		message := sanitizeAgentText(runErr.Error(), secret)
		m.finishAgent(id, state, result, message)
		return
	}
	m.finishAgent(id, AgentStateSucceeded, result, "")
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

func (m *Manager) finishAgent(id string, state AgentState, result ProviderResult, message string) {
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
	}
	now := m.options.Now().UTC()
	managed.record.State = state
	managed.record.FinishedAt = &now
	managed.record.UpdatedAt = now
	managed.record.ResolvedModel = result.ResolvedModel
	managed.record.StopReason = result.StopReason
	managed.record.ResultSummary = sanitizeAgentText(result.Summary, nil)
	managed.record.Error = redact.Text(message)
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

func cloneAgentRecord(record Record) Record { return record }
