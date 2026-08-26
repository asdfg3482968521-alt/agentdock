package nativebridge

import (
	"context"
	"encoding/json"
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
