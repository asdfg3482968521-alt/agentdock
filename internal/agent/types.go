package agent

import (
	"context"
	"errors"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

type AgentState string

const (
	AgentStateQueued          AgentState = "QUEUED"
	AgentStateStarting        AgentState = "STARTING"
	AgentStateRunning         AgentState = "RUNNING"
	AgentStateCancelRequested AgentState = "CANCEL_REQUESTED"
	AgentStateSucceeded       AgentState = "SUCCEEDED"
	AgentStateFailed          AgentState = "FAILED"
	AgentStateCancelled       AgentState = "CANCELLED"
	AgentStateTimedOut        AgentState = "TIMED_OUT"
	AgentStateInterrupted     AgentState = "INTERRUPTED"
)

func (s AgentState) Terminal() bool {
	switch s {
	case AgentStateSucceeded, AgentStateFailed, AgentStateCancelled, AgentStateTimedOut, AgentStateInterrupted:
		return true
	default:
		return false
	}
}

var (
	ErrAgentNotFound                 = errors.New("agent not found")
	ErrAgentSessionOwnershipMismatch = errors.New("agent session ownership mismatch")
	ErrAgentSessionClosed            = errors.New("agent session closed")
	ErrAgentWriteDenied              = errors.New("agent write denied")
	ErrAgentNotTerminal              = errors.New("agent not terminal")
	ErrInvalidAgentTransition        = errors.New("invalid agent transition")
	ErrInvalidArguments              = errors.New("invalid arguments")
	ErrProviderUnsupported           = errors.New("provider unsupported")
	ErrBudgetExceeded                = errors.New("budget exceeded")
	ErrResourceLimit                 = errors.New("resource limit")
	ErrToolError                     = errors.New("tool error")
)

type SessionBinding struct {
	SessionID      string `json:"session_id"`
	Workspace      string `json:"workspace"`
	RuntimeScope   string `json:"runtime_scope"`
	PermissionMode string `json:"permission_mode"`
}

type SessionResolver interface {
	ResolveAgentSession(context.Context, string) (SessionBinding, error)
}

type AgentLimits struct {
	MaxConcurrent            int
	MaxConcurrentPerProvider int
	MaxConcurrentPerSession  int
	MaxQueued                int
	MaxOutputBytesPerRead    int
	MaxOutputBytesPerAgent   int64
}

func DefaultAgentLimits() AgentLimits {
	return AgentLimits{
		MaxConcurrent:            4,
		MaxConcurrentPerProvider: 2,
		MaxConcurrentPerSession:  1,
		MaxQueued:                64,
		MaxOutputBytesPerRead:    256 << 10,
		MaxOutputBytesPerAgent:   16 << 20,
	}
}

type ManagerOptions struct {
	StorageRoot string
	Sessions    SessionResolver
	Credentials credential.Resolver
	Providers   map[string]ProviderAdapter
	Tools       ToolBridge
	Limits      AgentLimits
	Now         func() time.Time
	IDFactory   func() string
}

type StartRequest struct {
	SessionID      string
	Provider       string
	Model          string
	CredentialRef  string
	Role           string
	Prompt         string
	PermissionMode string
	MaxRequests    int
	Timeout        time.Duration
}

type AgentBudget struct {
	MaxRequests int `json:"max_requests"`
}

type AgentUsage struct {
	Requests     int    `json:"requests"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

type ErrorEnvelope struct {
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable"`
	ProviderCode *string        `json:"provider_code,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

type Record struct {
	AgentID        string         `json:"agent_id"`
	SessionID      string         `json:"session_id"`
	Workspace      string         `json:"workspace"`
	RuntimeScope   string         `json:"runtime_scope"`
	PermissionMode string         `json:"permission_mode"`
	Provider       string         `json:"provider"`
	Model          string         `json:"model"`
	ResolvedModel  string         `json:"resolved_model,omitempty"`
	Role           string         `json:"role"`
	CredentialRef  string         `json:"credential_ref"`
	TimeoutMS      int64          `json:"timeout_ms"`
	Budget         AgentBudget    `json:"budget"`
	Usage          AgentUsage     `json:"usage"`
	MaxRequests    int            `json:"max_requests"`
	RequestsUsed   int            `json:"requests_used"`
	State          AgentState     `json:"state"`
	CreatedAt      time.Time      `json:"created_at"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at"`
	OutputCursor   int64          `json:"output_cursor"`
	ResultSummary  string         `json:"result_summary,omitempty"`
	Error          string         `json:"error,omitempty"`
	FailureReason  *ErrorEnvelope `json:"failure_reason,omitempty"`
	StopReason     string         `json:"stop_reason,omitempty"`
}

type AgentResource struct {
	AgentID       string         `json:"agent_id"`
	SessionID     string         `json:"session_id"`
	Provider      string         `json:"provider"`
	Model         string         `json:"model"`
	Role          string         `json:"role"`
	CredentialRef string         `json:"credential_ref"`
	Permission    string         `json:"permission"`
	TimeoutMS     int64          `json:"timeout_ms"`
	Budget        AgentBudget    `json:"budget"`
	State         AgentState     `json:"state"`
	CreatedAt     time.Time      `json:"created_at"`
	StartedAt     *time.Time     `json:"started_at"`
	FinishedAt    *time.Time     `json:"finished_at"`
	FailureReason *ErrorEnvelope `json:"failure_reason"`
	RuntimeScope  string         `json:"runtime_scope"`
	OutputCursor  int64          `json:"output_cursor"`
	Usage         AgentUsage     `json:"usage"`
}

func PublicResource(record Record) AgentResource {
	return AgentResource{
		AgentID: record.AgentID, SessionID: record.SessionID, Provider: record.Provider, Model: record.Model,
		Role: record.Role, CredentialRef: record.CredentialRef, Permission: record.PermissionMode, TimeoutMS: record.TimeoutMS,
		Budget: record.Budget, State: record.State, CreatedAt: record.CreatedAt, StartedAt: record.StartedAt,
		FinishedAt: record.FinishedAt, FailureReason: record.FailureReason, RuntimeScope: record.RuntimeScope,
		OutputCursor: record.OutputCursor, Usage: record.Usage,
	}
}

type OutputResult struct {
	Text       string `json:"text"`
	NextCursor int64  `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
	Complete   bool   `json:"complete"`
}

type Result struct {
	AgentID       string     `json:"agent_id"`
	State         AgentState `json:"state"`
	Summary       string     `json:"summary,omitempty"`
	ResolvedModel string     `json:"resolved_model,omitempty"`
	StopReason    string     `json:"stop_reason,omitempty"`
}

type ListFilter struct {
	SessionID string
	State     AgentState
	Limit     int
}
