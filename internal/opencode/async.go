package opencode

import (
	"context"
	"strings"
)

type AsyncExecutionOutcome struct {
	Result ExecutionResult
	Err    error
}

type AsyncExecutionHandle struct {
	RequestID  string
	Completion <-chan AsyncExecutionOutcome
}

type activeExecution struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// StartExecution registers a request with the Supervisor and returns before the
// workspace runtime is acquired or the tool completes. The request is detached
// from the caller's MCP context so a web/client disconnect does not cancel it.
func (s *Supervisor) StartExecution(requestID, workspace string, mode PermissionMode, tool string, arguments map[string]any) (AsyncExecutionHandle, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return AsyncExecutionHandle{}, newError("OPENCODE_REQUEST_ID_REQUIRED", "OpenCode request id is required", false, nil, nil)
	}
	if !requiredTool(tool) {
		return AsyncExecutionHandle{}, newError("OPENCODE_TOOL_NOT_ALLOWED", "tool is not part of the verified OpenCode runtime surface", false, map[string]any{"tool": tool}, nil)
	}
	if mode != PermissionProject && mode != PermissionFull {
		return AsyncExecutionHandle{}, newError("OPENCODE_PERMISSION_INVALID", "permission mode must be project or full", false, map[string]any{"permission_mode": mode}, nil)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	active := &activeExecution{cancel: cancel, done: make(chan struct{})}
	completion := make(chan AsyncExecutionOutcome, 1)

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		cancel()
		return AsyncExecutionHandle{}, newError("OPENCODE_SUPERVISOR_CLOSING", "OpenCode supervisor is shutting down", true, nil, nil)
	}
	if _, exists := s.requests[requestID]; exists {
		s.mu.Unlock()
		cancel()
		return AsyncExecutionHandle{}, newError("OPENCODE_REQUEST_DUPLICATE", "OpenCode request id is already active", false, map[string]any{"request_id": requestID}, nil)
	}
	s.requests[requestID] = active
	s.mu.Unlock()

	args := cloneArguments(arguments)
	go func() {
		result, err := s.Execute(runCtx, workspace, mode, tool, args)
		completion <- AsyncExecutionOutcome{Result: result, Err: err}
		close(completion)
		close(active.done)
		cancel()
		s.mu.Lock()
		if s.requests[requestID] == active {
			delete(s.requests, requestID)
		}
		s.mu.Unlock()
	}()
	return AsyncExecutionHandle{RequestID: requestID, Completion: completion}, nil
}

// CancelExecution propagates cancellation into the active OpenCode tool call
// and does not return until that request has stopped. Windows descendant-tree
// termination remains the responsibility of the frozen OpenCode runtime and
// its Job Object implementation; AgentDock never taskkills the tree itself.
func (s *Supervisor) CancelExecution(ctx context.Context, requestID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return newError("OPENCODE_REQUEST_ID_REQUIRED", "OpenCode request id is required", false, nil, nil)
	}
	s.mu.RLock()
	active := s.requests[requestID]
	s.mu.RUnlock()
	if active == nil {
		return nil
	}
	active.cancel()
	select {
	case <-active.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cloneArguments(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneArgumentValue(value)
	}
	return out
}

func cloneArgumentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneArguments(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneArgumentValue(item)
		}
		return out
	default:
		return typed
	}
}
