package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

func (r *Runtime) d1SessionOpen(ctx context.Context, args map[string]any) (Result, error) {
	if r == nil || r.sessions == nil {
		return nil, toolError("SESSION_RUNTIME_UNAVAILABLE", "session runtime is unavailable", "session")
	}
	record, err := r.sessions.Open(ctx, sessionruntime.OpenRequest{
		ProjectID:      strings.TrimSpace(toolcore.StringArg(args, "project_id", "")),
		RepoRoot:       strings.TrimSpace(toolcore.StringArg(args, "repo_root", "")),
		BaseRef:        strings.TrimSpace(toolcore.StringArg(args, "base_ref", "")),
		PermissionMode: strings.TrimSpace(toolcore.StringArg(args, "permission_mode", "project")),
		ClientTag:      strings.TrimSpace(toolcore.StringArg(args, "client_tag", "")),
		IsolationMode:  sessionruntime.IsolationMode(strings.TrimSpace(toolcore.StringArg(args, "isolation_mode", ""))),
	})
	if err != nil {
		return nil, sessionToolError(err)
	}
	return sessionResult(Result{"ok": true, "session": record})
}

func (r *Runtime) d1SessionStatus(_ context.Context, args map[string]any) (Result, error) {
	if r == nil || r.sessions == nil {
		return nil, toolError("SESSION_RUNTIME_UNAVAILABLE", "session runtime is unavailable", "session")
	}
	sessionID := strings.TrimSpace(toolcore.StringArg(args, "session_id", ""))
	if sessionID == "" {
		return nil, toolError(sessionruntime.CodeNotFound, "session_id is required", "validation")
	}
	record, err := r.sessions.Get(sessionID)
	if err != nil {
		return nil, sessionToolError(err)
	}
	return sessionResult(Result{"ok": true, "session": record})
}

func (r *Runtime) d1SessionList(_ context.Context, args map[string]any) (Result, error) {
	if r == nil || r.sessions == nil {
		return nil, toolError("SESSION_RUNTIME_UNAVAILABLE", "session runtime is unavailable", "session")
	}
	stateFilter := strings.ToLower(strings.TrimSpace(toolcore.StringArg(args, "state_filter", "all")))
	if stateFilter == "" {
		stateFilter = "all"
	}
	if stateFilter != "all" && stateFilter != "active" {
		return nil, toolError("INVALID_ARGUMENT", "state_filter must be all or active", "validation")
	}
	limit := toolcore.IntArg(args, "limit", 100)
	if limit < 1 || limit > 200 {
		return nil, toolError("INVALID_ARGUMENT", "limit must be between 1 and 200", "validation")
	}
	offset, err := decodeSessionCursor(strings.TrimSpace(toolcore.StringArg(args, "cursor", "")))
	if err != nil {
		return nil, toolErrorCause("INVALID_ARGUMENT", "invalid session_list cursor", "validation", nil, err)
	}
	items := r.sessions.List(sessionruntime.ListFilter{
		ActiveOnly: stateFilter == "active",
		ProjectID:  strings.TrimSpace(toolcore.StringArg(args, "project_id", "")),
		ClientTag:  strings.TrimSpace(toolcore.StringArg(args, "client_tag", "")),
	})
	if offset > len(items) {
		return nil, toolError("INVALID_ARGUMENT", "session_list cursor is beyond the result set", "validation")
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[offset:end]
	var next any
	if end < len(items) {
		next = encodeSessionCursor(end)
	}
	return sessionResult(Result{"ok": true, "sessions": page, "next_cursor": next})
}

func (r *Runtime) d1SessionClose(ctx context.Context, args map[string]any) (Result, error) {
	if r == nil || r.sessions == nil {
		return nil, toolError("SESSION_RUNTIME_UNAVAILABLE", "session runtime is unavailable", "session")
	}
	sessionID := strings.TrimSpace(toolcore.StringArg(args, "session_id", ""))
	if sessionID == "" {
		return nil, toolError(sessionruntime.CodeNotFound, "session_id is required", "validation")
	}
	current, err := r.sessions.Get(sessionID)
	if err != nil {
		return nil, sessionToolError(err)
	}
	if current.State == sessionruntime.StateFailed {
		return nil, toolError(sessionruntime.CodeFailed, "failed session cannot be closed in D1; filesystem state is preserved", "session")
	}
	if current.State != sessionruntime.StateClosed && r.agents != nil {
		for _, agent := range r.agents.List(agentruntime.ListFilter{SessionID: sessionID, Limit: 200}) {
			if agent.State.Terminal() {
				continue
			}
			if _, cancelErr := r.agents.Cancel(ctx, sessionID, agent.AgentID, "session_close"); cancelErr != nil {
				return nil, toolErrorCause(sessionruntime.CodeFailed, "failed to stop an owned Agent during session_close", "session", map[string]any{"agent_id": agent.AgentID}, cancelErr)
			}
		}
	}
	jobsCancelled := make([]string, 0)
	if current.State != sessionruntime.StateClosed && r.jobs != nil {
		for _, jobID := range current.JobIDs {
			status, statusErr := r.jobs.Status(jobID)
			if statusErr != nil {
				// Completed jobs may have expired under normal retention. Active jobs are
				// never retention-pruned, so a missing record is safe to treat as terminal.
				continue
			}
			if status.SessionID != sessionID {
				_, _ = r.sessions.Fail(sessionID, "session_close detected a job ownership mismatch")
				return nil, toolError(sessionruntime.CodeJobOwnership, "owned job metadata does not match the closing session", "session")
			}
			if status.State.Terminal() {
				continue
			}
			if _, cancelErr := r.jobs.Cancel(ctx, jobID, "session_close"); cancelErr != nil {
				_, _ = r.sessions.Fail(sessionID, "failed to cancel owned job during session_close: "+cancelErr.Error())
				return nil, toolErrorCause(sessionruntime.CodeFailed, "failed to stop an owned job during session_close", "session", map[string]any{"job_id": jobID}, cancelErr)
			}
			jobsCancelled = append(jobsCancelled, jobID)
		}
	}
	if current.State != sessionruntime.StateClosed && r.openCode != nil && current.RuntimeID != nil && !r.sessionRuntimeHasPeer(current) {
		mode, modeErr := opencoderuntime.ParsePermissionMode(current.PermissionMode)
		if modeErr != nil {
			_, _ = r.sessions.Fail(sessionID, "invalid persisted permission mode during session_close")
			return nil, toolErrorCause(sessionruntime.CodeFailed, "invalid persisted permission mode during session_close", "session", nil, modeErr)
		}
		if releaseErr := r.openCode.Release(current.Workspace(), mode); releaseErr != nil {
			_, _ = r.sessions.Fail(sessionID, "failed to release session runtime: "+releaseErr.Error())
			return nil, toolErrorCause(sessionruntime.CodeFailed, "failed to release session runtime", "session", nil, releaseErr)
		}
	}
	record, err := r.sessions.Close(ctx, sessionID)
	if err != nil {
		return nil, sessionToolError(err)
	}
	return sessionResult(Result{
		"ok":                 true,
		"session":            record,
		"jobs_cancelled":     jobsCancelled,
		"branch_preserved":   true,
		"worktree_preserved": true,
	})
}

func sessionToolError(err error) error {
	if err == nil {
		return nil
	}
	code := sessionruntime.ErrorCode(err)
	if code == "" {
		return toolErrorCause("SESSION_RUNTIME_FAILED", "session runtime request failed", "session", nil, err)
	}
	message := err.Error()
	var sessionErr *sessionruntime.Error
	if ok := errorsAs(err, &sessionErr); ok && strings.TrimSpace(sessionErr.Message) != "" {
		message = sessionErr.Message
	}
	return toolErrorCause(code, message, "session", nil, err)
}

func sessionResult(value Result) (Result, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode session result: %w", err)
	}
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode session result: %w", err)
	}
	return result, nil
}

func encodeSessionCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeSessionCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	offset, err := strconv.Atoi(string(body))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor offset")
	}
	return offset, nil
}

// errorsAs is a tiny indirection so this file does not export implementation details.
func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
