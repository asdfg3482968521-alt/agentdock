package session

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeMultiturnLedgerPersistsOrderingDedupApprovalAndCursor(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	manager := newTestManager(t, root, git, func() string { return "session-f1" })
	record, err := manager.Open(context.Background(), OpenRequest{ProjectID: "d3-f1", RepoRoot: workspace, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	binding := NativeSessionBinding{
		SubagentID: "subagent-f1", AgentDockSessionID: record.SessionID, ClientRequestID: "start-f1",
		OpenCodeSessionID: "ses-f1-root", Workspace: record.Workspace(), RuntimeScope: "session:" + record.SessionID,
		PermissionMode: record.PermissionMode, Provider: "opencode", ExecutionMode: "native-session",
		State: NativeConversationActive, ExecutionState: NativeExecutionIdle,
	}
	if _, err := manager.BindNativeSession(record.SessionID, binding); err != nil {
		t.Fatal(err)
	}

	promptA := json.RawMessage(`{"messageID":"supmsg-a","parts":[{"type":"text","text":"task A"}]}`)
	first, created, err := manager.AdmitNativeSupervisorMessage(record.SessionID, binding.SubagentID, "client-a", "TASK", "supmsg-a", promptA)
	if err != nil || !created {
		t.Fatalf("first admission failed: created=%v err=%v", created, err)
	}
	if first.MessageSeq != 1 || first.SupervisorMessageID != "supmsg-a" || first.State != NativeSupervisorQueued {
		t.Fatalf("unexpected first message: %+v", first)
	}
	duplicate, created, err := manager.AdmitNativeSupervisorMessage(record.SessionID, binding.SubagentID, "client-a", "TASK", "must-not-replace", nil)
	if err != nil || created {
		t.Fatalf("duplicate admission not idempotent: created=%v err=%v", created, err)
	}
	if duplicate.SupervisorMessageID != first.SupervisorMessageID || duplicate.MessageSeq != first.MessageSeq || string(duplicate.PromptPayload) != string(promptA) {
		t.Fatalf("duplicate changed durable identity/payload: first=%+v duplicate=%+v", first, duplicate)
	}

	second, created, err := manager.AdmitNativeSupervisorMessage(record.SessionID, binding.SubagentID, "client-b", "FOLLOWUP", "supmsg-b", json.RawMessage(`{"messageID":"supmsg-b","parts":[{"type":"text","text":"follow-up B"}]}`))
	if err != nil || !created || second.MessageSeq != 2 {
		t.Fatalf("second admission order mismatch: created=%v message=%+v err=%v", created, second, err)
	}
	if _, err := manager.UpdateNativeSupervisorMessage(record.SessionID, binding.SubagentID, first.SupervisorMessageID, func(message *NativeSupervisorMessage) error {
		message.State = NativeSupervisorTerminal
		message.TerminalOutcome = "SUCCEEDED"
		message.NativeMessageID = first.SupervisorMessageID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateNativeSessionBinding(record.SessionID, binding.SubagentID, func(value *NativeSessionBinding) error {
		value.LastOwnedEventCursor = 7
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	approval, created, err := manager.AdmitNativePermissionApproval(record.SessionID, binding.SubagentID, NativePermissionApproval{
		NativePermissionRequestID: "native-perm-1",
		SupervisorMessageID:       second.SupervisorMessageID,
		ObservedEventCursor:       7,
	})
	if err != nil || !created {
		t.Fatalf("approval admission failed: created=%v err=%v", created, err)
	}
	if approval.MessageSeq != second.MessageSeq || approval.ApprovalSeq != 1 || approval.ApprovalRequestID == "" {
		t.Fatalf("approval ordering mismatch: %+v", approval)
	}
	approvalDup, created, err := manager.AdmitNativePermissionApproval(record.SessionID, binding.SubagentID, NativePermissionApproval{
		NativePermissionRequestID: "native-perm-1",
		SupervisorMessageID:       second.SupervisorMessageID,
		ObservedEventCursor:       7,
	})
	if err != nil || created || approvalDup.ApprovalRequestID != approval.ApprovalRequestID {
		t.Fatalf("approval duplicate mismatch: created=%v first=%+v duplicate=%+v err=%v", created, approval, approvalDup, err)
	}
	decided, applied, err := manager.DecideNativePermissionApproval(record.SessionID, binding.SubagentID, approval.ApprovalRequestID, "decision-1", "ALLOW")
	if err != nil || !applied || decided.Decision != "ALLOW" {
		t.Fatalf("approval decision failed: applied=%v approval=%+v err=%v", applied, decided, err)
	}
	decidedAgain, applied, err := manager.DecideNativePermissionApproval(record.SessionID, binding.SubagentID, approval.ApprovalRequestID, "decision-1", "ALLOW")
	if err != nil || applied || decidedAgain.Decision != "ALLOW" {
		t.Fatalf("approval decision retry was not idempotent: applied=%v approval=%+v err=%v", applied, decidedAgain, err)
	}
	if _, _, err := manager.DecideNativePermissionApproval(record.SessionID, binding.SubagentID, approval.ApprovalRequestID, "decision-2", "DENY"); ErrorCode(err) != CodeNativeApprovalConflict {
		t.Fatalf("conflicting approval should be rejected with %s, got %v", CodeNativeApprovalConflict, err)
	}

	restarted, err := NewManager(context.Background(), Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: git,
		Now: func() time.Time { return time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC) }, IDFactory: func() string { return "unused" },
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.NativeSessionBinding(record.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.NextSupervisorMessageSeq != 2 || recovered.NextApprovalSeq != 1 || recovered.LastOwnedEventCursor != 7 {
		t.Fatalf("restart lost sequence/cursor state: %+v", recovered)
	}
	if len(recovered.SupervisorMessages) != 2 || len(recovered.PermissionApprovals) != 1 {
		t.Fatalf("restart lost durable ledgers: messages=%+v approvals=%+v", recovered.SupervisorMessages, recovered.PermissionApprovals)
	}
	if recovered.SupervisorMessages[0].TerminalOutcome != "SUCCEEDED" || !sameJSON(recovered.SupervisorMessages[0].PromptPayload, promptA) {
		t.Fatalf("restart changed durable message payload/outcome: %+v", recovered.SupervisorMessages[0])
	}
	if recovered.PermissionApprovals[0].Decision != "ALLOW" {
		t.Fatalf("restart lost approval decision: %+v", recovered.PermissionApprovals[0])
	}
	third, created, err := restarted.AdmitNativeSupervisorMessage(record.SessionID, binding.SubagentID, "client-c", "CORRECTION", "supmsg-c", json.RawMessage(`{"messageID":"supmsg-c","parts":[{"type":"text","text":"correction C"}]}`))
	if err != nil || !created || third.MessageSeq != 3 {
		t.Fatalf("restart lost next message sequence: created=%v message=%+v err=%v", created, third, err)
	}
}

func sameJSON(left, right json.RawMessage) bool {
	var a, b bytes.Buffer
	if json.Compact(&a, left) != nil || json.Compact(&b, right) != nil {
		return false
	}
	return bytes.Equal(a.Bytes(), b.Bytes())
}
