package nativebridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

func (h *nativeHarness) putMessage(sessionID string, message opencodenative.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages[sessionID] = append(h.messages[sessionID], message)
}

func admitDispatchingSupervisorMessage(t *testing.T, f multiturnFixture, clientID, supervisorID, text string) session.NativeSupervisorMessage {
	t.Helper()
	prompt := opencodenative.TextPrompt(text)
	prompt.MessageID = supervisorID
	payload, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	message, created, err := f.store.AdmitNativeSupervisorMessage(f.record.SessionID, f.binding.SubagentID, clientID, "FOLLOWUP", supervisorID, payload)
	if err != nil || !created {
		t.Fatalf("admit crash-window message: created=%v err=%v", created, err)
	}
	message, err = f.store.UpdateNativeSupervisorMessage(f.record.SessionID, f.binding.SubagentID, supervisorID, func(value *session.NativeSupervisorMessage) error {
		value.State = session.NativeSupervisorDispatching
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestSupervisorRecoveryResendsAbsentDispatchWithSameIdentity(t *testing.T) {
	f := newMultiturnFixture(t)
	message := admitDispatchingSupervisorMessage(t, f, "client-recovery-absent", "supervisor-recovery-absent", "recover me")
	_, bridge := f.restartedBridge(t)

	results, err := bridge.ReconcileSupervisorMessages(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Dispatched || results[0].Message.State != session.NativeSupervisorAccepted {
		t.Fatalf("absent dispatch was not reconciled by one resend: %+v", results)
	}
	if results[0].Message.SupervisorMessageID != message.SupervisorMessageID || results[0].OpenCodeSessionID != f.binding.OpenCodeSessionID {
		t.Fatalf("recovery changed durable/native identity: original=%+v result=%+v root=%s", message, results[0], f.binding.OpenCodeSessionID)
	}
	create, _, async, _ := f.harness.counts()
	_, _, prompts := f.harness.supervisorTrace()
	if create != 1 || async != 1 || len(prompts) != 1 || prompts[0].MessageID != message.SupervisorMessageID {
		t.Fatalf("recovery resend identity/count mismatch: create=%d async=%d prompts=%+v", create, async, prompts)
	}
}

func TestSupervisorRecoveryFindsNativeMessageAndDoesNotResend(t *testing.T) {
	f := newMultiturnFixture(t)
	message := admitDispatchingSupervisorMessage(t, f, "client-recovery-found", "supervisor-recovery-found", "already posted")
	f.harness.putMessage(f.binding.OpenCodeSessionID, opencodenative.Message{
		Info: opencodenative.MessageInfo{ID: message.SupervisorMessageID, SessionID: f.binding.OpenCodeSessionID, Role: "user"},
	})
	_, bridge := f.restartedBridge(t)

	results, err := bridge.ReconcileSupervisorMessages(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Dispatched || results[0].Message.State != session.NativeSupervisorAccepted || results[0].Message.NativeMessageID != message.SupervisorMessageID {
		t.Fatalf("found native message was not accepted without resend: %+v", results)
	}
	create, _, async, _ := f.harness.counts()
	if create != 1 || async != 0 {
		t.Fatalf("found-message reconciliation created/re-sent unexpectedly: create=%d async=%d", create, async)
	}
}

func TestTerminalExecutionAllowsFollowup(t *testing.T) {
	outcomes := []struct {
		name    string
		state   session.NativeExecutionState
		outcome string
	}{
		{name: "success", state: session.NativeExecutionSucceeded, outcome: "SUCCEEDED"},
		{name: "failure", state: session.NativeExecutionFailed, outcome: "FAILED"},
		{name: "cancel", state: session.NativeExecutionCancelled, outcome: "CANCELLED"},
		{name: "timeout", state: session.NativeExecutionTimedOut, outcome: "TIMED_OUT"},
		{name: "interrupted", state: session.NativeExecutionInterrupted, outcome: "INTERRUPTED"},
	}
	for _, tc := range outcomes {
		t.Run(tc.name, func(t *testing.T) {
			f := newMultiturnFixture(t)
			first, err := f.bridge.SubmitSupervisorMessage(context.Background(), f.request("client-first", "TASK", "first turn"))
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := f.bridge.MarkSupervisorExecutionTerminal(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), first.Message.SupervisorMessageID, tc.outcome)
			if err != nil {
				t.Fatal(err)
			}
			if terminal.State != session.NativeSupervisorTerminal || terminal.TerminalOutcome != tc.outcome {
				t.Fatalf("message terminal state mismatch: %+v", terminal)
			}
			binding, err := f.store.NativeSessionBinding(f.record.SessionID, f.binding.SubagentID)
			if err != nil {
				t.Fatal(err)
			}
			if binding.State != session.NativeConversationActive || binding.ExecutionState != tc.state {
				t.Fatalf("execution terminal state closed/replaced conversation: %+v", binding)
			}
			followup, err := f.bridge.SubmitSupervisorMessage(context.Background(), f.request("client-followup", "FOLLOWUP", "continue after terminal"))
			if err != nil {
				t.Fatal(err)
			}
			if followup.Message.MessageSeq != first.Message.MessageSeq+1 || followup.OpenCodeSessionID != f.binding.OpenCodeSessionID {
				t.Fatalf("follow-up did not continue same durable conversation: first=%+v followup=%+v root=%s", first, followup, f.binding.OpenCodeSessionID)
			}
		})
	}
}
