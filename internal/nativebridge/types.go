package nativebridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

const (
	ErrorInvalid             = "NATIVE_BRIDGE_INVALID"
	ErrorOwnership           = "NATIVE_BRIDGE_OWNERSHIP_MISMATCH"
	ErrorWorkspace           = "NATIVE_BRIDGE_WORKSPACE_MISMATCH"
	ErrorLost                = "NATIVE_BRIDGE_LOST"
	ErrorReconciliation      = "NATIVE_BRIDGE_RECONCILIATION_FAILED"
	ErrorNative              = "NATIVE_BRIDGE_NATIVE_FAILED"
	ErrorSameSession         = "NATIVE_BRIDGE_SAME_SESSION_GATE_FAILED"
	ErrorPermissionOwnership = "NATIVE_BRIDGE_PERMISSION_OWNERSHIP_MISMATCH"
)

const (
	metadataSubagentID       = "agentdock_d3_subagent_id"
	metadataSessionID        = "agentdock_session_id"
	metadataClientRequestID  = "agentdock_client_request_id"
	metadataRuntimeScope     = "agentdock_runtime_scope"
	metadataPermissionMode   = "agentdock_permission_mode"
	metadataProvider         = "agentdock_provider"
	metadataModel            = "agentdock_model"
	metadataExecutionMode    = "agentdock_execution_mode"
	metadataNativeRuntimeSHA = "agentdock_native_runtime_sha"
)

type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}
func (e *Error) Unwrap() error { return e.Cause }
func ErrorCode(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

type SessionStore interface {
	Resolve(sessionID, requestedWorkspace string) (session.Record, error)
	NativeSessionBinding(sessionID, subagentID string) (session.NativeSessionBinding, error)
	NativeSessionBindingByRequest(sessionID, clientRequestID string) (session.NativeSessionBinding, bool, error)
	BindNativeSession(sessionID string, binding session.NativeSessionBinding) (session.NativeSessionBinding, error)
	UpdateNativeSessionBinding(sessionID, subagentID string, update func(*session.NativeSessionBinding) error) (session.NativeSessionBinding, error)
	MarkNativeSessionLost(sessionID, subagentID string) (session.NativeSessionBinding, error)
	AppendNativePublicEvent(sessionID, subagentID string, event session.NativePublicEvent) (session.NativePublicEvent, error)
	NativePublicEvents(sessionID, subagentID string, after int64, limit int) (session.NativePublicEventPage, error)
	AdmitNativeSupervisorMessage(sessionID, subagentID, clientMessageID, kind, supervisorMessageID string, promptPayload json.RawMessage) (session.NativeSupervisorMessage, bool, error)
	UpdateNativeSupervisorMessage(sessionID, subagentID, supervisorMessageID string, update func(*session.NativeSupervisorMessage) error) (session.NativeSupervisorMessage, error)
	AdmitNativePermissionApproval(sessionID, subagentID string, approval session.NativePermissionApproval) (session.NativePermissionApproval, bool, error)
	DecideNativePermissionApproval(sessionID, subagentID, approvalRequestID, clientDecisionID, decision string) (session.NativePermissionApproval, bool, error)
}

type NativeClient interface {
	WaitReady(context.Context, string) (opencodenative.ReadinessResult, error)
	CreateNativeSession(context.Context, string, opencodenative.CreateSessionRequest) (opencodenative.SessionInfo, error)
	GetNativeSession(context.Context, string, string) (opencodenative.SessionInfo, error)
	ListNativeSessions(context.Context, string) ([]opencodenative.SessionInfo, error)
	SubmitNativeMessage(context.Context, string, string, opencodenative.PromptRequest) (opencodenative.Message, error)
	SubmitNativePromptAsync(context.Context, string, string, opencodenative.PromptRequest) error
	ListNativeMessages(context.Context, string, string) ([]opencodenative.Message, error)
	ObserveNativeStatus(context.Context, string) (opencodenative.StatusMap, error)
	ObserveNativeEvents(context.Context, string, func(opencodenative.NativeEvent) error) error
	AbortNativeExecution(context.Context, string, string) error
	ContinueNativeConversation(context.Context, string, string, opencodenative.PromptRequest) error
	ListNativePermissions(context.Context, string) ([]opencodenative.PermissionRequest, error)
	ReplyNativePermission(context.Context, string, string, opencodenative.PermissionReplyRequest) error
	DiscoverNativeProviderModelMetadata(context.Context, string) (json.RawMessage, error)
}

type Options struct {
	Sessions             SessionStore
	Native               NativeClient
	Now                  func() time.Time
	SubagentIDFactory    func() string
	NativeRuntimeSHA     string
	NativeServerIdentity string
}

type StartRequest struct {
	AgentDockSessionID string
	ClientRequestID    string
	SubagentID         string
	Workspace          string
	Provider           string
	Model              string
	ExecutionMode      string
}

type Status struct {
	OpenCodeSessionID string
	Present           bool
	Value             json.RawMessage
}

type SupervisorMessageRequest struct {
	AgentDockSessionID string
	SubagentID         string
	Workspace          string
	ClientMessageID    string
	Kind               string
	Prompt             opencodenative.PromptRequest
}

type SupervisorMessageResult struct {
	OpenCodeSessionID string                          `json:"opencode_session_id"`
	Message           session.NativeSupervisorMessage `json:"message"`
	Created           bool                            `json:"created"`
	Dispatched        bool                            `json:"dispatched"`
}

type OwnedMessage struct {
	OpenCodeSessionID string                 `json:"opencode_session_id"`
	Message           opencodenative.Message `json:"message"`
}

type SupervisorObservation struct {
	RootOpenCodeSessionID string                         `json:"root_opencode_session_id"`
	Statuses              map[string]json.RawMessage     `json:"statuses"`
	Messages              []OwnedMessage                 `json:"messages"`
	PublicEvents          session.NativePublicEventPage  `json:"public_events"`
	PendingPermissions    []SupervisorPermissionIdentity `json:"pending_permissions"`
}

type CorrectionMode string

const (
	CorrectionGraceful CorrectionMode = "graceful"
	CorrectionForce    CorrectionMode = "force"
)

type CorrectionRequest struct {
	Mode   CorrectionMode
	Prompt opencodenative.PromptRequest
}

type CorrectionResult struct {
	Mode                    CorrectionMode `json:"mode"`
	OrderID                 string         `json:"order_id"`
	OpenCodeSessionIDBefore string         `json:"opencode_session_id_before"`
	OpenCodeSessionIDAfter  string         `json:"opencode_session_id_after"`
	Aborted                 bool           `json:"aborted"`
	PreemptiveClaimed       bool           `json:"preemptive_claimed"`
}

type CancelResult struct {
	OpenCodeSessionID string `json:"opencode_session_id"`
	Aborted           bool   `json:"aborted"`
}

type PermissionDecision string

const (
	PermissionDecisionApprove PermissionDecision = "approve"
	PermissionDecisionReject  PermissionDecision = "reject"
)

type SupervisorPermissionIdentity struct {
	RequestID             string                         `json:"request_id"`
	NativeSessionID       string                         `json:"native_session_id"`
	RootOpenCodeSessionID string                         `json:"root_opencode_session_id"`
	AgentDockSessionID    string                         `json:"agentdock_session_id"`
	SubagentID            string                         `json:"subagent_id"`
	Workspace             string                         `json:"workspace"`
	PermissionMode        string                         `json:"permission_mode"`
	Permission            string                         `json:"permission"`
	Patterns              []string                       `json:"patterns"`
	Metadata              map[string]any                 `json:"metadata"`
	Always                []string                       `json:"always"`
	Tool                  *opencodenative.PermissionTool `json:"tool,omitempty"`
	ApprovalScope         string                         `json:"approval_scope"`
}
