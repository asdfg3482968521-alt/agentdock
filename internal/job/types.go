package job

import (
	"context"
	"time"
)

type State string

const (
	StateQueued          State = "QUEUED"
	StateStarting        State = "STARTING"
	StateRunning         State = "RUNNING"
	StateSucceeded       State = "SUCCEEDED"
	StateFailed          State = "FAILED"
	StateCancelRequested State = "CANCEL_REQUESTED"
	StateCancelled       State = "CANCELLED"
	StateTimedOut        State = "TIMED_OUT"
	StateInterrupted     State = "INTERRUPTED"
)

func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateTimedOut, StateInterrupted:
		return true
	default:
		return false
	}
}

type ConcurrencyClass string

const (
	ConcurrencyRead        ConcurrencyClass = "read"
	ConcurrencyWrite       ConcurrencyClass = "write"
	ConcurrencyDestructive ConcurrencyClass = "destructive"
)

type Limits struct {
	MaxConcurrentJobs              int
	MaxConcurrentPerWorkspace      int
	MaxConcurrentWritePerWorkspace int
	MemoryOutputBytesPerJob        int64
	MaxOutputBytesPerResponse      int
	MaxDiskOutputBytesPerJob       int64
	Retention                      time.Duration
	RetentionMaxCompletedJobs      int
	MaxArgumentSummaryBytes        int
	MaxMetadataSummaryBytes        int
	MaxResultSummaryBytes          int
	MaxTimeout                     time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxConcurrentJobs:              4,
		MaxConcurrentPerWorkspace:      2,
		MaxConcurrentWritePerWorkspace: 1,
		MemoryOutputBytesPerJob:        1 << 20,
		MaxOutputBytesPerResponse:      256 << 10,
		MaxDiskOutputBytesPerJob:       64 << 20,
		Retention:                      7 * 24 * time.Hour,
		RetentionMaxCompletedJobs:      200,
		MaxArgumentSummaryBytes:        16 << 10,
		MaxMetadataSummaryBytes:        8 << 10,
		MaxResultSummaryBytes:          16 << 10,
		MaxTimeout:                     24 * time.Hour,
	}
}

type Config struct {
	StorageRoot string
	Limits      Limits
	RedactText  func(string) string
	Now         func() time.Time
	IDFactory   func() string
}

type StartRequest struct {
	Workspace        string
	Tool             string
	Arguments        map[string]any
	PermissionMode   string
	Timeout          time.Duration
	Metadata         map[string]any
	ConcurrencyClass ConcurrencyClass
}

type StartResult struct {
	JobID     string    `json:"job_id"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

type Record struct {
	JobID            string           `json:"job_id"`
	Workspace        string           `json:"workspace"`
	Tool             string           `json:"tool"`
	PermissionMode   string           `json:"permission_mode"`
	ArgumentsSummary string           `json:"arguments_summary"`
	MetadataSummary  string           `json:"metadata_summary,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	StartedAt        *time.Time       `json:"started_at,omitempty"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
	State            State            `json:"state"`
	ExitCode         *int             `json:"exit_code,omitempty"`
	ResultSummary    string           `json:"result_summary,omitempty"`
	Error            string           `json:"error,omitempty"`
	RuntimeID        string           `json:"runtime_id,omitempty"`
	OutputCursor     int64            `json:"output_cursor"`
	OutputBytes      int64            `json:"output_bytes"`
	CancelReason     string           `json:"cancel_reason,omitempty"`
	TimeoutMillis    int64            `json:"timeout_ms,omitempty"`
	ConcurrencyClass ConcurrencyClass `json:"concurrency_class"`
}

type StatusResult struct {
	JobID            string         `json:"job_id"`
	Workspace        string         `json:"workspace"`
	Tool             string         `json:"tool"`
	PermissionMode   string         `json:"permission_mode"`
	State            State          `json:"state"`
	CreatedAt        time.Time      `json:"created_at"`
	StartedAt        *time.Time     `json:"started_at,omitempty"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	DurationMS       int64          `json:"duration_ms"`
	RuntimeID        string         `json:"runtime_id,omitempty"`
	OutputCursor     int64          `json:"output_cursor"`
	OutputBytes      int64          `json:"output_bytes"`
	ExitCode         *int           `json:"exit_code,omitempty"`
	ResultSummary    string         `json:"result_summary,omitempty"`
	Error            string         `json:"error,omitempty"`
	CancelReason     string         `json:"cancel_reason,omitempty"`
	ProgressMetadata map[string]any `json:"progress_metadata,omitempty"`
}

type ListFilter struct {
	State     State
	Workspace string
	Limit     int
}

type OutputChunk struct {
	Cursor    int64  `json:"cursor"`
	Stream    string `json:"stream"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	Bytes     int    `json:"bytes,omitempty"`
}

type OutputRequest struct {
	JobID      string
	Cursor     int64
	LimitBytes int
}

type OutputResult struct {
	Chunks     []OutputChunk `json:"chunks"`
	Text       string        `json:"text"`
	NextCursor int64         `json:"next_cursor"`
	Truncated  bool          `json:"truncated"`
	Complete   bool          `json:"complete"`
}

type RuntimeOutputEvent struct {
	// After exists so deterministic test executors can schedule output without
	// inventing a second event type. Production adapters leave it zero.
	After     time.Duration
	Stream    string
	Text      string
	Timestamp time.Time
}

type RuntimeExecutionResult struct {
	OK       bool
	ExitCode int
	Result   any
	Summary  string
	Err      error
}

type RuntimeExecutionRequest struct {
	JobID          string
	Workspace      string
	Tool           string
	Arguments      map[string]any
	PermissionMode string
	OnOutput       func(RuntimeOutputEvent) error
	OnProgress     func(map[string]any)
}

type RuntimeExecutionHandle struct {
	RuntimeID  string
	Completion <-chan RuntimeExecutionResult
}

type RuntimeCancelRequest struct {
	JobID     string
	RuntimeID string
	Reason    string
}

type RuntimeExecutor interface {
	Execute(context.Context, RuntimeExecutionRequest) (RuntimeExecutionHandle, error)
	Cancel(context.Context, RuntimeCancelRequest) error
}

// AvailabilityChecker lets job_start reject a known-unavailable runtime before
// a job is persisted. Runtime launch failures after this point remain normal
// FAILED jobs with durable diagnostics.
type AvailabilityChecker interface {
	Available() error
}
