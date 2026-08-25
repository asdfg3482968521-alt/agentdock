package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeSessionBindingDurableIdentityAndUniqueness(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	manager := newTestManager(t, root, git, func() string { return "session-native-a" })
	record, err := manager.Open(context.Background(), OpenRequest{ProjectID: "d3", RepoRoot: repo, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}

	binding := NativeSessionBinding{
		SubagentID: "subagent-a", AgentDockSessionID: record.SessionID, ClientRequestID: "request-a",
		OpenCodeSessionID: "ses_native_exact_001", Workspace: record.Workspace(), RuntimeScope: "session:" + record.SessionID,
		PermissionMode: record.PermissionMode, Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session",
		State: NativeConversationActive, ExecutionState: NativeExecutionIdle,
		ResultRefs:         []NativeResultRef{{Kind: "message", Ref: "msg-1"}},
		CurrentExecutionID: "exec-1", LastNativeEventID: "evt-1", LastNativeEventSeq: 7,
		LastNativeMessageID: "msg-1", LastNativePartID: "part-1", PendingSupervisorMessageID: "supervisor-1",
		NativeRuntimeSHA: "runtime-sha", NativeServerIdentity: "127.0.0.1:test",
	}
	stored, err := manager.BindNativeSession(record.SessionID, binding)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("native id changed: %q", stored.OpenCodeSessionID)
	}
	stored.ResultRefs[0].Ref = "mutated"
	got, err := manager.NativeSessionBinding(record.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultRefs[0].Ref != "msg-1" {
		t.Fatalf("binding clone leaked mutation: %+v", got.ResultRefs)
	}

	byRequest, ok, err := manager.NativeSessionBindingByRequest(record.SessionID, binding.ClientRequestID)
	if err != nil || !ok || byRequest.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("request lookup mismatch: ok=%v err=%v got=%+v", ok, err, byRequest)
	}

	duplicateRequest := binding
	duplicateRequest.SubagentID = "subagent-b"
	duplicateRequest.OpenCodeSessionID = "ses_native_other"
	if _, err := manager.BindNativeSession(record.SessionID, duplicateRequest); ErrorCode(err) != CodeNativeBindingConflict {
		t.Fatalf("duplicate request should conflict: %v", err)
	}
	duplicateNative := binding
	duplicateNative.SubagentID = "subagent-c"
	duplicateNative.ClientRequestID = "request-c"
	if _, err := manager.BindNativeSession(record.SessionID, duplicateNative); ErrorCode(err) != CodeNativeBindingConflict {
		t.Fatalf("duplicate native id should conflict: %v", err)
	}

	badWorkspace := binding
	badWorkspace.SubagentID, badWorkspace.ClientRequestID, badWorkspace.OpenCodeSessionID = "subagent-d", "request-d", "ses-d"
	badWorkspace.Workspace = filepath.Join(root, "other")
	if _, err := manager.BindNativeSession(record.SessionID, badWorkspace); ErrorCode(err) != CodeWorkspaceMismatch {
		t.Fatalf("workspace mismatch should fail: %v", err)
	}
	badOwner := binding
	badOwner.SubagentID, badOwner.ClientRequestID, badOwner.OpenCodeSessionID = "subagent-e", "request-e", "ses-e"
	badOwner.AgentDockSessionID = "session-other"
	if _, err := manager.BindNativeSession(record.SessionID, badOwner); ErrorCode(err) != CodeNativeBindingConflict {
		t.Fatalf("ownership mismatch should fail: %v", err)
	}

	metadataPath := filepath.Join(root, "sessions", record.SessionID, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	bindings, ok := raw["native_session_bindings"].(map[string]any)
	if !ok || bindings[binding.SubagentID] == nil {
		t.Fatalf("native mapping missing from D1 metadata: %s", data)
	}

	restarted, err := NewManager(context.Background(), Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: git,
		Now: func() time.Time { return time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC) }, IDFactory: func() string { return "unused" },
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.NativeSessionBinding(record.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.OpenCodeSessionID != binding.OpenCodeSessionID || recovered.ClientRequestID != binding.ClientRequestID || recovered.NativeRuntimeSHA != binding.NativeRuntimeSHA {
		t.Fatalf("restart changed durable native identity: %+v", recovered)
	}
	lost, err := restarted.MarkNativeSessionLost(record.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if lost.State != NativeConversationLost || lost.TerminalState != "LOST" {
		t.Fatalf("LOST state not persisted: %+v", lost)
	}
}
