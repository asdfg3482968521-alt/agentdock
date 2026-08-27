package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

func (r *Runtime) d2AgentStart(ctx context.Context, args map[string]any) (Result, error) {
	if err := validateFrozenAgentInput("agent_start", args); err != nil {
		return nil, err
	}
	if r == nil || r.agents == nil || r.credentialStore == nil {
		return nil, agentToolError(agentruntime.ErrAgentSessionClosed)
	}
	sessionID := strings.TrimSpace(toolcore.StringArg(args, "session_id", ""))
	binding, err := r.resolveD2AgentSession(ctx, sessionID)
	if err != nil {
		return nil, agentToolError(err)
	}
	permission := strings.TrimSpace(toolcore.StringArg(args, "permission", ""))
	if permission == "project" && binding.PermissionMode != "project" {
		return nil, agentToolError(agentruntime.ErrAgentWriteDenied)
	}
	provider := strings.ToLower(strings.TrimSpace(toolcore.StringArg(args, "provider", "")))
	credentialRef := strings.TrimSpace(toolcore.StringArg(args, "credential_ref", ""))
	route := routeAgentBackend(provider)
	if err := validateAgentStartCredential(ctx, r.credentialStore, route, provider, credentialRef); err != nil {
		return nil, agentToolError(err)
	}
	model := ""
	if raw, exists := args["model"]; exists && raw != nil {
		model, _ = raw.(string)
	}
	budget, _ := args["budget"].(map[string]any)
	outcome, err := r.agents.StartAdmission(ctx, agentruntime.StartRequest{
		SessionID:       sessionID,
		ClientRequestID: strings.TrimSpace(toolcore.StringArg(args, "client_request_id", "")),
		Provider:        provider,
		Model:           strings.TrimSpace(model),
		Role:            strings.TrimSpace(toolcore.StringArg(args, "role", "")),
		CredentialRef:   credentialRef,
		Prompt:          toolcore.StringArg(args, "prompt", ""),
		PermissionMode:  permission,
		MaxRequests:     toolcore.IntArg(budget, "max_requests", 0),
		Timeout:         time.Duration(toolcore.IntArg(args, "timeout_ms", 0)) * time.Millisecond,
	})
	if err != nil {
		return nil, agentToolError(err)
	}
	record := outcome.Record
	resource, err := agentPublicResourceMap(record)
	if err != nil {
		return nil, agentToolError(err)
	}
	return Result{"ok": true, "agent": resource, "created": outcome.Created, "replayed": outcome.Replayed}, nil
}

func (r *Runtime) d2AgentStatus(ctx context.Context, args map[string]any) (Result, error) {
	if err := validateFrozenAgentInput("agent_status", args); err != nil {
		return nil, err
	}
	sessionID, agentID, err := r.d2AgentOwnedIDs(ctx, args)
	if err != nil {
		return nil, err
	}
	record, err := r.agents.Get(sessionID, agentID)
	if err != nil {
		return nil, agentToolError(err)
	}
	status, err := agentPublicStatusMap(record)
	if err != nil {
		return nil, agentToolError(err)
	}
	return Result{"ok": true, "agent": status}, nil
}

func (r *Runtime) d2AgentOutput(ctx context.Context, args map[string]any) (Result, error) {
	if err := validateFrozenAgentInput("agent_output", args); err != nil {
		return nil, err
	}
	sessionID, agentID, err := r.d2AgentOwnedIDs(ctx, args)
	if err != nil {
		return nil, err
	}
	cursor := int64(toolcore.IntArg(args, "cursor", 0))
	limit := toolcore.IntArg(args, "limit", 65536)
	output, err := r.agents.Output(sessionID, agentID, cursor, limit)
	if err != nil {
		return nil, agentToolError(err)
	}
	return Result{
		"ok":          true,
		"agent_id":    agentID,
		"session_id":  sessionID,
		"cursor":      cursor,
		"next_cursor": output.NextCursor,
		"output":      output.Text,
		"has_more":    output.Truncated,
	}, nil
}

func (r *Runtime) d2AgentResult(ctx context.Context, args map[string]any) (Result, error) {
	if err := validateFrozenAgentInput("agent_result", args); err != nil {
		return nil, err
	}
	sessionID, agentID, err := r.d2AgentOwnedIDs(ctx, args)
	if err != nil {
		return nil, err
	}
	record, err := r.agents.Get(sessionID, agentID)
	if err != nil {
		return nil, agentToolError(err)
	}
	if !record.State.Terminal() {
		return nil, agentToolError(agentruntime.ErrAgentNotTerminal)
	}
	resource, err := agentPublicResourceMap(record)
	if err != nil {
		return nil, agentToolError(err)
	}
	var result any
	if strings.TrimSpace(record.ResultSummary) != "" {
		result = record.ResultSummary
	}
	return Result{
		"ok":             true,
		"agent_id":       agentID,
		"session_id":     sessionID,
		"state":          string(record.State),
		"result":         result,
		"usage":          resource["usage"],
		"failure_reason": resource["failure_reason"],
	}, nil
}

func (r *Runtime) d2AgentCancel(ctx context.Context, args map[string]any) (Result, error) {
	if err := validateFrozenAgentInput("agent_cancel", args); err != nil {
		return nil, err
	}
	sessionID, agentID, err := r.d2AgentOwnedIDs(ctx, args)
	if err != nil {
		return nil, err
	}
	current, err := r.agents.Get(sessionID, agentID)
	if err != nil {
		return nil, agentToolError(err)
	}
	alreadyTerminal := current.State.Terminal()
	reason := ""
	if raw, exists := args["reason"]; exists && raw != nil {
		reason, _ = raw.(string)
	}
	record, err := r.agents.Cancel(ctx, sessionID, agentID, reason)
	if err != nil {
		return nil, agentToolError(err)
	}
	return Result{
		"ok":               true,
		"agent_id":         agentID,
		"session_id":       sessionID,
		"state":            string(record.State),
		"already_terminal": alreadyTerminal,
	}, nil
}

func (r *Runtime) d2AgentList(ctx context.Context, args map[string]any) (Result, error) {
	if err := validateFrozenAgentInput("agent_list", args); err != nil {
		return nil, err
	}
	if r == nil || r.agents == nil {
		return nil, agentToolError(agentruntime.ErrAgentSessionClosed)
	}
	sessionID := strings.TrimSpace(toolcore.StringArg(args, "session_id", ""))
	if _, err := r.resolveD2AgentSession(ctx, sessionID); err != nil {
		return nil, agentToolError(err)
	}
	state := agentruntime.AgentState("")
	if raw, exists := args["state"]; exists && raw != nil {
		state = agentruntime.AgentState(strings.TrimSpace(raw.(string)))
	}
	provider := ""
	if raw, exists := args["provider"]; exists && raw != nil {
		provider = strings.TrimSpace(raw.(string))
	}
	cursor := ""
	if raw, exists := args["cursor"]; exists && raw != nil {
		cursor = strings.TrimSpace(raw.(string))
	}
	page, err := r.agents.ListPage(agentruntime.ListFilter{
		SessionID: sessionID,
		State:     state,
		Provider:  provider,
		Limit:     toolcore.IntArg(args, "limit", 100),
		Cursor:    cursor,
	})
	if err != nil {
		return nil, agentToolError(err)
	}
	agents := make([]any, 0, len(page.Records))
	for _, record := range page.Records {
		if record.SessionID != sessionID {
			return nil, agentToolError(agentruntime.ErrAgentSessionOwnershipMismatch)
		}
		resource, err := agentPublicResourceMap(record)
		if err != nil {
			return nil, agentToolError(err)
		}
		agents = append(agents, resource)
	}
	var next any
	if page.NextCursor != "" {
		next = page.NextCursor
	}
	return Result{"ok": true, "agents": agents, "next_cursor": next}, nil
}

func (r *Runtime) d2AgentOwnedIDs(ctx context.Context, args map[string]any) (string, string, error) {
	if r == nil || r.agents == nil {
		return "", "", agentToolError(agentruntime.ErrAgentSessionClosed)
	}
	sessionID := strings.TrimSpace(toolcore.StringArg(args, "session_id", ""))
	if _, err := r.resolveD2AgentSession(ctx, sessionID); err != nil {
		return "", "", agentToolError(err)
	}
	agentID := strings.TrimSpace(toolcore.StringArg(args, "agent_id", ""))
	return sessionID, agentID, nil
}

func (r *Runtime) resolveD2AgentSession(ctx context.Context, sessionID string) (agentruntime.SessionBinding, error) {
	resolver := newAgentSessionResolver(r)
	binding, err := resolver.ResolveAgentSession(ctx, sessionID)
	if err != nil {
		return agentruntime.SessionBinding{}, err
	}
	if binding.SessionID != strings.TrimSpace(sessionID) {
		return agentruntime.SessionBinding{}, agentruntime.ErrAgentSessionOwnershipMismatch
	}
	return binding, nil
}

func agentToolError(err error) error {
	if err == nil {
		return nil
	}
	var existing *ToolError
	if errors.As(err, &existing) {
		return existing
	}
	envelope := agentruntime.AgentErrorFrom(err, nil)
	details := map[string]any{"retryable": envelope.Retryable}
	if envelope.ProviderCode != nil {
		details["provider_code"] = *envelope.ProviderCode
	}
	for key, value := range envelope.Details {
		details[key] = value
	}
	return toolErrorCause(envelope.Code, envelope.Message, "agent", details, err)
}

func agentPublicResourceMap(record agentruntime.Record) (map[string]any, error) {
	return normalizeAgentPublicObject(agentruntime.PublicResource(record))
}

func agentPublicStatusMap(record agentruntime.Record) (map[string]any, error) {
	resource, err := agentPublicResourceMap(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"agent_id":       resource["agent_id"],
		"session_id":     resource["session_id"],
		"provider":       resource["provider"],
		"model":          resource["model"],
		"role":           resource["role"],
		"state":          resource["state"],
		"created_at":     resource["created_at"],
		"started_at":     resource["started_at"],
		"finished_at":    resource["finished_at"],
		"usage":          resource["usage"],
		"failure_reason": resource["failure_reason"],
	}, nil
}

func normalizeAgentPublicObject(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized map[string]any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}
