package job

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
)

var ErrRuntimeUnavailable = errors.New("OpenCode runtime supervisor is unavailable")

type openCodeSupervisor interface {
	StartExecution(string, string, opencoderuntime.PermissionMode, string, map[string]any) (opencoderuntime.AsyncExecutionHandle, error)
	CancelExecution(context.Context, string) error
}

type OpenCodeExecutor struct {
	supervisor openCodeSupervisor
}

func NewOpenCodeExecutor(supervisor *opencoderuntime.Supervisor) *OpenCodeExecutor {
	if supervisor == nil {
		return &OpenCodeExecutor{}
	}
	return &OpenCodeExecutor{supervisor: supervisor}
}

func newOpenCodeExecutor(supervisor openCodeSupervisor) *OpenCodeExecutor {
	return &OpenCodeExecutor{supervisor: supervisor}
}

func (e *OpenCodeExecutor) Available() error {
	if e == nil || e.supervisor == nil {
		return ErrRuntimeUnavailable
	}
	return nil
}

func (e *OpenCodeExecutor) Execute(_ context.Context, request RuntimeExecutionRequest) (RuntimeExecutionHandle, error) {
	if err := e.Available(); err != nil {
		return RuntimeExecutionHandle{}, err
	}
	mode, err := opencoderuntime.ParsePermissionMode(request.PermissionMode)
	if err != nil {
		return RuntimeExecutionHandle{}, err
	}
	handle, err := e.supervisor.StartExecution(request.JobID, request.Workspace, mode, request.Tool, request.Arguments)
	if err != nil {
		return RuntimeExecutionHandle{}, err
	}
	completion := make(chan RuntimeExecutionResult, 1)
	go func() {
		defer close(completion)
		outcome, ok := <-handle.Completion
		if !ok {
			completion <- RuntimeExecutionResult{Err: errors.New("OpenCode supervisor completion channel closed without a result")}
			return
		}
		if outcome.Err != nil {
			completion <- RuntimeExecutionResult{Err: outcome.Err}
			return
		}
		isError, _ := outcome.Result.Result["isError"].(bool)
		exitCode := 0
		if isError {
			exitCode = 1
		}
		completion <- RuntimeExecutionResult{
			OK:       !isError,
			ExitCode: exitCode,
			Result:   outcome.Result.Result,
			Summary:  openCodeResultSummary(outcome.Result.Result),
		}
	}()
	return RuntimeExecutionHandle{RuntimeID: handle.RequestID, Completion: completion}, nil
}

func (e *OpenCodeExecutor) Cancel(ctx context.Context, request RuntimeCancelRequest) error {
	if err := e.Available(); err != nil {
		return err
	}
	requestID := strings.TrimSpace(request.RuntimeID)
	if requestID == "" {
		requestID = strings.TrimSpace(request.JobID)
	}
	if requestID == "" {
		return errors.New("runtime request id is required")
	}
	return e.supervisor.CancelExecution(ctx, requestID)
}

func openCodeResultSummary(result map[string]any) string {
	if result == nil {
		return ""
	}
	var builder strings.Builder
	if content, ok := result["content"].([]any); ok {
		for _, item := range content {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, _ := entry["text"].(string)
			builder.WriteString(text)
		}
	}
	if builder.Len() > 0 {
		return builder.String()
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "[unserializable OpenCode result]"
	}
	return string(body)
}
