package nativebridge

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

func (m *Manager) ObserveSupervisor(ctx context.Context, agentDockSessionID, subagentID, workspace string, afterCursor int64, limit int) (SupervisorObservation, error) {
	binding, _, err := m.Attach(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return SupervisorObservation{}, err
	}
	items, err := m.native.ListNativeSessions(ctx, binding.Workspace)
	if err != nil {
		return SupervisorObservation{}, &Error{Code: ErrorNative, Message: "list native sessions for supervisor observation", Cause: err}
	}
	owned := descendantOwnershipClosure(items, binding.OpenCodeSessionID, binding.Workspace)
	statuses, err := m.native.ObserveNativeStatus(ctx, binding.Workspace)
	if err != nil {
		return SupervisorObservation{}, &Error{Code: ErrorNative, Message: "observe native status for supervisor", Cause: err}
	}
	filteredStatuses := map[string]json.RawMessage{}
	for id, value := range statuses {
		if _, ok := owned[id]; !ok {
			continue
		}
		filteredStatuses[id] = append(json.RawMessage(nil), value...)
	}

	ids := make([]string, 0, len(owned))
	for id := range owned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	messages := []OwnedMessage{}
	for _, id := range ids {
		items, listErr := m.native.ListNativeMessages(ctx, binding.Workspace, id)
		if listErr != nil {
			if id != binding.OpenCodeSessionID && opencodenative.ErrorCode(listErr) == opencodenative.ErrorNotFound {
				continue
			}
			return SupervisorObservation{}, &Error{Code: ErrorNative, Message: "list native messages for supervisor", Cause: listErr}
		}
		for _, message := range items {
			messages = append(messages, OwnedMessage{OpenCodeSessionID: id, Message: message})
		}
	}
	page, err := m.sessions.NativePublicEvents(binding.AgentDockSessionID, binding.SubagentID, afterCursor, limit)
	if err != nil {
		return SupervisorObservation{}, m.authorityError("read durable supervisor public events", err)
	}
	pending, err := m.pendingSupervisorPermissions(ctx, binding, owned)
	if err != nil {
		return SupervisorObservation{}, err
	}
	return SupervisorObservation{
		RootOpenCodeSessionID: binding.OpenCodeSessionID,
		Statuses:              filteredStatuses,
		Messages:              messages,
		PublicEvents:          page,
		PendingPermissions:    pending,
	}, nil
}

func (m *Manager) Correct(ctx context.Context, agentDockSessionID, subagentID, workspace string, request CorrectionRequest) (CorrectionResult, error) {
	if request.Mode != CorrectionGraceful && request.Mode != CorrectionForce {
		return CorrectionResult{}, &Error{Code: ErrorInvalid, Message: "correction mode must be graceful or force"}
	}
	if !hasPromptContent(request.Prompt) {
		return CorrectionResult{}, &Error{Code: ErrorInvalid, Message: "correction prompt requires non-empty text content"}
	}
	release := m.acquireKey("supervisor\x00" + strings.TrimSpace(agentDockSessionID) + "\x00" + strings.TrimSpace(subagentID))
	defer release()

	before, _, err := m.Attach(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return CorrectionResult{}, err
	}
	orderID := strings.Replace(randomSubagentID(), "subagent_", "supervisor_", 1)
	if _, err := m.sessions.UpdateNativeSessionBinding(before.AgentDockSessionID, before.SubagentID, func(value *session.NativeSessionBinding) error {
		value.PendingSupervisorMessageID = orderID
		value.LastActivity = m.now().UTC()
		return nil
	}); err != nil {
		return CorrectionResult{}, &Error{Code: ErrorReconciliation, Message: "persist pending supervisor correction", Cause: err}
	}
	if err := m.appendSupervisorEvent(before, "supervisor_order", map[string]any{
		"order_id": orderID, "mode": request.Mode, "delivery": "pending", "same_session_required": true, "preemptive_claimed": false,
	}); err != nil {
		return CorrectionResult{}, err
	}

	aborted := false
	if request.Mode == CorrectionForce {
		if err := m.native.AbortNativeExecution(ctx, before.Workspace, before.OpenCodeSessionID); err != nil {
			return CorrectionResult{}, &Error{Code: ErrorNative, Message: "force correction abort current native execution", Cause: err}
		}
		aborted = true
		if err := m.appendSupervisorEvent(before, "activity", map[string]any{
			"order_id": orderID, "action": "abort", "opencode_session_id": before.OpenCodeSessionID,
		}); err != nil {
			return CorrectionResult{}, err
		}
	}
	if err := m.native.ContinueNativeConversation(ctx, before.Workspace, before.OpenCodeSessionID, request.Prompt); err != nil {
		return CorrectionResult{}, &Error{Code: ErrorNative, Message: "submit same-session supervisor correction", Cause: err}
	}
	after, _, err := m.Attach(ctx, before.AgentDockSessionID, before.SubagentID, before.Workspace)
	if err != nil {
		return CorrectionResult{}, err
	}
	if before.OpenCodeSessionID != after.OpenCodeSessionID {
		return CorrectionResult{}, &Error{Code: ErrorSameSession, Message: "supervisor correction changed OpenCodeSessionID; refusing replacement-session semantics"}
	}
	if _, err := m.sessions.UpdateNativeSessionBinding(before.AgentDockSessionID, before.SubagentID, func(value *session.NativeSessionBinding) error {
		if value.OpenCodeSessionID != before.OpenCodeSessionID {
			return &Error{Code: ErrorSameSession, Message: "durable mapping changed during supervisor correction"}
		}
		value.PendingSupervisorMessageID = ""
		value.LastActivity = m.now().UTC()
		return nil
	}); err != nil {
		if ErrorCode(err) == ErrorSameSession {
			return CorrectionResult{}, err
		}
		return CorrectionResult{}, &Error{Code: ErrorReconciliation, Message: "finalize durable supervisor correction", Cause: err}
	}
	if err := m.appendSupervisorEvent(after, "supervisor_correction", map[string]any{
		"order_id": orderID, "mode": request.Mode, "delivery": "accepted", "same_session": true,
		"opencode_session_id_before": before.OpenCodeSessionID, "opencode_session_id_after": after.OpenCodeSessionID,
	}); err != nil {
		return CorrectionResult{}, err
	}
	return CorrectionResult{
		Mode: request.Mode, OrderID: orderID,
		OpenCodeSessionIDBefore: before.OpenCodeSessionID,
		OpenCodeSessionIDAfter:  after.OpenCodeSessionID,
		Aborted:                 aborted,
		PreemptiveClaimed:       false,
	}, nil
}

func (m *Manager) CancelSupervisor(ctx context.Context, agentDockSessionID, subagentID, workspace string) (CancelResult, error) {
	binding, _, err := m.Attach(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return CancelResult{}, err
	}
	if err := m.native.AbortNativeExecution(ctx, binding.Workspace, binding.OpenCodeSessionID); err != nil {
		return CancelResult{}, &Error{Code: ErrorNative, Message: "cancel exact supervisor-owned native execution", Cause: err}
	}
	if err := m.appendSupervisorEvent(binding, "activity", map[string]any{
		"action": "cancel", "opencode_session_id": binding.OpenCodeSessionID,
	}); err != nil {
		return CancelResult{}, err
	}
	return CancelResult{OpenCodeSessionID: binding.OpenCodeSessionID, Aborted: true}, nil
}

func (m *Manager) PendingSupervisorPermissions(ctx context.Context, agentDockSessionID, subagentID, workspace string) ([]SupervisorPermissionIdentity, error) {
	binding, _, err := m.Attach(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return nil, err
	}
	items, err := m.native.ListNativeSessions(ctx, binding.Workspace)
	if err != nil {
		return nil, &Error{Code: ErrorNative, Message: "list native sessions for permission ownership", Cause: err}
	}
	return m.pendingSupervisorPermissions(ctx, binding, descendantOwnershipClosure(items, binding.OpenCodeSessionID, binding.Workspace))
}

func (m *Manager) RespondSupervisorPermission(ctx context.Context, agentDockSessionID, subagentID, workspace, requestID string, decision PermissionDecision, message string) (SupervisorPermissionIdentity, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return SupervisorPermissionIdentity{}, &Error{Code: ErrorInvalid, Message: "permission request id is required"}
	}
	if decision != PermissionDecisionApprove && decision != PermissionDecisionReject {
		return SupervisorPermissionIdentity{}, &Error{Code: ErrorInvalid, Message: "permission decision must be approve or reject"}
	}
	pending, err := m.PendingSupervisorPermissions(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return SupervisorPermissionIdentity{}, err
	}
	var identity SupervisorPermissionIdentity
	found := false
	for _, item := range pending {
		if item.RequestID == requestID {
			identity = item
			found = true
			break
		}
	}
	if !found {
		return SupervisorPermissionIdentity{}, &Error{Code: ErrorPermissionOwnership, Message: "permission request is not pending inside the exact supervisor-owned native root closure"}
	}

	// Supervisor approval is deliberately one-shot. Never sending OpenCode's
	// "always" reply prevents an approval from becoming a durable rule that
	// could outrank a later HARD DENY. Reject remains explicit and same-session.
	reply := opencodenative.PermissionReplyRequest{Reply: opencodenative.PermissionReplyReject, Message: strings.TrimSpace(message)}
	if decision == PermissionDecisionApprove {
		reply.Reply = opencodenative.PermissionReplyOnce
		reply.Message = ""
	}
	if err := m.native.ReplyNativePermission(ctx, identity.Workspace, identity.RequestID, reply); err != nil {
		return SupervisorPermissionIdentity{}, &Error{Code: ErrorNative, Message: "reply to owned native permission request", Cause: err}
	}
	binding, err := m.sessions.NativeSessionBinding(identity.AgentDockSessionID, identity.SubagentID)
	if err != nil {
		return SupervisorPermissionIdentity{}, m.authorityError("reload supervisor binding after permission reply", err)
	}
	if err := m.appendSupervisorEvent(binding, "permission", map[string]any{
		"request_id": identity.RequestID, "native_session_id": identity.NativeSessionID, "decision": decision, "approval_scope": identity.ApprovalScope,
	}); err != nil {
		return SupervisorPermissionIdentity{}, err
	}
	return identity, nil
}

func (m *Manager) pendingSupervisorPermissions(ctx context.Context, binding session.NativeSessionBinding, owned map[string]struct{}) ([]SupervisorPermissionIdentity, error) {
	requests, err := m.native.ListNativePermissions(ctx, binding.Workspace)
	if err != nil {
		return nil, &Error{Code: ErrorNative, Message: "list native permission requests", Cause: err}
	}
	result := make([]SupervisorPermissionIdentity, 0, len(requests))
	for _, request := range requests {
		if _, ok := owned[request.SessionID]; !ok {
			continue
		}
		identity := SupervisorPermissionIdentity{
			RequestID: request.ID, NativeSessionID: request.SessionID, RootOpenCodeSessionID: binding.OpenCodeSessionID,
			AgentDockSessionID: binding.AgentDockSessionID, SubagentID: binding.SubagentID, Workspace: binding.Workspace,
			PermissionMode: binding.PermissionMode, Permission: request.Permission,
			Patterns: append([]string(nil), request.Patterns...), Always: append([]string(nil), request.Always...),
			Metadata: cloneMetadata(request.Metadata), ApprovalScope: "once",
		}
		if request.Tool != nil {
			tool := *request.Tool
			identity.Tool = &tool
		}
		result = append(result, identity)
	}
	return result, nil
}

func (m *Manager) appendSupervisorEvent(binding session.NativeSessionBinding, kind string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return &Error{Code: ErrorInvalid, Message: "encode supervisor event", Cause: err}
	}
	_, err = m.sessions.AppendNativePublicEvent(binding.AgentDockSessionID, binding.SubagentID, session.NativePublicEvent{
		Kind: kind, NativeSessionID: binding.OpenCodeSessionID, Payload: encoded, CreatedAt: m.now().UTC(),
	})
	if err != nil {
		return &Error{Code: ErrorReconciliation, Message: "persist supervisor public event", Cause: err}
	}
	return nil
}

func hasPromptContent(prompt opencodenative.PromptRequest) bool {
	for _, part := range prompt.Parts {
		if strings.TrimSpace(part.Text) != "" {
			return true
		}
	}
	return false
}

func cloneMetadata(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	copy := make(map[string]any, len(input))
	for key, value := range input {
		copy[key] = value
	}
	return copy
}

func classifyNativePublicEvent(event opencodenative.NativeEvent) session.NativePublicEvent {
	typeName := strings.ToLower(strings.TrimSpace(event.Type))
	kind := "event"
	switch {
	case strings.Contains(typeName, "permission"):
		kind = "permission"
	case strings.Contains(typeName, "error") || strings.Contains(typeName, "failed"):
		kind = "error"
	case strings.Contains(typeName, "test"):
		kind = "test"
	case strings.Contains(typeName, "file"):
		kind = "file"
	case strings.Contains(typeName, "tool") && (strings.Contains(typeName, "success") || strings.Contains(typeName, "result") || strings.Contains(typeName, "completed")):
		kind = "tool_result"
	case strings.Contains(typeName, "tool"):
		kind = "tool_call"
	case strings.Contains(typeName, "message"):
		kind = classifyMessageEvent(event.Properties)
	case strings.Contains(typeName, "status") || strings.Contains(typeName, "busy") || strings.Contains(typeName, "idle"):
		kind = "activity"
	case strings.Contains(typeName, "result") || strings.Contains(typeName, "completed") || strings.Contains(typeName, "finish"):
		kind = "result"
	}
	messageID, partID := nativeEventMessagePartIDs(event.Properties)
	return session.NativePublicEvent{
		Kind: kind, NativeEventID: event.ID, NativeEventSeq: event.Seq, NativeType: event.Type,
		NativeSessionID: nativeEventSessionID(event), MessageID: messageID, PartID: partID,
		Payload: append(json.RawMessage(nil), event.Properties...),
	}
}

func classifyMessageEvent(properties json.RawMessage) string {
	var payload map[string]any
	if json.Unmarshal(properties, &payload) != nil {
		return "message"
	}
	part, _ := payload["part"].(map[string]any)
	if part == nil {
		return "message"
	}
	if strings.EqualFold(stringValue(part["type"]), "tool") {
		state, _ := part["state"].(map[string]any)
		status := strings.ToLower(stringValue(state["status"]))
		if status == "error" || status == "failed" {
			return "error"
		}
		if status == "completed" || status == "success" {
			return "tool_result"
		}
		return "tool_call"
	}
	return "message"
}

func nativeEventMessagePartIDs(properties json.RawMessage) (string, string) {
	var payload map[string]any
	if json.Unmarshal(properties, &payload) != nil {
		return "", ""
	}
	messageID := firstString(payload, "messageID", "messageId", "message_id")
	partID := firstString(payload, "partID", "partId", "part_id")
	if part, _ := payload["part"].(map[string]any); part != nil {
		if messageID == "" {
			messageID = firstString(part, "messageID", "messageId", "message_id")
		}
		if partID == "" {
			partID = firstString(part, "id", "partID", "partId", "part_id")
		}
	}
	if info, _ := payload["info"].(map[string]any); info != nil && messageID == "" {
		messageID = firstString(info, "id", "messageID", "messageId", "message_id")
	}
	return messageID, partID
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringValue(values[key])); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
