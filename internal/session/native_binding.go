package session

import (
	"fmt"
	"strings"
	"time"
)

const (
	CodeNativeBindingNotFound = "SESSION_NATIVE_BINDING_NOT_FOUND"
	CodeNativeBindingConflict = "SESSION_NATIVE_BINDING_CONFLICT"
	CodeNativeBindingLost     = "SESSION_NATIVE_BINDING_LOST"
)

type NativeConversationState string

const (
	NativeConversationActive NativeConversationState = "ACTIVE"
	NativeConversationClosed NativeConversationState = "CLOSED"
	NativeConversationLost   NativeConversationState = "LOST"
)

type NativeExecutionState string

const (
	NativeExecutionIdle              NativeExecutionState = "IDLE"
	NativeExecutionStarting          NativeExecutionState = "STARTING"
	NativeExecutionRunning           NativeExecutionState = "RUNNING"
	NativeExecutionWaitingPermission NativeExecutionState = "WAITING_PERMISSION"
	NativeExecutionCancelRequested   NativeExecutionState = "CANCEL_REQUESTED"
	NativeExecutionSucceeded         NativeExecutionState = "SUCCEEDED"
	NativeExecutionFailed            NativeExecutionState = "FAILED"
	NativeExecutionCancelled         NativeExecutionState = "CANCELLED"
	NativeExecutionInterrupted       NativeExecutionState = "INTERRUPTED"
	NativeExecutionTimedOut          NativeExecutionState = "TIMED_OUT"
)

type NativeResultRef struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// NativeSessionBinding is the AgentDock-owned durable identity for one D3 root
// subagent conversation. OpenCode descendant sessions are provenance of this
// root binding, not additional AgentDock top-level subagents.
type NativeSessionBinding struct {
	SubagentID                 string                     `json:"subagent_id"`
	AgentDockSessionID         string                     `json:"agentdock_session_id"`
	ClientRequestID            string                     `json:"client_request_id"`
	OpenCodeSessionID          string                     `json:"opencode_session_id"`
	Workspace                  string                     `json:"workspace"`
	RuntimeScope               string                     `json:"runtime_scope"`
	PermissionMode             string                     `json:"permission_mode"`
	Provider                   string                     `json:"provider"`
	Model                      string                     `json:"model"`
	ExecutionMode              string                     `json:"execution_mode"`
	State                      NativeConversationState    `json:"state"`
	ExecutionState             NativeExecutionState       `json:"execution_state"`
	LastEventCursor            int64                      `json:"last_event_cursor"`
	CreatedAt                  time.Time                  `json:"created_at"`
	LastActivity               time.Time                  `json:"last_activity"`
	TerminalState              string                     `json:"terminal_state"`
	ResultRefs                 []NativeResultRef          `json:"result_refs"`
	CurrentExecutionID         string                     `json:"current_execution_id,omitempty"`
	LastNativeEventID          string                     `json:"last_native_event_id,omitempty"`
	LastNativeEventSeq         int64                      `json:"last_native_event_seq,omitempty"`
	LastNativeMessageID        string                     `json:"last_native_message_id,omitempty"`
	LastNativePartID           string                     `json:"last_native_part_id,omitempty"`
	PendingSupervisorMessageID string                     `json:"pending_supervisor_message_id,omitempty"`
	NextSupervisorMessageSeq   int64                      `json:"next_supervisor_message_seq,omitempty"`
	NextApprovalSeq            int64                      `json:"next_approval_seq,omitempty"`
	LastOwnedEventCursor       int64                      `json:"last_owned_event_cursor,omitempty"`
	SupervisorMessages         []NativeSupervisorMessage  `json:"supervisor_messages,omitempty"`
	PermissionApprovals        []NativePermissionApproval `json:"permission_approvals,omitempty"`
	NativeRuntimeSHA           string                     `json:"native_runtime_sha,omitempty"`
	NativeServerIdentity       string                     `json:"native_server_identity,omitempty"`
	NativePublicEvents         []NativePublicEvent        `json:"native_public_events,omitempty"`
}

func nativeRuntimeScope(sessionID string) string {
	return "session:" + strings.TrimSpace(sessionID)
}

func knownNativeConversationState(state NativeConversationState) bool {
	switch state {
	case NativeConversationActive, NativeConversationClosed, NativeConversationLost:
		return true
	default:
		return false
	}
}

func knownNativeExecutionState(state NativeExecutionState) bool {
	switch state {
	case NativeExecutionIdle, NativeExecutionStarting, NativeExecutionRunning, NativeExecutionWaitingPermission,
		NativeExecutionCancelRequested, NativeExecutionSucceeded, NativeExecutionFailed, NativeExecutionCancelled,
		NativeExecutionInterrupted, NativeExecutionTimedOut:
		return true
	default:
		return false
	}
}

func normalizeNativeBinding(record Record, binding NativeSessionBinding, now time.Time) (NativeSessionBinding, error) {
	binding.SubagentID = strings.TrimSpace(binding.SubagentID)
	binding.AgentDockSessionID = strings.TrimSpace(binding.AgentDockSessionID)
	binding.ClientRequestID = strings.TrimSpace(binding.ClientRequestID)
	binding.OpenCodeSessionID = strings.TrimSpace(binding.OpenCodeSessionID)
	binding.Workspace = strings.TrimSpace(binding.Workspace)
	binding.RuntimeScope = strings.TrimSpace(binding.RuntimeScope)
	binding.PermissionMode = strings.TrimSpace(binding.PermissionMode)
	binding.Provider = strings.TrimSpace(binding.Provider)
	binding.Model = strings.TrimSpace(binding.Model)
	binding.ExecutionMode = strings.TrimSpace(binding.ExecutionMode)
	binding.NativeRuntimeSHA = strings.TrimSpace(binding.NativeRuntimeSHA)
	binding.NativeServerIdentity = strings.TrimSpace(binding.NativeServerIdentity)

	if binding.SubagentID == "" || binding.ClientRequestID == "" || binding.OpenCodeSessionID == "" {
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "subagent_id, client_request_id, and opencode_session_id are required", nil)
	}
	if binding.AgentDockSessionID != record.SessionID {
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "native binding AgentDock session ownership mismatch", nil)
	}
	if binding.RuntimeScope != nativeRuntimeScope(record.SessionID) {
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "native binding runtime scope does not match AgentDock session", nil)
	}
	if binding.PermissionMode != record.PermissionMode {
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "native binding permission context does not match AgentDock session", nil)
	}
	if binding.Workspace == "" || !pathEqual(binding.Workspace, record.Workspace()) {
		return NativeSessionBinding{}, newError(CodeWorkspaceMismatch, "native binding workspace does not match session canonical workspace", nil)
	}
	if binding.State == "" {
		binding.State = NativeConversationActive
	}
	if !knownNativeConversationState(binding.State) {
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "invalid native conversation state", nil)
	}
	if binding.ExecutionState == "" {
		binding.ExecutionState = NativeExecutionIdle
	}
	if !knownNativeExecutionState(binding.ExecutionState) {
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "invalid native execution state", nil)
	}
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now.UTC()
	}
	if binding.LastActivity.IsZero() {
		binding.LastActivity = binding.CreatedAt
	}
	if binding.ResultRefs == nil {
		binding.ResultRefs = []NativeResultRef{}
	}
	if binding.NativePublicEvents == nil {
		binding.NativePublicEvents = []NativePublicEvent{}
	}
	if err := normalizeNativeMultiturnBinding(&binding); err != nil {
		return NativeSessionBinding{}, err
	}
	return binding, nil
}

// BindNativeSession persists one root native session identity. The uniqueness
// invariant is per AgentDock session + client_request_id, with a second guard
// against reusing one OpenCode root session for another top-level subagent.
func (m *Manager) BindNativeSession(sessionID string, binding NativeSessionBinding) (NativeSessionBinding, error) {
	managed, err := m.activeManaged(sessionID)
	if err != nil {
		return NativeSessionBinding{}, err
	}
	m.mu.RLock()
	authority := cloneRecord(managed.record)
	m.mu.RUnlock()
	binding, err = normalizeNativeBinding(authority, binding, m.config.Now())
	if err != nil {
		return NativeSessionBinding{}, err
	}

	m.mu.Lock()
	if managed.record.NativeSessionBindings == nil {
		managed.record.NativeSessionBindings = map[string]NativeSessionBinding{}
	}
	if existing, ok := managed.record.NativeSessionBindings[binding.SubagentID]; ok {
		if existing.ClientRequestID == binding.ClientRequestID && existing.OpenCodeSessionID == binding.OpenCodeSessionID {
			result := cloneNativeSessionBinding(existing)
			m.mu.Unlock()
			return result, nil
		}
		m.mu.Unlock()
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "subagent is already bound to a different native session", nil)
	}
	for _, existing := range managed.record.NativeSessionBindings {
		if existing.ClientRequestID == binding.ClientRequestID {
			m.mu.Unlock()
			return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "client_request_id is already bound in this AgentDock session", nil)
		}
		if existing.OpenCodeSessionID == binding.OpenCodeSessionID {
			m.mu.Unlock()
			return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "OpenCode root session is already bound to another subagent", nil)
		}
	}
	managed.record.NativeSessionBindings[binding.SubagentID] = cloneNativeSessionBinding(binding)
	managed.record.LastActiveAt = m.config.Now().UTC()
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return NativeSessionBinding{}, fmt.Errorf("persist native session binding: %w", err)
	}
	return cloneNativeSessionBinding(binding), nil
}

func (m *Manager) NativeSessionBinding(sessionID, subagentID string) (NativeSessionBinding, error) {
	record, err := m.Get(sessionID)
	if err != nil {
		return NativeSessionBinding{}, err
	}
	binding, ok := record.NativeSessionBindings[strings.TrimSpace(subagentID)]
	if !ok {
		return NativeSessionBinding{}, newError(CodeNativeBindingNotFound, "native session binding not found", nil)
	}
	return cloneNativeSessionBinding(binding), nil
}

func (m *Manager) NativeSessionBindingByRequest(sessionID, clientRequestID string) (NativeSessionBinding, bool, error) {
	record, err := m.Get(sessionID)
	if err != nil {
		return NativeSessionBinding{}, false, err
	}
	requestID := strings.TrimSpace(clientRequestID)
	if requestID == "" {
		return NativeSessionBinding{}, false, nil
	}
	for _, binding := range record.NativeSessionBindings {
		if binding.ClientRequestID == requestID {
			return cloneNativeSessionBinding(binding), true, nil
		}
	}
	return NativeSessionBinding{}, false, nil
}

func (m *Manager) UpdateNativeSessionBinding(sessionID, subagentID string, update func(*NativeSessionBinding) error) (NativeSessionBinding, error) {
	if update == nil {
		return NativeSessionBinding{}, newError(CodeNativeBindingConflict, "native binding update is required", nil)
	}
	managed, err := m.activeManaged(sessionID)
	if err != nil {
		return NativeSessionBinding{}, err
	}
	key := strings.TrimSpace(subagentID)
	m.mu.Lock()
	binding, ok := managed.record.NativeSessionBindings[key]
	if !ok {
		m.mu.Unlock()
		return NativeSessionBinding{}, newError(CodeNativeBindingNotFound, "native session binding not found", nil)
	}
	candidate := cloneNativeSessionBinding(binding)
	if err := update(&candidate); err != nil {
		m.mu.Unlock()
		return NativeSessionBinding{}, err
	}
	candidate, err = normalizeNativeBinding(managed.record, candidate, m.config.Now())
	if err != nil {
		m.mu.Unlock()
		return NativeSessionBinding{}, err
	}
	candidate.LastActivity = m.config.Now().UTC()
	managed.record.NativeSessionBindings[key] = cloneNativeSessionBinding(candidate)
	managed.record.LastActiveAt = candidate.LastActivity
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return NativeSessionBinding{}, fmt.Errorf("persist native session binding update: %w", err)
	}
	return cloneNativeSessionBinding(candidate), nil
}

func (m *Manager) MarkNativeSessionLost(sessionID, subagentID string) (NativeSessionBinding, error) {
	return m.UpdateNativeSessionBinding(sessionID, subagentID, func(binding *NativeSessionBinding) error {
		binding.State = NativeConversationLost
		binding.ExecutionState = NativeExecutionInterrupted
		binding.TerminalState = "LOST"
		return nil
	})
}

func cloneNativeSessionBinding(binding NativeSessionBinding) NativeSessionBinding {
	copy := binding
	copy.ResultRefs = append([]NativeResultRef(nil), binding.ResultRefs...)
	if copy.ResultRefs == nil {
		copy.ResultRefs = []NativeResultRef{}
	}
	copy.SupervisorMessages = make([]NativeSupervisorMessage, len(binding.SupervisorMessages))
	for i, message := range binding.SupervisorMessages {
		copy.SupervisorMessages[i] = cloneNativeSupervisorMessage(message)
	}
	if copy.SupervisorMessages == nil {
		copy.SupervisorMessages = []NativeSupervisorMessage{}
	}
	copy.PermissionApprovals = append([]NativePermissionApproval(nil), binding.PermissionApprovals...)
	if copy.PermissionApprovals == nil {
		copy.PermissionApprovals = []NativePermissionApproval{}
	}
	copy.NativePublicEvents = make([]NativePublicEvent, len(binding.NativePublicEvents))
	for i, event := range binding.NativePublicEvents {
		copy.NativePublicEvents[i] = cloneNativePublicEvent(event)
	}
	if copy.NativePublicEvents == nil {
		copy.NativePublicEvents = []NativePublicEvent{}
	}
	return copy
}
