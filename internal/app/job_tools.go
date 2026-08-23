package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	jobruntime "github.com/uvwt/agentdock/internal/job"
	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

func (r *Runtime) jobStart(_ context.Context, args map[string]any) (Result, error) {
	if r == nil || r.jobs == nil {
		err := toolError("JOB_RUNTIME_UNAVAILABLE", "OpenCode persistent job runtime is unavailable", "job")
		err.Retryable = true
		return nil, err
	}
	workspace := strings.TrimSpace(toolcore.StringArg(args, "workspace", ""))
	tool := strings.TrimSpace(toolcore.StringArg(args, "tool", ""))
	if workspace == "" || tool == "" {
		return nil, toolError("JOB_START_INVALID", "workspace and tool are required", "validation")
	}
	if !isVerifiedOpenCodeTool(tool) {
		return nil, toolErrorDetails("JOB_TOOL_NOT_ALLOWED", "tool is not part of the verified OpenCode runtime surface", "validation", map[string]any{"tool": tool})
	}
	arguments, ok := args["arguments"].(map[string]any)
	if !ok || arguments == nil {
		return nil, toolError("JOB_START_INVALID", "arguments must be an object", "validation")
	}
	permissionMode := strings.ToLower(strings.TrimSpace(toolcore.StringArg(args, "permission_mode", "project")))
	if permissionMode != "project" && permissionMode != "full" {
		return nil, toolError("JOB_START_INVALID", "permission_mode must be project or full", "validation")
	}
	var timeout time.Duration
	if _, exists := args["timeout"]; exists {
		milliseconds := toolcore.IntArg(args, "timeout", -1)
		if milliseconds < 0 {
			return nil, toolError("JOB_START_INVALID", "timeout must be a non-negative integer number of milliseconds", "validation")
		}
		timeout = time.Duration(milliseconds) * time.Millisecond
	}
	var metadata map[string]any
	if value, exists := args["metadata"]; exists && value != nil {
		var metadataOK bool
		metadata, metadataOK = value.(map[string]any)
		if !metadataOK {
			return nil, toolError("JOB_START_INVALID", "metadata must be an object", "validation")
		}
	}
	class := classifyOpenCodeJob(tool)
	if raw := strings.TrimSpace(toolcore.StringArg(args, "concurrency_class", "")); raw != "" {
		class = jobruntime.ConcurrencyClass(strings.ToLower(raw))
	}
	started, err := r.jobs.Start(jobruntime.StartRequest{
		Workspace: workspace, Tool: tool, Arguments: arguments, PermissionMode: permissionMode,
		Timeout: timeout, Metadata: metadata, ConcurrencyClass: class,
	})
	if err != nil {
		if errors.Is(err, jobruntime.ErrRuntimeUnavailable) {
			result := toolErrorCause("JOB_RUNTIME_UNAVAILABLE", "OpenCode persistent job runtime is unavailable", "job", nil, err)
			result.Retryable = true
			return nil, result
		}
		return nil, toolErrorCause("JOB_START_FAILED", "failed to start persistent OpenCode job", "job", nil, err)
	}
	return jobStructResult(started)
}

func (r *Runtime) jobStatus(_ context.Context, args map[string]any) (Result, error) {
	if r == nil || r.jobs == nil {
		return nil, toolError("JOB_MANAGER_UNAVAILABLE", "persistent job manager is unavailable", "job")
	}
	jobID := strings.TrimSpace(toolcore.StringArg(args, "job_id", ""))
	if jobID == "" {
		return nil, toolError("JOB_ID_REQUIRED", "job_id is required", "validation")
	}
	status, err := r.jobs.Status(jobID)
	if err != nil {
		return nil, toolErrorCause("JOB_STATUS_FAILED", "failed to read job status", "job", map[string]any{"job_id": jobID}, err)
	}
	return jobStructResult(status)
}

func (r *Runtime) jobOutput(_ context.Context, args map[string]any) (Result, error) {
	if r == nil || r.jobs == nil {
		return nil, toolError("JOB_MANAGER_UNAVAILABLE", "persistent job manager is unavailable", "job")
	}
	jobID := strings.TrimSpace(toolcore.StringArg(args, "job_id", ""))
	if jobID == "" {
		return nil, toolError("JOB_ID_REQUIRED", "job_id is required", "validation")
	}
	cursor := toolcore.IntArg(args, "cursor", 0)
	limit := toolcore.IntArg(args, "limit", 0)
	if cursor < 0 || limit < 0 {
		return nil, toolError("JOB_OUTPUT_INVALID", "cursor and limit must be non-negative integers", "validation")
	}
	output, err := r.jobs.Output(jobruntime.OutputRequest{JobID: jobID, Cursor: int64(cursor), LimitBytes: limit})
	if err != nil {
		return nil, toolErrorCause("JOB_OUTPUT_FAILED", "failed to read job output", "job", map[string]any{"job_id": jobID}, err)
	}
	return jobStructResult(output)
}

func (r *Runtime) jobCancel(ctx context.Context, args map[string]any) (Result, error) {
	if r == nil || r.jobs == nil {
		return nil, toolError("JOB_MANAGER_UNAVAILABLE", "persistent job manager is unavailable", "job")
	}
	jobID := strings.TrimSpace(toolcore.StringArg(args, "job_id", ""))
	if jobID == "" {
		return nil, toolError("JOB_ID_REQUIRED", "job_id is required", "validation")
	}
	reason := strings.TrimSpace(toolcore.StringArg(args, "reason", "user requested cancellation"))
	status, err := r.jobs.Cancel(ctx, jobID, reason)
	if err != nil {
		return nil, toolErrorCause("JOB_CANCEL_FAILED", "failed to cancel persistent job", "job", map[string]any{"job_id": jobID}, err)
	}
	return jobStructResult(status)
}

func (r *Runtime) jobList(_ context.Context, args map[string]any) (Result, error) {
	if r == nil || r.jobs == nil {
		return nil, toolError("JOB_MANAGER_UNAVAILABLE", "persistent job manager is unavailable", "job")
	}
	var state jobruntime.State
	if raw := strings.ToUpper(strings.TrimSpace(toolcore.StringArg(args, "state", ""))); raw != "" {
		state = jobruntime.State(raw)
		if !validJobState(state) {
			return nil, toolErrorDetails("JOB_LIST_INVALID", "invalid job state filter", "validation", map[string]any{"state": raw})
		}
	}
	limit := toolcore.IntArg(args, "limit", 50)
	if limit <= 0 {
		return nil, toolError("JOB_LIST_INVALID", "limit must be a positive integer", "validation")
	}
	items := r.jobs.List(jobruntime.ListFilter{State: state, Workspace: strings.TrimSpace(toolcore.StringArg(args, "workspace", "")), Limit: limit})
	return Result{"jobs": items, "count": len(items)}, nil
}

func isVerifiedOpenCodeTool(tool string) bool {
	for _, allowed := range opencoderuntime.RequiredTools {
		if tool == allowed {
			return true
		}
	}
	return false
}

func classifyOpenCodeJob(tool string) jobruntime.ConcurrencyClass {
	switch tool {
	case "read", "glob", "grep", "lsp":
		return jobruntime.ConcurrencyRead
	case "bash", "git", "apply_patch":
		return jobruntime.ConcurrencyDestructive
	default:
		return jobruntime.ConcurrencyWrite
	}
}

func validJobState(state jobruntime.State) bool {
	switch state {
	case jobruntime.StateQueued, jobruntime.StateStarting, jobruntime.StateRunning, jobruntime.StateSucceeded,
		jobruntime.StateFailed, jobruntime.StateCancelRequested, jobruntime.StateCancelled, jobruntime.StateTimedOut, jobruntime.StateInterrupted:
		return true
	default:
		return false
	}
}

func jobStructResult(value any) (Result, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode job result: %w", err)
	}
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode job result: %w", err)
	}
	return result, nil
}
