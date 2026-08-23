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
	workspace := strings.TrimSpace(toolcore.StringArg(args, "workspace", ""))
	if workspace == "" {
		return nil, toolError("OPENCODE_WORKSPACE_REQUIRED", "workspace is required", "opencode")
	}
	mode, err := opencoderuntime.ParsePermissionMode(toolcore.StringArg(args, "permission_mode", "project"))
	if err != nil {
		return nil, toolErrorDetails("OPENCODE_PERMISSION_INVALID", err.Error(), "opencode", map[string]any{"permission_mode": args["permission_mode"]})
	}
	remoteArguments, ok := args["arguments"].(map[string]any)
	if !ok || remoteArguments == nil {
		return nil, toolError("OPENCODE_ARGUMENTS_INVALID", "arguments must be an object", "opencode")
	}
	execution, err := r.openCode.Execute(ctx, workspace, mode, remoteTool, remoteArguments)
	if err != nil {
		return nil, openCodeToolError(err)
	}
	return Result{
		"tool":            execution.Tool,
		"workspace":       execution.Workspace,
		"permission_mode": execution.PermissionMode,
		"runtime":         execution.Runtime,
		"result":          execution.Result,
	}, nil
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
