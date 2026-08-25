package opencodeworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/opencode"
)

const defaultMaxSteps = 32

type Completion string

const (
	CompletionSucceeded Completion = "SUCCEEDED"
	CompletionFailed    Completion = "FAILED"
	CompletionCancelled Completion = "CANCELLED"
	CompletionTimedOut  Completion = "TIMED_OUT"
)

type Scope struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
}

type Step struct {
	ID        string         `json:"id,omitempty"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type Request struct {
	Scope          Scope                   `json:"scope"`
	PermissionMode opencode.PermissionMode `json:"permission_mode"`
	Instruction    string                  `json:"instruction"`
	Timeout        time.Duration           `json:"-"`
	Steps          []Step                  `json:"steps"`
}

type StepResult struct {
	ID          string           `json:"id,omitempty"`
	Tool        string           `json:"tool"`
	Output      map[string]any   `json:"output,omitempty"`
	Runtime     opencode.Summary `json:"runtime"`
	StartedAt   time.Time        `json:"started_at"`
	CompletedAt time.Time        `json:"completed_at"`
}

type EventKind string

const (
	EventTaskStarted   EventKind = "task_started"
	EventStepStarted   EventKind = "step_started"
	EventStepCompleted EventKind = "step_completed"
	EventTaskCompleted EventKind = "task_completed"
	EventTaskFailed    EventKind = "task_failed"
)

type Event struct {
	Kind      EventKind      `json:"kind"`
	ScopeID   string         `json:"scope_id"`
	StepIndex int            `json:"step_index,omitempty"`
	StepID    string         `json:"step_id,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Message   string         `json:"message,omitempty"`
	Output    map[string]any `json:"output,omitempty"`
	At        time.Time      `json:"at"`
}

type Result struct {
	ScopeID        string                  `json:"scope_id"`
	Workspace      string                  `json:"workspace"`
	PermissionMode opencode.PermissionMode `json:"permission_mode"`
	Instruction    string                  `json:"instruction"`
	Completion     Completion              `json:"completion"`
	Steps          []StepResult            `json:"steps"`
	Events         []Event                 `json:"events"`
	StartedAt      time.Time               `json:"started_at"`
	CompletedAt    time.Time               `json:"completed_at"`
}

type EventSink func(Event)

type Executor interface {
	Execute(context.Context, string, opencode.PermissionMode, string, map[string]any) (opencode.ExecutionResult, error)
}

var _ Executor = (*opencode.Supervisor)(nil)

type Options struct {
	MaxSteps int
}

type Engine struct {
	executor Executor
	maxSteps int
}

func New(executor Executor, options Options) (*Engine, error) {
	if executor == nil {
		return nil, newError("OPENCODE_WORKER_EXECUTOR_REQUIRED", "OpenCode worker executor is required", nil)
	}
	if options.MaxSteps <= 0 {
		options.MaxSteps = defaultMaxSteps
	}
	return &Engine{executor: executor, maxSteps: options.MaxSteps}, nil
}

func (e *Engine) Execute(ctx context.Context, request Request, sink EventSink) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	canonical, err := e.validate(request)
	if err != nil {
		return Result{}, err
	}
	request.Scope.Workspace = canonical

	runCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	result := Result{
		ScopeID: request.Scope.ID, Workspace: canonical, PermissionMode: request.PermissionMode,
		Instruction: strings.TrimSpace(request.Instruction), Completion: CompletionFailed,
		Steps: make([]StepResult, 0, len(request.Steps)), Events: make([]Event, 0, 2+2*len(request.Steps)),
		StartedAt: time.Now().UTC(),
	}
	emit := func(event Event) {
		event.ScopeID = request.Scope.ID
		event.At = time.Now().UTC()
		result.Events = append(result.Events, event)
		if sink != nil {
			sink(event)
		}
	}
	emit(Event{Kind: EventTaskStarted, Message: result.Instruction})

	for index, step := range request.Steps {
		if err := runCtx.Err(); err != nil {
			return e.finishWithContextError(&result, emit, err)
		}
		started := time.Now().UTC()
		emit(Event{Kind: EventStepStarted, StepIndex: index, StepID: step.ID, Tool: step.Tool})
		execution, execErr := e.executor.Execute(runCtx, canonical, request.PermissionMode, step.Tool, step.Arguments)
		completed := time.Now().UTC()
		stepResult := StepResult{ID: step.ID, Tool: step.Tool, Output: execution.Result, Runtime: execution.Runtime, StartedAt: started, CompletedAt: completed}
		result.Steps = append(result.Steps, stepResult)

		if execErr != nil {
			if runCtx.Err() != nil || isContextExecutionError(execErr) {
				return e.finishWithContextError(&result, emit, firstContextError(runCtx.Err(), execErr))
			}
			emit(Event{Kind: EventTaskFailed, StepIndex: index, StepID: step.ID, Tool: step.Tool, Message: execErr.Error(), Output: execution.Result})
			result.Completion = CompletionFailed
			result.CompletedAt = time.Now().UTC()
			return result, newError("OPENCODE_WORKER_RUNTIME_FAILED", fmt.Sprintf("OpenCode worker step %q failed", step.Tool), execErr)
		}
		if toolFailed(execution.Result) {
			emit(Event{Kind: EventTaskFailed, StepIndex: index, StepID: step.ID, Tool: step.Tool, Message: remoteMessage(execution.Result), Output: execution.Result})
			result.Completion = CompletionFailed
			result.CompletedAt = time.Now().UTC()
			return result, newError("OPENCODE_WORKER_TOOL_FAILED", fmt.Sprintf("OpenCode worker tool %q reported failure", step.Tool), nil)
		}
		emit(Event{Kind: EventStepCompleted, StepIndex: index, StepID: step.ID, Tool: step.Tool, Output: execution.Result})
	}

	result.Completion = CompletionSucceeded
	result.CompletedAt = time.Now().UTC()
	emit(Event{Kind: EventTaskCompleted, Message: "OpenCode worker task completed"})
	return result, nil
}

func (e *Engine) validate(request Request) (string, error) {
	if strings.TrimSpace(request.Scope.ID) == "" {
		return "", newError("OPENCODE_WORKER_SCOPE_REQUIRED", "OpenCode worker scope id is required", nil)
	}
	if strings.TrimSpace(request.Instruction) == "" {
		return "", newError("OPENCODE_WORKER_INSTRUCTION_REQUIRED", "OpenCode worker instruction is required", nil)
	}
	if request.PermissionMode != opencode.PermissionProject && request.PermissionMode != opencode.PermissionFull {
		return "", newError("OPENCODE_WORKER_PERMISSION_INVALID", "permission mode must be project or full", nil)
	}
	if len(request.Steps) == 0 {
		return "", newError("OPENCODE_WORKER_STEPS_REQUIRED", "OpenCode worker requires at least one bounded tool step", nil)
	}
	if len(request.Steps) > e.maxSteps {
		return "", newError("OPENCODE_WORKER_STEP_LIMIT", fmt.Sprintf("OpenCode worker step count exceeds limit %d", e.maxSteps), nil)
	}
	for index, step := range request.Steps {
		if strings.TrimSpace(step.Tool) == "" {
			return "", newError("OPENCODE_WORKER_STEP_INVALID", fmt.Sprintf("OpenCode worker step %d has no tool", index), nil)
		}
	}
	return canonicalWorkspace(request.Scope.Workspace)
}

func canonicalWorkspace(raw string) (string, error) {
	workspace := strings.TrimSpace(raw)
	if workspace == "" {
		return "", newError("OPENCODE_WORKER_WORKSPACE_REQUIRED", "OpenCode worker workspace is required", nil)
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", newError("OPENCODE_WORKER_WORKSPACE_INVALID", "resolve OpenCode worker workspace", err)
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", newError("OPENCODE_WORKER_WORKSPACE_INVALID", "resolve OpenCode worker workspace symlinks", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", newError("OPENCODE_WORKER_WORKSPACE_INVALID", "OpenCode worker workspace must be an existing directory", err)
	}
	if filepath.Dir(real) == real {
		return "", newError("OPENCODE_WORKER_WORKSPACE_INVALID", "refusing filesystem root as OpenCode worker workspace", nil)
	}
	return filepath.Clean(real), nil
}

func (e *Engine) finishWithContextError(result *Result, emit func(Event), err error) (Result, error) {
	code := "OPENCODE_WORKER_CANCELLED"
	completion := CompletionCancelled
	message := "OpenCode worker task cancelled"
	if errors.Is(err, context.DeadlineExceeded) {
		code = "OPENCODE_WORKER_TIMEOUT"
		completion = CompletionTimedOut
		message = "OpenCode worker task timed out"
	}
	result.Completion = completion
	result.CompletedAt = time.Now().UTC()
	emit(Event{Kind: EventTaskFailed, Message: message})
	return *result, newError(code, message, err)
}

func firstContextError(ctxErr, execErr error) error {
	if ctxErr != nil {
		return ctxErr
	}
	if errors.Is(execErr, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

func isContextExecutionError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var runtimeErr *opencode.Error
	if errors.As(err, &runtimeErr) {
		return runtimeErr.Code == "OPENCODE_ABORTED" || runtimeErr.Code == "OPENCODE_TIMEOUT"
	}
	return false
}

func toolFailed(output map[string]any) bool {
	failed, _ := output["isError"].(bool)
	return failed
}

func remoteMessage(output map[string]any) string {
	content, _ := output["content"].([]any)
	parts := make([]string, 0, len(content))
	for _, item := range content {
		entry, _ := item.(map[string]any)
		if text, _ := entry["text"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	if len(parts) == 0 {
		return "OpenCode tool reported failure"
	}
	return strings.Join(parts, "\n")
}

type Error struct {
	Code  string
	Cause error
	msg   string
}

func (e *Error) Error() string { return e.msg }
func (e *Error) Unwrap() error { return e.Cause }

func newError(code, message string, cause error) *Error {
	return &Error{Code: code, msg: message, Cause: cause}
}

func ErrorCode(err error) string {
	var workerErr *Error
	if errors.As(err, &workerErr) {
		return workerErr.Code
	}
	return ""
}
