package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	CodeNativeSupervisorConflict = "SESSION_NATIVE_SUPERVISOR_CONFLICT"
	CodeNativeApprovalConflict   = "SESSION_NATIVE_APPROVAL_CONFLICT"
)

type NativeSupervisorMessageState string

const (
	NativeSupervisorQueued      NativeSupervisorMessageState = "QUEUED"
	NativeSupervisorDispatching NativeSupervisorMessageState = "DISPATCHING"
	NativeSupervisorAccepted    NativeSupervisorMessageState = "ACCEPTED"
	NativeSupervisorExecuting   NativeSupervisorMessageState = "EXECUTING"
	NativeSupervisorTerminal    NativeSupervisorMessageState = "TERMINAL"
)

type NativeSupervisorMessage struct {
	ClientMessageID     string                       `json:"client_message_id"`
	SupervisorMessageID string                       `json:"supervisor_message_id"`
	MessageSeq          int64                        `json:"message_seq"`
	Kind                string                       `json:"kind"`
	State               NativeSupervisorMessageState `json:"state"`
	TerminalOutcome     string                       `json:"terminal_outcome,omitempty"`
	NativeMessageID     string                       `json:"native_message_id,omitempty"`
	PromptPayload       json.RawMessage              `json:"prompt_payload,omitempty"`
	CreatedAt           time.Time                    `json:"created_at"`
	UpdatedAt           time.Time                    `json:"updated_at"`
}

type NativePermissionApproval struct {
	ApprovalRequestID         string    `json:"approval_request_id"`
	NativePermissionRequestID string    `json:"native_permission_request_id"`
	SupervisorMessageID       string    `json:"supervisor_message_id"`
	MessageSeq                int64     `json:"message_seq"`
	ApprovalSeq               int64     `json:"approval_seq"`
	ObservedEventCursor       int64     `json:"observed_event_cursor"`
	Decision                  string    `json:"decision,omitempty"`
	ClientDecisionID          string    `json:"client_decision_id,omitempty"`
	NativeDecisionDelivered   bool      `json:"native_decision_delivered,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

func knownNativeSupervisorMessageState(state NativeSupervisorMessageState) bool {
	switch state {
	case NativeSupervisorQueued, NativeSupervisorDispatching, NativeSupervisorAccepted, NativeSupervisorExecuting, NativeSupervisorTerminal:
		return true
	default:
		return false
	}
}

func normalizeNativeMultiturnBinding(binding *NativeSessionBinding) error {
	if binding.SupervisorMessages == nil {
		binding.SupervisorMessages = []NativeSupervisorMessage{}
	}
	if binding.PermissionApprovals == nil {
		binding.PermissionApprovals = []NativePermissionApproval{}
	}
	seenClient := make(map[string]struct{}, len(binding.SupervisorMessages))
	seenSupervisor := make(map[string]struct{}, len(binding.SupervisorMessages))
	var maxMessageSeq int64
	for i := range binding.SupervisorMessages {
		message := &binding.SupervisorMessages[i]
		message.ClientMessageID = strings.TrimSpace(message.ClientMessageID)
		message.SupervisorMessageID = strings.TrimSpace(message.SupervisorMessageID)
		message.Kind = strings.ToUpper(strings.TrimSpace(message.Kind))
		message.TerminalOutcome = strings.ToUpper(strings.TrimSpace(message.TerminalOutcome))
		message.NativeMessageID = strings.TrimSpace(message.NativeMessageID)
		if message.ClientMessageID == "" || message.SupervisorMessageID == "" || message.MessageSeq <= 0 {
			return newError(CodeNativeSupervisorConflict, "native supervisor message identity and positive sequence are required", nil)
		}
		if !knownNativeSupervisorMessageState(message.State) {
			return newError(CodeNativeSupervisorConflict, "invalid native supervisor message state", nil)
		}
		if len(message.PromptPayload) > 0 && !json.Valid(message.PromptPayload) {
			return newError(CodeNativeSupervisorConflict, "native supervisor prompt payload must be valid JSON", nil)
		}
		if _, ok := seenClient[message.ClientMessageID]; ok {
			return newError(CodeNativeSupervisorConflict, "duplicate native supervisor client_message_id", nil)
		}
		if _, ok := seenSupervisor[message.SupervisorMessageID]; ok {
			return newError(CodeNativeSupervisorConflict, "duplicate native supervisor supervisor_message_id", nil)
		}
		seenClient[message.ClientMessageID] = struct{}{}
		seenSupervisor[message.SupervisorMessageID] = struct{}{}
		if message.MessageSeq > maxMessageSeq {
			maxMessageSeq = message.MessageSeq
		}
	}
	if binding.NextSupervisorMessageSeq < maxMessageSeq {
		binding.NextSupervisorMessageSeq = maxMessageSeq
	}

	seenApproval := make(map[string]struct{}, len(binding.PermissionApprovals))
	seenNativeApproval := make(map[string]struct{}, len(binding.PermissionApprovals))
	var maxApprovalSeq int64
	for i := range binding.PermissionApprovals {
		approval := &binding.PermissionApprovals[i]
		approval.ApprovalRequestID = strings.TrimSpace(approval.ApprovalRequestID)
		approval.NativePermissionRequestID = strings.TrimSpace(approval.NativePermissionRequestID)
		approval.SupervisorMessageID = strings.TrimSpace(approval.SupervisorMessageID)
		approval.Decision = strings.ToUpper(strings.TrimSpace(approval.Decision))
		approval.ClientDecisionID = strings.TrimSpace(approval.ClientDecisionID)
		if approval.ApprovalRequestID == "" || approval.NativePermissionRequestID == "" || approval.SupervisorMessageID == "" || approval.MessageSeq <= 0 || approval.ApprovalSeq <= 0 {
			return newError(CodeNativeApprovalConflict, "native approval identity and positive sequences are required", nil)
		}
		if _, ok := seenSupervisor[approval.SupervisorMessageID]; !ok {
			return newError(CodeNativeApprovalConflict, "native approval parent supervisor message is missing", nil)
		}
		if _, ok := seenApproval[approval.ApprovalRequestID]; ok {
			return newError(CodeNativeApprovalConflict, "duplicate native approval_request_id", nil)
		}
		nativeKey := approval.SupervisorMessageID + "\x00" + approval.NativePermissionRequestID
		if _, ok := seenNativeApproval[nativeKey]; ok {
			return newError(CodeNativeApprovalConflict, "duplicate native permission request within supervisor message", nil)
		}
		seenApproval[approval.ApprovalRequestID] = struct{}{}
		seenNativeApproval[nativeKey] = struct{}{}
		if approval.ApprovalSeq > maxApprovalSeq {
			maxApprovalSeq = approval.ApprovalSeq
		}
	}
	if binding.NextApprovalSeq < maxApprovalSeq {
		binding.NextApprovalSeq = maxApprovalSeq
	}
	if binding.LastOwnedEventCursor < 0 {
		return newError(CodeNativeCursorInvalid, "native owned event cursor cannot be negative", nil)
	}
	return nil
}

func (m *Manager) AdmitNativeSupervisorMessage(sessionID, subagentID, clientMessageID, kind, supervisorMessageID string, promptPayload json.RawMessage) (NativeSupervisorMessage, bool, error) {
	clientMessageID = strings.TrimSpace(clientMessageID)
	kind = strings.ToUpper(strings.TrimSpace(kind))
	supervisorMessageID = strings.TrimSpace(supervisorMessageID)
	if clientMessageID == "" || supervisorMessageID == "" {
		return NativeSupervisorMessage{}, false, newError(CodeNativeSupervisorConflict, "client_message_id and supervisor_message_id are required", nil)
	}
	if kind != "TASK" && kind != "FOLLOWUP" && kind != "CORRECTION" {
		return NativeSupervisorMessage{}, false, newError(CodeNativeSupervisorConflict, "native supervisor message kind must be TASK, FOLLOWUP, or CORRECTION", nil)
	}
	if len(promptPayload) > 0 && !json.Valid(promptPayload) {
		return NativeSupervisorMessage{}, false, newError(CodeNativeSupervisorConflict, "native supervisor prompt payload must be valid JSON", nil)
	}

	var result NativeSupervisorMessage
	created := false
	_, err := m.UpdateNativeSessionBinding(sessionID, subagentID, func(binding *NativeSessionBinding) error {
		for _, existing := range binding.SupervisorMessages {
			if existing.ClientMessageID == clientMessageID {
				result = cloneNativeSupervisorMessage(existing)
				return nil
			}
			if existing.SupervisorMessageID == supervisorMessageID {
				return newError(CodeNativeSupervisorConflict, "supervisor_message_id is already bound to another logical message", nil)
			}
		}
		binding.NextSupervisorMessageSeq++
		now := m.config.Now().UTC()
		result = NativeSupervisorMessage{
			ClientMessageID: clientMessageID, SupervisorMessageID: supervisorMessageID,
			MessageSeq: binding.NextSupervisorMessageSeq, Kind: kind, State: NativeSupervisorQueued,
			PromptPayload: append(json.RawMessage(nil), promptPayload...), CreatedAt: now, UpdatedAt: now,
		}
		binding.SupervisorMessages = append(binding.SupervisorMessages, cloneNativeSupervisorMessage(result))
		created = true
		return nil
	})
	if err != nil {
		return NativeSupervisorMessage{}, false, err
	}
	return cloneNativeSupervisorMessage(result), created, nil
}

func (m *Manager) UpdateNativeSupervisorMessage(sessionID, subagentID, supervisorMessageID string, update func(*NativeSupervisorMessage) error) (NativeSupervisorMessage, error) {
	if update == nil {
		return NativeSupervisorMessage{}, newError(CodeNativeSupervisorConflict, "native supervisor message update is required", nil)
	}
	supervisorMessageID = strings.TrimSpace(supervisorMessageID)
	var result NativeSupervisorMessage
	found := false
	_, err := m.UpdateNativeSessionBinding(sessionID, subagentID, func(binding *NativeSessionBinding) error {
		for i := range binding.SupervisorMessages {
			if binding.SupervisorMessages[i].SupervisorMessageID != supervisorMessageID {
				continue
			}
			candidate := cloneNativeSupervisorMessage(binding.SupervisorMessages[i])
			if err := update(&candidate); err != nil {
				return err
			}
			candidate.ClientMessageID = strings.TrimSpace(candidate.ClientMessageID)
			candidate.SupervisorMessageID = strings.TrimSpace(candidate.SupervisorMessageID)
			candidate.Kind = strings.ToUpper(strings.TrimSpace(candidate.Kind))
			candidate.TerminalOutcome = strings.ToUpper(strings.TrimSpace(candidate.TerminalOutcome))
			candidate.NativeMessageID = strings.TrimSpace(candidate.NativeMessageID)
			if candidate.ClientMessageID != binding.SupervisorMessages[i].ClientMessageID || candidate.SupervisorMessageID != binding.SupervisorMessages[i].SupervisorMessageID || candidate.MessageSeq != binding.SupervisorMessages[i].MessageSeq {
				return newError(CodeNativeSupervisorConflict, "native supervisor message identity and sequence are immutable", nil)
			}
			if !knownNativeSupervisorMessageState(candidate.State) {
				return newError(CodeNativeSupervisorConflict, "invalid native supervisor message state", nil)
			}
			if len(candidate.PromptPayload) > 0 && !json.Valid(candidate.PromptPayload) {
				return newError(CodeNativeSupervisorConflict, "native supervisor prompt payload must be valid JSON", nil)
			}
			candidate.UpdatedAt = m.config.Now().UTC()
			binding.SupervisorMessages[i] = cloneNativeSupervisorMessage(candidate)
			result = candidate
			found = true
			return nil
		}
		return newError(CodeNativeSupervisorConflict, "native supervisor message not found", nil)
	})
	if err != nil {
		return NativeSupervisorMessage{}, err
	}
	if !found {
		return NativeSupervisorMessage{}, newError(CodeNativeSupervisorConflict, "native supervisor message not found", nil)
	}
	return cloneNativeSupervisorMessage(result), nil
}

func (m *Manager) AdmitNativePermissionApproval(sessionID, subagentID string, approval NativePermissionApproval) (NativePermissionApproval, bool, error) {
	approval.NativePermissionRequestID = strings.TrimSpace(approval.NativePermissionRequestID)
	approval.SupervisorMessageID = strings.TrimSpace(approval.SupervisorMessageID)
	if approval.NativePermissionRequestID == "" || approval.SupervisorMessageID == "" {
		return NativePermissionApproval{}, false, newError(CodeNativeApprovalConflict, "native permission request id and parent supervisor message id are required", nil)
	}
	var result NativePermissionApproval
	created := false
	_, err := m.UpdateNativeSessionBinding(sessionID, subagentID, func(binding *NativeSessionBinding) error {
		for _, existing := range binding.PermissionApprovals {
			if existing.SupervisorMessageID == approval.SupervisorMessageID && existing.NativePermissionRequestID == approval.NativePermissionRequestID {
				result = existing
				return nil
			}
		}
		var parent *NativeSupervisorMessage
		for i := range binding.SupervisorMessages {
			if binding.SupervisorMessages[i].SupervisorMessageID == approval.SupervisorMessageID {
				parent = &binding.SupervisorMessages[i]
				break
			}
		}
		if parent == nil {
			return newError(CodeNativeApprovalConflict, "parent supervisor message must be durable before approval admission", nil)
		}
		binding.NextApprovalSeq++
		now := m.config.Now().UTC()
		approval.ApprovalSeq = binding.NextApprovalSeq
		approval.MessageSeq = parent.MessageSeq
		approval.ApprovalRequestID = fmt.Sprintf("approval-%s-%06d", strings.TrimSpace(subagentID), approval.ApprovalSeq)
		approval.Decision = ""
		approval.ClientDecisionID = ""
		approval.NativeDecisionDelivered = false
		approval.CreatedAt = now
		approval.UpdatedAt = now
		result = approval
		binding.PermissionApprovals = append(binding.PermissionApprovals, approval)
		created = true
		return nil
	})
	if err != nil {
		return NativePermissionApproval{}, false, err
	}
	return result, created, nil
}

func (m *Manager) DecideNativePermissionApproval(sessionID, subagentID, approvalRequestID, clientDecisionID, decision string) (NativePermissionApproval, bool, error) {
	approvalRequestID = strings.TrimSpace(approvalRequestID)
	clientDecisionID = strings.TrimSpace(clientDecisionID)
	decision = strings.ToUpper(strings.TrimSpace(decision))
	if approvalRequestID == "" || clientDecisionID == "" || (decision != "ALLOW" && decision != "DENY") {
		return NativePermissionApproval{}, false, newError(CodeNativeApprovalConflict, "approval_request_id, client_decision_id, and ALLOW/DENY decision are required", nil)
	}
	var result NativePermissionApproval
	applied := false
	_, err := m.UpdateNativeSessionBinding(sessionID, subagentID, func(binding *NativeSessionBinding) error {
		for i := range binding.PermissionApprovals {
			if binding.PermissionApprovals[i].ApprovalRequestID != approvalRequestID {
				continue
			}
			current := binding.PermissionApprovals[i]
			if current.Decision != "" {
				if current.Decision != decision {
					return newError(CodeNativeApprovalConflict, "conflicting later native approval decision", nil)
				}
				result = current
				return nil
			}
			current.Decision = decision
			current.ClientDecisionID = clientDecisionID
			current.UpdatedAt = m.config.Now().UTC()
			binding.PermissionApprovals[i] = current
			result = current
			applied = true
			return nil
		}
		return newError(CodeNativeApprovalConflict, "native approval request not found", nil)
	})
	if err != nil {
		return NativePermissionApproval{}, false, err
	}
	return result, applied, nil
}

func cloneNativeSupervisorMessage(message NativeSupervisorMessage) NativeSupervisorMessage {
	copy := message
	copy.PromptPayload = append(json.RawMessage(nil), message.PromptPayload...)
	return copy
}
