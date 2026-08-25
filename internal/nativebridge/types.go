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
	ErrorInvalid        = "NATIVE_BRIDGE_INVALID"
	ErrorOwnership      = "NATIVE_BRIDGE_OWNERSHIP_MISMATCH"
	ErrorWorkspace      = "NATIVE_BRIDGE_WORKSPACE_MISMATCH"
	ErrorLost           = "NATIVE_BRIDGE_LOST"
	ErrorReconciliation = "NATIVE_BRIDGE_RECONCILIATION_FAILED"
	ErrorNative         = "NATIVE_BRIDGE_NATIVE_FAILED"
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
