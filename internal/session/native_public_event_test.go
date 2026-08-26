package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativePublicEventCursorDurableBoundedAndPaged(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	manager := newTestManager(t, root, git, func() string { return "session-events" })
	record, err := manager.Open(context.Background(), OpenRequest{ProjectID: "d3", RepoRoot: repo, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	binding := NativeSessionBinding{SubagentID: "subagent-events", AgentDockSessionID: record.SessionID, ClientRequestID: "request-events", OpenCodeSessionID: "ses-events", Workspace: record.Workspace(), RuntimeScope: "session:" + record.SessionID, PermissionMode: record.PermissionMode, State: NativeConversationActive, ExecutionState: NativeExecutionIdle}
	if _, err := manager.BindNativeSession(record.SessionID, binding); err != nil {
		t.Fatal(err)
	}

	total := NativePublicEventRetention + 5
	for i := 0; i < total; i++ {
		payload, _ := json.Marshal(map[string]any{"i": i})
		got, err := manager.AppendNativePublicEvent(record.SessionID, binding.SubagentID, NativePublicEvent{Kind: "tool_result", NativeEventID: "evt", NativeEventSeq: int64(i + 100), NativeType: "message.part.updated", NativeSessionID: binding.OpenCodeSessionID, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		if got.Seq != int64(i+1) {
			t.Fatalf("seq=%d want=%d", got.Seq, i+1)
		}
	}
	page, err := manager.NativePublicEvents(record.SessionID, binding.SubagentID, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Events) != 3 || page.Events[0].Seq != 6 || page.LatestCursor != int64(total) {
		t.Fatalf("page=%+v", page)
	}
	restarted, err := NewManager(context.Background(), Config{StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: git, IDFactory: func() string { return "unused" }})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := restarted.NativePublicEvents(record.SessionID, binding.SubagentID, int64(total-2), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Events) != 2 || persisted.Events[0].Seq != int64(total-1) || persisted.NextCursor != int64(total) {
		t.Fatalf("persisted=%+v", persisted)
	}
}
