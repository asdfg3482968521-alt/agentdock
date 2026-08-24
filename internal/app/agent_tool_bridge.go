package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	jobruntime "github.com/uvwt/agentdock/internal/job"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

const agentToolPollInterval = 5 * time.Millisecond

type appAgentToolBridge struct {
	runtime *Runtime
}

type appAgentSessionResolver struct {
	runtime *Runtime
}

func newAgentToolBridge(runtime *Runtime) *appAgentToolBridge {
	return &appAgentToolBridge{runtime: runtime}
}

func newAgentSessionResolver(runtime *Runtime) *appAgentSessionResolver {
	return &appAgentSessionResolver{runtime: runtime}
}

func (r *appAgentSessionResolver) ResolveAgentSession(ctx context.Context, sessionID string) (agentruntime.SessionBinding, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return agentruntime.SessionBinding{}, err
	}
	if r == nil || r.runtime == nil || r.runtime.sessions == nil {
		return agentruntime.SessionBinding{}, agentruntime.ErrAgentSessionClosed
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return agentruntime.SessionBinding{}, agentruntime.ErrInvalidArguments
	}
	record, err := r.runtime.sessions.Resolve(sessionID, "")
	if err != nil {
		return agentruntime.SessionBinding{}, agentSessionBridgeError(err)
	}
	permission := "project"
	if err := sessionruntime.AuthorizeMutation(record); err != nil {
		if sessionruntime.ErrorCode(err) != sessionruntime.CodeWriteDenied {
			return agentruntime.SessionBinding{}, agentSessionBridgeError(err)
		}
		permission = "read-only"
	}
	return agentruntime.SessionBinding{
		SessionID: record.SessionID, Workspace: record.Workspace(), RuntimeScope: d1RuntimeScope(record.SessionID), PermissionMode: permission,
	}, nil
}

func (b *appAgentToolBridge) Tools(permission string) []agentruntime.ProviderTool {
	permission = strings.ToLower(strings.TrimSpace(permission))
	names := []string{"opencode_read", "opencode_glob", "opencode_grep", "opencode_lsp", "opencode_git"}
	if permission == "project" {
		names = []string{"opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit", "opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git"}
	}
	tools := make([]agentruntime.ProviderTool, 0, len(names))
	for _, name := range names {
		schema := map[string]any{"type": "object", "additionalProperties": true}
		if name == "opencode_git" {
			schema["properties"] = map[string]any{"operation": map[string]any{"type": "string", "minLength": 1}}
			schema["required"] = []string{"operation"}
		}
		encoded, _ := json.Marshal(schema)
		tools = append(tools, agentruntime.ProviderTool{Name: name, Description: "Verified OpenCode runtime tool bound to the Agent D1 session.", InputSchema: encoded})
	}
	return tools
}

func (b *appAgentToolBridge) Execute(ctx context.Context, request agentruntime.ToolExecutionRequest) (agentruntime.ToolExecutionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return agentruntime.ToolExecutionResult{}, err
	}
	if b == nil || b.runtime == nil || b.runtime.sessions == nil || b.runtime.jobs == nil {
		return agentruntime.ToolExecutionResult{}, agentruntime.ErrToolError
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.Workspace = strings.TrimSpace(request.Workspace)
	request.RuntimeScope = strings.TrimSpace(request.RuntimeScope)
	request.PermissionMode = strings.ToLower(strings.TrimSpace(request.PermissionMode))
	request.Tool = strings.TrimSpace(request.Tool)
	if request.AgentID == "" || request.SessionID == "" || request.Workspace == "" || request.RuntimeScope == "" || (request.PermissionMode != "read-only" && request.PermissionMode != "project") {
		return agentruntime.ToolExecutionResult{}, agentruntime.ErrInvalidArguments
	}
	remoteTool, ok := agentOpenCodeToolName(request.Tool)
	if !ok {
		return agentruntime.ToolExecutionResult{}, agentruntime.ErrToolError
	}
	if err := validateAgentToolArguments(remoteTool, request.Arguments); err != nil {
		return agentruntime.ToolExecutionResult{}, err
	}

	fresh, err := b.runtime.sessions.Resolve(request.SessionID, "")
	if err != nil {
		return agentruntime.ToolExecutionResult{}, agentSessionBridgeError(err)
	}
	if fresh.SessionID != request.SessionID || strings.TrimSpace(fresh.Workspace()) != request.Workspace || request.RuntimeScope != d1RuntimeScope(request.SessionID) {
		return agentruntime.ToolExecutionResult{}, agentruntime.ErrAgentSessionOwnershipMismatch
	}

	sessionWritable := sessionruntime.AuthorizeMutation(fresh) == nil
	if request.PermissionMode == "project" && !sessionWritable {
		return agentruntime.ToolExecutionResult{}, agentruntime.ErrAgentWriteDenied
	}
	mutating := agentToolMutates(remoteTool, request.Arguments)
	if request.PermissionMode == "read-only" && mutating {
		return agentruntime.ToolExecutionResult{}, agentruntime.ErrAgentWriteDenied
	}
	if mutating {
		if err := sessionruntime.AuthorizeMutation(fresh); err != nil {
			if sessionruntime.ErrorCode(err) == sessionruntime.CodeWriteDenied {
				return agentruntime.ToolExecutionResult{}, agentruntime.ErrAgentWriteDenied
			}
			return agentruntime.ToolExecutionResult{}, agentSessionBridgeError(err)
		}
	}

	started, err := b.runtime.jobs.Start(jobruntime.StartRequest{
		Workspace: fresh.Workspace(), Tool: remoteTool, Arguments: cloneAgentArguments(request.Arguments), PermissionMode: fresh.PermissionMode,
		SessionID: fresh.SessionID, AgentID: request.AgentID, RuntimeScope: d1RuntimeScope(fresh.SessionID), ConcurrencyClass: classifyOpenCodeJob(remoteTool),
		Metadata: map[string]any{"agent_id": request.AgentID}, KnownSecrets: request.KnownSecrets,
	})
	if err != nil {
		return agentruntime.ToolExecutionResult{}, fmt.Errorf("%w: start Agent-owned job", agentruntime.ErrToolError)
	}
	if _, err := b.runtime.sessions.AttachJob(fresh.SessionID, started.JobID); err != nil {
		_, _ = b.runtime.jobs.Cancel(context.Background(), started.JobID, "agent session ownership persistence failed")
		return agentruntime.ToolExecutionResult{}, agentSessionBridgeError(err)
	}

	status, err := b.waitAgentJob(ctx, started.JobID)
	if err != nil {
		return agentruntime.ToolExecutionResult{JobID: started.JobID}, err
	}
	output, outputErr := b.runtime.jobs.Output(jobruntime.OutputRequest{JobID: started.JobID, Cursor: 0, LimitBytes: 256 << 10})
	if outputErr != nil {
		return agentruntime.ToolExecutionResult{JobID: started.JobID}, fmt.Errorf("%w: read Agent-owned job output", agentruntime.ErrToolError)
	}
	if status.State != jobruntime.StateSucceeded {
		if status.State == jobruntime.StateCancelled {
			return agentruntime.ToolExecutionResult{JobID: started.JobID, Text: output.Text}, context.Canceled
		}
		if status.State == jobruntime.StateTimedOut {
			return agentruntime.ToolExecutionResult{JobID: started.JobID, Text: output.Text}, context.DeadlineExceeded
		}
		return agentruntime.ToolExecutionResult{JobID: started.JobID, Text: output.Text}, fmt.Errorf("%w: Agent-owned job ended in %s", agentruntime.ErrToolError, status.State)
	}
	text := output.Text
	if strings.TrimSpace(text) == "" {
		text = status.ResultSummary
	}
	return agentruntime.ToolExecutionResult{JobID: started.JobID, Text: text}, nil
}

func (b *appAgentToolBridge) waitAgentJob(ctx context.Context, jobID string) (jobruntime.StatusResult, error) {
	ticker := time.NewTicker(agentToolPollInterval)
	defer ticker.Stop()
	for {
		status, err := b.runtime.jobs.Status(jobID)
		if err != nil {
			return jobruntime.StatusResult{}, fmt.Errorf("%w: read Agent-owned job status", agentruntime.ErrToolError)
		}
		if status.State.Terminal() {
			return status, nil
		}
		select {
		case <-ctx.Done():
			status, cancelErr := b.runtime.jobs.Cancel(context.Background(), jobID, "agent tool context cancelled")
			if cancelErr != nil {
				return status, fmt.Errorf("%w: cancel Agent-owned job", agentruntime.ErrToolError)
			}
			return status, ctx.Err()
		case <-ticker.C:
		}
	}
}

func agentOpenCodeToolName(name string) (string, bool) {
	if !strings.HasPrefix(name, "opencode_") {
		return "", false
	}
	remote := strings.TrimPrefix(name, "opencode_")
	if !isVerifiedOpenCodeTool(remote) {
		return "", false
	}
	return remote, true
}

func validateAgentToolArguments(tool string, arguments map[string]any) error {
	if arguments == nil {
		return agentruntime.ErrInvalidArguments
	}
	for _, forbidden := range []string{"session_id", "workspace", "permission_mode", "agent_id", "runtime_scope"} {
		if _, exists := arguments[forbidden]; exists {
			return agentruntime.ErrInvalidArguments
		}
	}
	if _, err := json.Marshal(arguments); err != nil {
		return agentruntime.ErrInvalidArguments
	}
	if tool == "git" {
		operation, ok := arguments["operation"].(string)
		if !ok || strings.TrimSpace(operation) == "" {
			return agentruntime.ErrInvalidArguments
		}
	}
	return nil
}

func agentToolMutates(tool string, arguments map[string]any) bool {
	switch tool {
	case "read", "glob", "grep", "lsp":
		return false
	case "git":
		operation, _ := arguments["operation"].(string)
		switch strings.ToLower(strings.TrimSpace(operation)) {
		case "repository", "status", "diff":
			return false
		default:
			return true
		}
	default:
		return true
	}
}

func cloneAgentArguments(arguments map[string]any) map[string]any {
	copy := make(map[string]any, len(arguments))
	for key, value := range arguments {
		copy[key] = value
	}
	return copy
}

func agentSessionBridgeError(err error) error {
	switch sessionruntime.ErrorCode(err) {
	case sessionruntime.CodeClosed, sessionruntime.CodeFailed, sessionruntime.CodeInvalidTransition, sessionruntime.CodeNotFound:
		return agentruntime.ErrAgentSessionClosed
	case sessionruntime.CodeWorkspaceMismatch, sessionruntime.CodeJobOwnership:
		return agentruntime.ErrAgentSessionOwnershipMismatch
	case sessionruntime.CodeWriteDenied:
		return agentruntime.ErrAgentWriteDenied
	default:
		if errors.Is(err, context.Canceled) {
			return err
		}
		return agentruntime.ErrAgentSessionOwnershipMismatch
	}
}
