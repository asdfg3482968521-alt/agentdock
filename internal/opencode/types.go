package opencode

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// State is the lifecycle state of one workspace-bound OpenCode runtime.
type State string

const (
	StateStopped    State = "STOPPED"
	StateStarting   State = "STARTING"
	StateReady      State = "READY"
	StateBusy       State = "BUSY"
	StateStopping   State = "STOPPING"
	StateCrashed    State = "CRASHED"
	StateRestarting State = "RESTARTING"
)

type PermissionMode string

const (
	PermissionProject PermissionMode = "project"
	PermissionFull    PermissionMode = "full"
)

func ParsePermissionMode(value string) (PermissionMode, error) {
	mode := PermissionMode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		mode = PermissionProject
	}
	switch mode {
	case PermissionProject, PermissionFull:
		return mode, nil
	default:
		return "", fmt.Errorf("permission mode must be project or full")
	}
}

var RequiredTools = []string{
	"bash",
	"read",
	"glob",
	"grep",
	"edit",
	"write",
	"apply_patch",
	"lsp",
	"git",
}

type RuntimeLocation struct {
	RuntimeVersion  string
	VerifiedSHA     string
	Executable      string
	Args            []string
	Cwd             string
	SourceRoot      string
	ProtocolVersion string
}

type Tool struct {
	Name string
}

type ProcessMetrics struct {
	Spawn      time.Duration `json:"-"`
	Initialize time.Duration `json:"-"`
	ToolsList  time.Duration `json:"-"`
}

type RuntimeConnection interface {
	PID() int
	ProtocolVersion() string
	Metrics() ProcessMetrics
	ListTools(context.Context) ([]Tool, error)
	CallTool(context.Context, string, map[string]any) (map[string]any, error)
	Close(time.Duration) error
	Done() <-chan struct{}
	ExitError() error
	StderrTail() string
}

type RuntimeLocator interface {
	Locate(context.Context) (RuntimeLocation, error)
}

type RuntimeLauncher interface {
	Launch(context.Context, RuntimeLocation, string, PermissionMode, string) (RuntimeConnection, error)
}

type Summary struct {
	Workspace       string `json:"workspace"`
	PermissionMode  string `json:"permission_mode"`
	State           State  `json:"state"`
	RuntimePath     string `json:"runtime_path"`
	RuntimeVersion  string `json:"runtime_version"`
	RuntimeSHA      string `json:"runtime_sha"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	PID             int    `json:"pid,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	Health          string `json:"health"`
	RestartCount    int    `json:"restart_count"`
	ActiveCalls     int    `json:"active_calls"`
	LastError       string `json:"last_error,omitempty"`
	StderrTail      string `json:"stderr_tail,omitempty"`
	SpawnMS         int64  `json:"spawn_ms,omitempty"`
	InitializeMS    int64  `json:"initialize_ms,omitempty"`
	ToolsListMS     int64  `json:"tools_list_ms,omitempty"`
}

type ExecutionResult struct {
	Tool           string         `json:"tool"`
	Workspace      string         `json:"workspace"`
	PermissionMode string         `json:"permission_mode"`
	Runtime        Summary        `json:"runtime"`
	Result         map[string]any `json:"result"`
}

type Error struct {
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
	Cause     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func newError(code, message string, retryable bool, details map[string]any, cause error) *Error {
	if details == nil {
		details = map[string]any{}
	}
	return &Error{Code: code, Message: message, Retryable: retryable, Details: details, Cause: cause}
}
