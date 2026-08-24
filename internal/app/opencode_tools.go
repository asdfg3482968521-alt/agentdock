package app

import (
	"context"
	"errors"
	"strings"

	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

func (r *Runtime) openCodeSummaries() []opencoderuntime.Summary {
	if r == nil || r.openCode == nil {
		return []opencoderuntime.Summary{}
	}
	return r.openCode.Summaries()
}

func (r *Runtime) openCodeTool(ctx context.Context, remoteTool string, args map[string]any) (Result, error) {
	if r.openCode == nil {
		return nil, toolError("OPENCODE_DISABLED", "OpenCode runtime integration is disabled", "opencode")
	}
	sessionID := strings.TrimSpace(toolcore.StringArg(args, "session_id", ""))
	workspace := strings.TrimSpace(toolcore.StringArg(args, "workspace", ""))
	permissionMode := toolcore.StringArg(args, "permission_mode", "project")
	if sessionID != "" {
		record, err := r.resolveD1SessionRoute(sessionID, workspace, openCodeToolMutates(remoteTool))
		if err != nil {
			return nil, err
		}
		workspace = record.Workspace()
		permissionMode = record.PermissionMode
		if _, err := r.sessions.SetRuntime(sessionID, d1RuntimeScope(sessionID)); err != nil {
			return nil, sessionToolError(err)
		}
	} else if workspace == "" {
		return nil, toolError("OPENCODE_WORKSPACE_REQUIRED", "workspace or session_id is required", "opencode")
	}
	mode, err := opencoderuntime.ParsePermissionMode(permissionMode)
	if err != nil {
		return nil, toolErrorDetails("OPENCODE_PERMISSION_INVALID", err.Error(), "opencode", map[string]any{"permission_mode": permissionMode})
	}
	remoteArguments, ok := args["arguments"].(map[string]any)
	if !ok || remoteArguments == nil {
		return nil, toolError("OPENCODE_ARGUMENTS_INVALID", "arguments must be an object", "opencode")
	}
	execution, err := r.openCode.Execute(ctx, workspace, mode, remoteTool, remoteArguments)
	if err != nil {
		return nil, openCodeToolError(err)
	}
	result := Result{
		"tool":            execution.Tool,
		"workspace":       execution.Workspace,
		"permission_mode": execution.PermissionMode,
		"runtime":         execution.Runtime,
		"result":          execution.Result,
	}
	if sessionID != "" {
		result["session_id"] = sessionID
		result["runtime_scope"] = d1RuntimeScope(sessionID)
	}
	return result, nil
}

func openCodeToolError(err error) error {
	var runtimeErr *opencoderuntime.Error
	if !errors.As(err, &runtimeErr) {
		return toolErrorCause("OPENCODE_RUNTIME_FAILED", "OpenCode runtime request failed", "opencode", nil, err)
	}
	result := toolErrorCause(runtimeErr.Code, runtimeErr.Message, "opencode", runtimeErr.Details, runtimeErr.Cause)
	result.Retryable = runtimeErr.Retryable
	return result
}
