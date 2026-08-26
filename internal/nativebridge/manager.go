package nativebridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

type Manager struct {
	sessions       SessionStore
	native         NativeClient
	now            func() time.Time
	id             func() string
	runtimeSHA     string
	serverIdentity string

	keyMu sync.Mutex
	keys  map[string]*keyedLock
}

func New(options Options) (*Manager, error) {
	if options.Sessions == nil {
		return nil, &Error{Code: ErrorInvalid, Message: "native bridge requires AgentDock session authority"}
	}
	if options.Native == nil {
		return nil, &Error{Code: ErrorInvalid, Message: "native bridge requires OpenCode native client"}
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.SubagentIDFactory == nil {
		options.SubagentIDFactory = randomSubagentID
	}
	return &Manager{
		sessions: options.Sessions, native: options.Native, now: options.Now, id: options.SubagentIDFactory,
		runtimeSHA: strings.TrimSpace(options.NativeRuntimeSHA), serverIdentity: strings.TrimSpace(options.NativeServerIdentity),
		keys: map[string]*keyedLock{},
	}, nil
}

func randomSubagentID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return "subagent_" + hex.EncodeToString(value[:])
}

func (m *Manager) Start(ctx context.Context, request StartRequest) (session.NativeSessionBinding, error) {
	request.AgentDockSessionID = strings.TrimSpace(request.AgentDockSessionID)
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	request.SubagentID = strings.TrimSpace(request.SubagentID)
	request.Workspace = strings.TrimSpace(request.Workspace)
	request.Provider = strings.TrimSpace(request.Provider)
	request.Model = strings.TrimSpace(request.Model)
	request.ExecutionMode = strings.TrimSpace(request.ExecutionMode)
	if request.AgentDockSessionID == "" || request.ClientRequestID == "" {
		return session.NativeSessionBinding{}, &Error{Code: ErrorInvalid, Message: "agentdock_session_id and client_request_id are required"}
	}
	if request.ExecutionMode == "" {
		request.ExecutionMode = "native-session"
	}

	record, err := m.sessions.Resolve(request.AgentDockSessionID, request.Workspace)
	if err != nil {
		return session.NativeSessionBinding{}, m.authorityError("resolve AgentDock session authority", err)
	}
	if existing, ok, err := m.sessions.NativeSessionBindingByRequest(record.SessionID, request.ClientRequestID); err != nil {
		return session.NativeSessionBinding{}, m.authorityError("read durable native mapping", err)
	} else if ok {
		if err := authorizeBinding(record, existing); err != nil {
			return session.NativeSessionBinding{}, err
		}
		if existing.State == session.NativeConversationLost {
			return existing, &Error{Code: ErrorLost, Message: "durable native mapping is LOST; refusing replacement session"}
		}
		return existing, nil
	}

	release := m.acquireKey(record.SessionID + "\x00" + request.ClientRequestID)
	defer release()
	if existing, ok, err := m.sessions.NativeSessionBindingByRequest(record.SessionID, request.ClientRequestID); err != nil {
		return session.NativeSessionBinding{}, m.authorityError("read durable native mapping", err)
	} else if ok {
		if err := authorizeBinding(record, existing); err != nil {
			return session.NativeSessionBinding{}, err
		}
		if existing.State == session.NativeConversationLost {
			return existing, &Error{Code: ErrorLost, Message: "durable native mapping is LOST; refusing replacement session"}
		}
		return existing, nil
	}

	if _, err := m.native.WaitReady(ctx, record.Workspace()); err != nil {
		return session.NativeSessionBinding{}, &Error{Code: ErrorNative, Message: "OpenCode native server readiness failed", Cause: err}
	}

	sessions, err := m.native.ListNativeSessions(ctx, record.Workspace())
	if err != nil {
		return session.NativeSessionBinding{}, &Error{Code: ErrorNative, Message: "list OpenCode native sessions for reconciliation", Cause: err}
	}
	nativeInfo, found, err := reconcileNativeSession(sessions, record, request)
	if err != nil {
		return session.NativeSessionBinding{}, err
	}
	if found {
		markerSubagent := metadataString(nativeInfo.Metadata, metadataSubagentID)
		if request.SubagentID != "" && request.SubagentID != markerSubagent {
			return session.NativeSessionBinding{}, &Error{Code: ErrorReconciliation, Message: "reconciled native session belongs to a different subagent id"}
		}
		request.SubagentID = markerSubagent
		for _, pair := range []struct {
			name    string
			current *string
			native  string
		}{
			{name: "provider", current: &request.Provider, native: metadataString(nativeInfo.Metadata, metadataProvider)},
			{name: "model", current: &request.Model, native: metadataString(nativeInfo.Metadata, metadataModel)},
			{name: "execution_mode", current: &request.ExecutionMode, native: metadataString(nativeInfo.Metadata, metadataExecutionMode)},
		} {
			if *pair.current != "" && pair.native != "" && *pair.current != pair.native {
				return session.NativeSessionBinding{}, &Error{Code: ErrorReconciliation, Message: "reconciled native session " + pair.name + " differs from logical start"}
			}
			if pair.native != "" {
				*pair.current = pair.native
			}
		}
	} else {
		if request.SubagentID == "" {
			request.SubagentID = strings.TrimSpace(m.id())
		}
		if request.SubagentID == "" {
			return session.NativeSessionBinding{}, &Error{Code: ErrorInvalid, Message: "subagent id factory returned empty id"}
		}
		metadata := map[string]any{
			metadataSubagentID:       request.SubagentID,
			metadataSessionID:        record.SessionID,
			metadataClientRequestID:  request.ClientRequestID,
			metadataRuntimeScope:     runtimeScope(record.SessionID),
			metadataPermissionMode:   record.PermissionMode,
			metadataProvider:         request.Provider,
			metadataModel:            request.Model,
			metadataExecutionMode:    request.ExecutionMode,
			metadataNativeRuntimeSHA: m.runtimeSHA,
		}
		nativeInfo, err = m.native.CreateNativeSession(ctx, record.Workspace(), opencodenative.CreateSessionRequest{
			Title:    "AgentDock D3 " + request.SubagentID,
			Metadata: metadata,
		})
		if err != nil {
			return session.NativeSessionBinding{}, &Error{Code: ErrorNative, Message: "create OpenCode native session", Cause: err}
		}
		if !samePath(nativeInfo.Directory, record.Workspace()) {
			return session.NativeSessionBinding{}, &Error{Code: ErrorWorkspace, Message: "OpenCode created native session outside canonical AgentDock workspace"}
		}
	}

	now := m.now().UTC()
	binding := session.NativeSessionBinding{
		SubagentID: request.SubagentID, AgentDockSessionID: record.SessionID, ClientRequestID: request.ClientRequestID,
		OpenCodeSessionID: nativeInfo.ID, Workspace: record.Workspace(), RuntimeScope: runtimeScope(record.SessionID),
		PermissionMode: record.PermissionMode, Provider: request.Provider, Model: request.Model,
		ExecutionMode: request.ExecutionMode, State: session.NativeConversationActive, ExecutionState: session.NativeExecutionIdle,
		LastEventCursor: 0, CreatedAt: now, LastActivity: now, TerminalState: "", ResultRefs: []session.NativeResultRef{},
		NativeRuntimeSHA: m.runtimeSHA, NativeServerIdentity: m.serverIdentity,
	}
	persisted, err := m.sessions.BindNativeSession(record.SessionID, binding)
	if err != nil {
		// Deliberately leave the tagged native session in place. A retry will
		// reconcile by metadata and persist this exact OpenCodeSessionID instead
		// of issuing a second POST /session.
		return session.NativeSessionBinding{}, &Error{Code: ErrorReconciliation, Message: "persist native mapping after native session creation", Cause: err}
	}
	return persisted, nil
}

func (m *Manager) Attach(ctx context.Context, agentDockSessionID, subagentID, requestedWorkspace string) (session.NativeSessionBinding, opencodenative.SessionInfo, error) {
	record, err := m.sessions.Resolve(strings.TrimSpace(agentDockSessionID), strings.TrimSpace(requestedWorkspace))
	if err != nil {
		return session.NativeSessionBinding{}, opencodenative.SessionInfo{}, m.authorityError("resolve AgentDock session authority", err)
	}
	binding, err := m.sessions.NativeSessionBinding(record.SessionID, strings.TrimSpace(subagentID))
	if err != nil {
		return session.NativeSessionBinding{}, opencodenative.SessionInfo{}, &Error{Code: ErrorOwnership, Message: "subagent does not belong to requested AgentDock session", Cause: err}
	}
	if err := authorizeBinding(record, binding); err != nil {
		return session.NativeSessionBinding{}, opencodenative.SessionInfo{}, err
	}
	if binding.State == session.NativeConversationLost {
		return binding, opencodenative.SessionInfo{}, &Error{Code: ErrorLost, Message: "native session mapping is LOST"}
	}

	info, err := m.native.GetNativeSession(ctx, binding.Workspace, binding.OpenCodeSessionID)
	if err != nil {
		if opencodenative.ErrorCode(err) == opencodenative.ErrorNotFound {
			lost, markErr := m.sessions.MarkNativeSessionLost(record.SessionID, binding.SubagentID)
			if markErr == nil {
				binding = lost
			}
			return binding, opencodenative.SessionInfo{}, &Error{Code: ErrorLost, Message: "mapped OpenCode native session no longer exists; replacement was not created", Cause: err}
		}
		return binding, opencodenative.SessionInfo{}, &Error{Code: ErrorNative, Message: "attach exact OpenCode native session", Cause: err}
	}
	if info.ID != binding.OpenCodeSessionID || !samePath(info.Directory, binding.Workspace) || !nativeMetadataMatchesBinding(info.Metadata, binding) {
		lost, markErr := m.sessions.MarkNativeSessionLost(record.SessionID, binding.SubagentID)
		if markErr == nil {
			binding = lost
		}
		return binding, opencodenative.SessionInfo{}, &Error{Code: ErrorLost, Message: "mapped OpenCode native identity no longer matches durable AgentDock ownership"}
	}
	return binding, info, nil
}

func (m *Manager) SubmitMessage(ctx context.Context, sessionID, subagentID, workspace string, prompt opencodenative.PromptRequest) (opencodenative.Message, error) {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return opencodenative.Message{}, err
	}
	message, err := m.native.SubmitNativeMessage(ctx, binding.Workspace, binding.OpenCodeSessionID, prompt)
	if err != nil {
		return opencodenative.Message{}, &Error{Code: ErrorNative, Message: "submit native message", Cause: err}
	}
	_, _ = m.sessions.UpdateNativeSessionBinding(binding.AgentDockSessionID, binding.SubagentID, func(value *session.NativeSessionBinding) error {
		value.LastNativeMessageID = message.Info.ID
		return nil
	})
	return message, nil
}

func (m *Manager) SubmitPromptAsync(ctx context.Context, sessionID, subagentID, workspace string, prompt opencodenative.PromptRequest) error {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return err
	}
	if err := m.native.SubmitNativePromptAsync(ctx, binding.Workspace, binding.OpenCodeSessionID, prompt); err != nil {
		return &Error{Code: ErrorNative, Message: "submit native async prompt", Cause: err}
	}
	return nil
}

func (m *Manager) ContinueConversation(ctx context.Context, sessionID, subagentID, workspace string, prompt opencodenative.PromptRequest) error {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return err
	}
	if err := m.native.ContinueNativeConversation(ctx, binding.Workspace, binding.OpenCodeSessionID, prompt); err != nil {
		return &Error{Code: ErrorNative, Message: "continue native conversation on same session", Cause: err}
	}
	return nil
}

func (m *Manager) ListMessages(ctx context.Context, sessionID, subagentID, workspace string) ([]opencodenative.Message, error) {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return nil, err
	}
	messages, err := m.native.ListNativeMessages(ctx, binding.Workspace, binding.OpenCodeSessionID)
	if err != nil {
		return nil, &Error{Code: ErrorNative, Message: "list native messages", Cause: err}
	}
	return messages, nil
}

func (m *Manager) ObserveStatus(ctx context.Context, sessionID, subagentID, workspace string) (Status, error) {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return Status{}, err
	}
	statuses, err := m.native.ObserveNativeStatus(ctx, binding.Workspace)
	if err != nil {
		return Status{}, &Error{Code: ErrorNative, Message: "observe native session status", Cause: err}
	}
	value, ok := statuses[binding.OpenCodeSessionID]
	return Status{OpenCodeSessionID: binding.OpenCodeSessionID, Present: ok, Value: append(json.RawMessage(nil), value...)}, nil
}

func (m *Manager) Abort(ctx context.Context, sessionID, subagentID, workspace string) error {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return err
	}
	if err := m.native.AbortNativeExecution(ctx, binding.Workspace, binding.OpenCodeSessionID); err != nil {
		return &Error{Code: ErrorNative, Message: "abort native execution", Cause: err}
	}
	return nil
}

func (m *Manager) ProviderModelMetadata(ctx context.Context, sessionID, subagentID, workspace string) (json.RawMessage, error) {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return nil, err
	}
	result, err := m.native.DiscoverNativeProviderModelMetadata(ctx, binding.Workspace)
	if err != nil {
		return nil, &Error{Code: ErrorNative, Message: "discover native provider/model metadata", Cause: err}
	}
	return result, nil
}

func (m *Manager) ModelDiscovery(ctx context.Context, sessionID, subagentID, workspace string) (opencodenative.ModelDiscovery, error) {
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return opencodenative.ModelDiscovery{}, err
	}
	result, err := m.native.DiscoverNativeModels(ctx, binding.Workspace)
	if err != nil {
		return opencodenative.ModelDiscovery{}, &Error{Code: ErrorNative, Message: "discover native models", Cause: err}
	}
	return result, nil
}

func (m *Manager) ObserveEvents(ctx context.Context, sessionID, subagentID, workspace string, onEvent func(opencodenative.NativeEvent, int64) error) error {
	if onEvent == nil {
		return &Error{Code: ErrorInvalid, Message: "event callback is required"}
	}
	binding, _, err := m.Attach(ctx, sessionID, subagentID, workspace)
	if err != nil {
		return err
	}
	items, err := m.native.ListNativeSessions(ctx, binding.Workspace)
	if err != nil {
		return &Error{Code: ErrorNative, Message: "list native sessions for descendant ownership closure", Cause: err}
	}
	owned := descendantOwnershipClosure(items, binding.OpenCodeSessionID, binding.Workspace)
	return m.native.ObserveNativeEvents(ctx, binding.Workspace, func(event opencodenative.NativeEvent) error {
		if _, ok := owned[nativeEventSessionID(event)]; !ok {
			return nil
		}
		publicEvent := classifyNativePublicEvent(event)
		publicEvent.CreatedAt = m.now().UTC()
		_, ownedCursor, applied, updateErr := m.sessions.AppendNativeOwnedPublicEvent(binding.AgentDockSessionID, binding.SubagentID, publicEvent)
		if updateErr != nil {
			return &Error{Code: ErrorReconciliation, Message: "persist AgentDock-owned native event provenance/cursor", Cause: updateErr}
		}
		if !applied {
			return nil
		}
		return onEvent(event, ownedCursor)
	})
}

// descendantOwnershipClosure returns the native root plus only sessions whose
// parent chain reaches that exact root inside the same canonical workspace.
// Orphans, cycles, and unrelated roots are excluded and never promoted into
// AgentDock top-level subagents.
func descendantOwnershipClosure(items []opencodenative.SessionInfo, rootSessionID, workspace string) map[string]struct{} {
	rootSessionID = strings.TrimSpace(rootSessionID)
	owned := map[string]struct{}{}
	if rootSessionID == "" {
		return owned
	}
	owned[rootSessionID] = struct{}{}
	remaining := make(map[string]opencodenative.SessionInfo, len(items))
	for _, item := range items {
		if item.ID == "" || item.ID == rootSessionID || !samePath(item.Directory, workspace) {
			continue
		}
		remaining[item.ID] = item
	}
	for {
		changed := false
		for id, item := range remaining {
			parentID := strings.TrimSpace(item.ParentID)
			if parentID == "" {
				continue
			}
			if _, ok := owned[parentID]; !ok {
				continue
			}
			owned[id] = struct{}{}
			delete(remaining, id)
			changed = true
		}
		if !changed {
			break
		}
	}
	return owned
}

func authorizeBinding(record session.Record, binding session.NativeSessionBinding) error {
	if binding.AgentDockSessionID != record.SessionID {
		return &Error{Code: ErrorOwnership, Message: "native mapping AgentDock session ownership mismatch"}
	}
	if binding.RuntimeScope != runtimeScope(record.SessionID) {
		return &Error{Code: ErrorOwnership, Message: "native mapping runtime scope ownership mismatch"}
	}
	if binding.PermissionMode != record.PermissionMode {
		return &Error{Code: ErrorOwnership, Message: "native mapping permission context mismatch"}
	}
	if !samePath(binding.Workspace, record.Workspace()) {
		return &Error{Code: ErrorWorkspace, Message: "native mapping workspace mismatch"}
	}
	return nil
}

func reconcileNativeSession(items []opencodenative.SessionInfo, record session.Record, request StartRequest) (opencodenative.SessionInfo, bool, error) {
	matches := make([]opencodenative.SessionInfo, 0, 1)
	for _, item := range items {
		if strings.TrimSpace(item.ParentID) != "" {
			continue
		}
		if metadataString(item.Metadata, metadataSessionID) != record.SessionID || metadataString(item.Metadata, metadataClientRequestID) != request.ClientRequestID {
			continue
		}
		if metadataString(item.Metadata, metadataRuntimeScope) != runtimeScope(record.SessionID) || metadataString(item.Metadata, metadataPermissionMode) != record.PermissionMode {
			return opencodenative.SessionInfo{}, false, &Error{Code: ErrorReconciliation, Message: "native reconciliation metadata conflicts with AgentDock authority"}
		}
		if metadataString(item.Metadata, metadataSubagentID) == "" || !samePath(item.Directory, record.Workspace()) {
			return opencodenative.SessionInfo{}, false, &Error{Code: ErrorReconciliation, Message: "native reconciliation candidate has invalid identity/workspace metadata"}
		}
		matches = append(matches, item)
	}
	if len(matches) > 1 {
		return opencodenative.SessionInfo{}, false, &Error{Code: ErrorReconciliation, Message: "multiple native sessions match one logical AgentDock start; refusing ambiguous ownership"}
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	return opencodenative.SessionInfo{}, false, nil
}

func nativeMetadataMatchesBinding(metadata map[string]any, binding session.NativeSessionBinding) bool {
	return metadataString(metadata, metadataSubagentID) == binding.SubagentID &&
		metadataString(metadata, metadataSessionID) == binding.AgentDockSessionID &&
		metadataString(metadata, metadataClientRequestID) == binding.ClientRequestID &&
		metadataString(metadata, metadataRuntimeScope) == binding.RuntimeScope &&
		metadataString(metadata, metadataPermissionMode) == binding.PermissionMode &&
		metadataString(metadata, metadataProvider) == binding.Provider &&
		metadataString(metadata, metadataModel) == binding.Model &&
		metadataString(metadata, metadataExecutionMode) == binding.ExecutionMode
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func nativeEventSessionID(event opencodenative.NativeEvent) string {
	var properties map[string]any
	if len(event.Properties) == 0 || json.Unmarshal(event.Properties, &properties) != nil {
		return ""
	}
	for _, key := range []string{"sessionID", "sessionId", "session_id"} {
		if value, ok := properties[key]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	if part, ok := properties["part"].(map[string]any); ok {
		if value, ok := part["sessionID"]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	if info, ok := properties["info"].(map[string]any); ok {
		if value, ok := info["sessionID"]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func runtimeScope(sessionID string) string { return "session:" + strings.TrimSpace(sessionID) }

func samePath(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (m *Manager) acquireKey(key string) func() {
	m.keyMu.Lock()
	entry := m.keys[key]
	if entry == nil {
		entry = &keyedLock{}
		m.keys[key] = entry
	}
	entry.refs++
	m.keyMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.keyMu.Lock()
		entry.refs--
		if entry.refs == 0 && m.keys[key] == entry {
			delete(m.keys, key)
		}
		m.keyMu.Unlock()
	}
}

func (m *Manager) authorityError(message string, err error) error {
	code := ErrorOwnership
	if session.ErrorCode(err) == session.CodeWorkspaceMismatch {
		code = ErrorWorkspace
	}
	return &Error{Code: code, Message: message, Cause: err}
}
