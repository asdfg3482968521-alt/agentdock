package job

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionBindingPersistsAndSurvivesManagerRestart(t *testing.T) {
	root := t.TempDir()
	executor := newFakeExecutor()
	jobID := "11111111-1111-4111-8111-111111111111"
	config := Config{StorageRoot: filepath.Join(root, "jobs"), Limits: DefaultLimits(), IDFactory: func() string { return jobID }}
	manager, err := NewManager(context.Background(), executor, config)
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan(jobID, fakePlan{duration: 5 * time.Millisecond, result: RuntimeExecutionResult{OK: true, ExitCode: 0, Summary: "done"}})
	started, err := manager.Start(StartRequest{
		Workspace: "C:/work/session-a", Tool: "read", Arguments: map[string]any{"filePath": "x"}, PermissionMode: "project",
		SessionID: "session-a", RuntimeScope: "session:session-a", ConcurrencyClass: ConcurrencyRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := waitState(t, manager, started.JobID, StateSucceeded)
	if status.SessionID != "session-a" || status.RuntimeScope != "session:session-a" {
		t.Fatalf("status binding mismatch: %+v", status)
	}

	restarted, err := NewManager(context.Background(), newFakeExecutor(), config)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Status(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SessionID != "session-a" || recovered.RuntimeScope != "session:session-a" || recovered.Workspace != "C:/work/session-a" {
		t.Fatalf("recovered binding mismatch: %+v", recovered)
	}
}

func TestListCanFilterBySessionOwnership(t *testing.T) {
	executor := newFakeExecutor()
	ids := []string{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}
	config := testConfig(t)
	config.IDFactory = func() string { id := ids[0]; ids = ids[1:]; return id }
	manager, err := NewManager(context.Background(), executor, config)
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"session-a", "session-b"} {
		started, err := manager.Start(StartRequest{
			Workspace: "C:/work/" + sessionID, Tool: "read", Arguments: map[string]any{}, PermissionMode: "project",
			SessionID: sessionID, RuntimeScope: "session:" + sessionID, ConcurrencyClass: ConcurrencyRead,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, manager, started.JobID, StateFailed, StateSucceeded)
	}
	items := manager.List(ListFilter{SessionID: "session-a", Limit: 10})
	if len(items) != 1 || items[0].SessionID != "session-a" {
		t.Fatalf("session filter mismatch: %+v", items)
	}
}
