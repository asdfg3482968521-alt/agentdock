package nativebridge

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

func supervisorMutationKey(agentDockSessionID, subagentID string) string {
	return "supervisor\x00" + strings.TrimSpace(agentDockSessionID) + "\x00" + strings.TrimSpace(subagentID)
}

func randomSupervisorMessageID() string {
	return strings.Replace(randomSubagentID(), "subagent_", "supervisor_message_", 1)
}

func (m *Manager) SubmitSupervisorMessage(ctx context.Context, request SupervisorMessageRequest) (SupervisorMessageResult, error) {
	request.AgentDockSessionID = strings.TrimSpace(request.AgentDockSessionID)
	request.SubagentID = strings.TrimSpace(request.SubagentID)
	request.Workspace = strings.TrimSpace(request.Workspace)
	request.ClientMessageID = strings.TrimSpace(request.ClientMessageID)
	request.Kind = strings.ToUpper(strings.TrimSpace(request.Kind))
	if request.AgentDockSessionID == "" || request.SubagentID == "" || request.ClientMessageID == "" {
		return SupervisorMessageResult{}, &Error{Code: ErrorInvalid, Message: "agentdock_session_id, subagent_id, and client_message_id are required"}
	}
	if request.Kind != "TASK" && request.Kind != "FOLLOWUP" && request.Kind != "CORRECTION" {
		return SupervisorMessageResult{}, &Error{Code: ErrorInvalid, Message: "supervisor message kind must be TASK, FOLLOWUP, or CORRECTION"}
	}
	if !hasPromptContent(request.Prompt) {
		return SupervisorMessageResult{}, &Error{Code: ErrorInvalid, Message: "supervisor message prompt requires non-empty text content"}
	}

	release := m.acquireKey(supervisorMutationKey(request.AgentDockSessionID, request.SubagentID))
	defer release()

	binding, _, err := m.Attach(ctx, request.AgentDockSessionID, request.SubagentID, request.Workspace)
	if err != nil {
		return SupervisorMessageResult{}, err
	}
	if binding.State != session.NativeConversationActive {
		return SupervisorMessageResult{}, &Error{Code: ErrorLost, Message: "supervisor message requires an ACTIVE native conversation"}
	}

	prompt := request.Prompt
	prompt.MessageID = randomSupervisorMessageID()
	payload, err := json.Marshal(prompt)
	if err != nil {
		return SupervisorMessageResult{}, &Error{Code: ErrorInvalid, Message: "encode durable supervisor prompt", Cause: err}
	}
	message, created, err := m.sessions.AdmitNativeSupervisorMessage(
		binding.AgentDockSessionID,
		binding.SubagentID,
		request.ClientMessageID,
		request.Kind,
		prompt.MessageID,
		payload,
	)
	if err != nil {
		return SupervisorMessageResult{}, m.authorityError("admit durable supervisor message", err)
	}
	result := SupervisorMessageResult{
		OpenCodeSessionID: binding.OpenCodeSessionID,
		Message:           message,
		Created:           created,
	}
	if !created {
		return result, nil
	}

	prompt.MessageID = message.SupervisorMessageID
	if _, err := m.sessions.UpdateNativeSupervisorMessage(binding.AgentDockSessionID, binding.SubagentID, message.SupervisorMessageID, func(value *session.NativeSupervisorMessage) error {
		value.State = session.NativeSupervisorDispatching
		return nil
	}); err != nil {
		return SupervisorMessageResult{}, m.authorityError("persist supervisor message dispatch intent", err)
	}
	if err := m.native.ContinueNativeConversation(ctx, binding.Workspace, binding.OpenCodeSessionID, prompt); err != nil {
		return SupervisorMessageResult{}, &Error{Code: ErrorNative, Message: "submit ordered same-session supervisor message", Cause: err}
	}
	accepted, err := m.sessions.UpdateNativeSupervisorMessage(binding.AgentDockSessionID, binding.SubagentID, message.SupervisorMessageID, func(value *session.NativeSupervisorMessage) error {
		value.State = session.NativeSupervisorAccepted
		value.NativeMessageID = value.SupervisorMessageID
		return nil
	})
	if err != nil {
		return SupervisorMessageResult{}, &Error{Code: ErrorReconciliation, Message: "persist accepted supervisor message after native submission", Cause: err}
	}
	_, _ = m.sessions.UpdateNativeSessionBinding(binding.AgentDockSessionID, binding.SubagentID, func(value *session.NativeSessionBinding) error {
		value.LastNativeMessageID = accepted.SupervisorMessageID
		return nil
	})
	result.Message = accepted
	result.Dispatched = true
	return result, nil
}

func (m *Manager) ReconcileSupervisorMessages(ctx context.Context, agentDockSessionID, subagentID, workspace string) ([]SupervisorMessageResult, error) {
	agentDockSessionID = strings.TrimSpace(agentDockSessionID)
	subagentID = strings.TrimSpace(subagentID)
	workspace = strings.TrimSpace(workspace)
	if agentDockSessionID == "" || subagentID == "" {
		return nil, &Error{Code: ErrorInvalid, Message: "agentdock_session_id and subagent_id are required for supervisor reconciliation"}
	}
	release := m.acquireKey(supervisorMutationKey(agentDockSessionID, subagentID))
	defer release()

	binding, _, err := m.Attach(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return nil, err
	}
	nativeMessages, err := m.native.ListNativeMessages(ctx, binding.Workspace, binding.OpenCodeSessionID)
	if err != nil {
		return nil, &Error{Code: ErrorNative, Message: "list exact native root messages for supervisor reconciliation", Cause: err}
	}
	found := make(map[string]string, len(nativeMessages))
	for _, nativeMessage := range nativeMessages {
		if id := strings.TrimSpace(nativeMessage.Info.ID); id != "" {
			found[id] = id
		}
	}

	pending := append([]session.NativeSupervisorMessage(nil), binding.SupervisorMessages...)
	sort.Slice(pending, func(i, j int) bool { return pending[i].MessageSeq < pending[j].MessageSeq })
	results := make([]SupervisorMessageResult, 0, len(pending))
	for _, message := range pending {
		if message.State != session.NativeSupervisorQueued && message.State != session.NativeSupervisorDispatching {
			continue
		}
		result := SupervisorMessageResult{OpenCodeSessionID: binding.OpenCodeSessionID, Message: message}
		if nativeID, ok := found[message.SupervisorMessageID]; ok {
			accepted, updateErr := m.sessions.UpdateNativeSupervisorMessage(binding.AgentDockSessionID, binding.SubagentID, message.SupervisorMessageID, func(value *session.NativeSupervisorMessage) error {
				value.State = session.NativeSupervisorAccepted
				value.NativeMessageID = nativeID
				return nil
			})
			if updateErr != nil {
				return nil, m.authorityError("persist reconciled native supervisor message", updateErr)
			}
			result.Message = accepted
			results = append(results, result)
			continue
		}

		prompt, decodeErr := decodeDurableSupervisorPrompt(message)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if message.State == session.NativeSupervisorQueued {
			if _, updateErr := m.sessions.UpdateNativeSupervisorMessage(binding.AgentDockSessionID, binding.SubagentID, message.SupervisorMessageID, func(value *session.NativeSupervisorMessage) error {
				value.State = session.NativeSupervisorDispatching
				return nil
			}); updateErr != nil {
				return nil, m.authorityError("persist recovered supervisor dispatch intent", updateErr)
			}
		}
		if err := m.native.ContinueNativeConversation(ctx, binding.Workspace, binding.OpenCodeSessionID, prompt); err != nil {
			return nil, &Error{Code: ErrorNative, Message: "resend reconciled supervisor message on exact native root", Cause: err}
		}
		accepted, updateErr := m.sessions.UpdateNativeSupervisorMessage(binding.AgentDockSessionID, binding.SubagentID, message.SupervisorMessageID, func(value *session.NativeSupervisorMessage) error {
			value.State = session.NativeSupervisorAccepted
			value.NativeMessageID = value.SupervisorMessageID
			return nil
		})
		if updateErr != nil {
			return nil, &Error{Code: ErrorReconciliation, Message: "persist accepted recovered supervisor message", Cause: updateErr}
		}
		result.Message = accepted
		result.Dispatched = true
		results = append(results, result)
	}
	return results, nil
}

func (m *Manager) MarkSupervisorExecutionTerminal(ctx context.Context, agentDockSessionID, subagentID, workspace, supervisorMessageID, outcome string) (session.NativeSupervisorMessage, error) {
	agentDockSessionID = strings.TrimSpace(agentDockSessionID)
	subagentID = strings.TrimSpace(subagentID)
	supervisorMessageID = strings.TrimSpace(supervisorMessageID)
	outcome = strings.ToUpper(strings.TrimSpace(outcome))
	states := map[string]session.NativeExecutionState{
		"SUCCEEDED":   session.NativeExecutionSucceeded,
		"FAILED":      session.NativeExecutionFailed,
		"CANCELLED":   session.NativeExecutionCancelled,
		"TIMED_OUT":   session.NativeExecutionTimedOut,
		"INTERRUPTED": session.NativeExecutionInterrupted,
	}
	executionState, ok := states[outcome]
	if agentDockSessionID == "" || subagentID == "" || supervisorMessageID == "" || !ok {
		return session.NativeSupervisorMessage{}, &Error{Code: ErrorInvalid, Message: "terminal supervisor update requires ids and SUCCEEDED/FAILED/CANCELLED/TIMED_OUT/INTERRUPTED outcome"}
	}
	release := m.acquireKey(supervisorMutationKey(agentDockSessionID, subagentID))
	defer release()
	binding, _, err := m.Attach(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return session.NativeSupervisorMessage{}, err
	}
	var terminal session.NativeSupervisorMessage
	found := false
	_, err = m.sessions.UpdateNativeSessionBinding(binding.AgentDockSessionID, binding.SubagentID, func(value *session.NativeSessionBinding) error {
		if value.State != session.NativeConversationActive {
			return &Error{Code: ErrorLost, Message: "terminal execution update cannot revive a non-active native conversation"}
		}
		for i := range value.SupervisorMessages {
			if value.SupervisorMessages[i].SupervisorMessageID != supervisorMessageID {
				continue
			}
			value.SupervisorMessages[i].State = session.NativeSupervisorTerminal
			value.SupervisorMessages[i].TerminalOutcome = outcome
			value.SupervisorMessages[i].UpdatedAt = m.now().UTC()
			terminal = value.SupervisorMessages[i]
			found = true
			break
		}
		if !found {
			return &Error{Code: ErrorInvalid, Message: "supervisor message not found for terminal execution update"}
		}
		value.ExecutionState = executionState
		value.CurrentExecutionID = ""
		value.PendingSupervisorMessageID = ""
		return nil
	})
	if err != nil {
		if ErrorCode(err) != "" {
			return session.NativeSupervisorMessage{}, err
		}
		return session.NativeSupervisorMessage{}, m.authorityError("persist terminal supervisor execution", err)
	}
	return terminal, nil
}

func (m *Manager) AdmitSupervisorPermissionApproval(ctx context.Context, agentDockSessionID, subagentID, workspace, supervisorMessageID, nativePermissionRequestID string, observedEventCursor int64) (session.NativePermissionApproval, bool, error) {
	agentDockSessionID = strings.TrimSpace(agentDockSessionID)
	subagentID = strings.TrimSpace(subagentID)
	supervisorMessageID = strings.TrimSpace(supervisorMessageID)
	nativePermissionRequestID = strings.TrimSpace(nativePermissionRequestID)
	if agentDockSessionID == "" || subagentID == "" || supervisorMessageID == "" || nativePermissionRequestID == "" {
		return session.NativePermissionApproval{}, false, &Error{Code: ErrorInvalid, Message: "approval admission requires agentdock_session_id, subagent_id, supervisor_message_id, and native permission request id"}
	}
	release := m.acquireKey(supervisorMutationKey(agentDockSessionID, subagentID))
	defer release()
	binding, _, err := m.Attach(ctx, agentDockSessionID, subagentID, workspace)
	if err != nil {
		return session.NativePermissionApproval{}, false, err
	}
	if observedEventCursor <= 0 || observedEventCursor > binding.LastOwnedEventCursor {
		return session.NativePermissionApproval{}, false, &Error{Code: ErrorReconciliation, Message: "approval request must bind to an already durable owned native event cursor"}
	}
	approval, created, err := m.sessions.AdmitNativePermissionApproval(binding.AgentDockSessionID, binding.SubagentID, session.NativePermissionApproval{
		NativePermissionRequestID: nativePermissionRequestID,
		SupervisorMessageID:       supervisorMessageID,
		ObservedEventCursor:       observedEventCursor,
	})
	if err != nil {
		return session.NativePermissionApproval{}, false, &Error{Code: ErrorReconciliation, Message: "persist ordered supervisor permission approval hook", Cause: err}
	}
	return approval, created, nil
}

func decodeDurableSupervisorPrompt(message session.NativeSupervisorMessage) (opencodenative.PromptRequest, error) {
	var prompt opencodenative.PromptRequest
	if len(message.PromptPayload) == 0 {
		return prompt, &Error{Code: ErrorReconciliation, Message: "durable supervisor message is missing prompt payload"}
	}
	if err := json.Unmarshal(message.PromptPayload, &prompt); err != nil {
		return prompt, &Error{Code: ErrorReconciliation, Message: "decode durable supervisor prompt payload", Cause: err}
	}
	prompt.MessageID = message.SupervisorMessageID
	if !hasPromptContent(prompt) {
		return prompt, &Error{Code: ErrorReconciliation, Message: "durable supervisor prompt has no message content"}
	}
	return prompt, nil
}
