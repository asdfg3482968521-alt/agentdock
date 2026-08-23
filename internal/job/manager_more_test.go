package job

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFailureBecomesFailed(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return "71111111-1111-4111-8111-111111111111" }
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan("71111111-1111-4111-8111-111111111111", fakePlan{duration: 20 * time.Millisecond, result: RuntimeExecutionResult{OK: false, ExitCode: 9, Summary: "failed"}})
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	status := waitState(t, manager, started.JobID, StateFailed)
	if status.ExitCode == nil || *status.ExitCode != 9 {
		t.Fatalf("status=%+v", status)
	}
}

func TestOutputResponseTruncationAdvancesByCursor(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return "72222222-2222-4222-8222-222222222222" }
	cfg.Limits.MaxOutputBytesPerResponse = 32
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan("72222222-2222-4222-8222-222222222222", fakePlan{duration: 40 * time.Millisecond, outputs: []RuntimeOutputEvent{{After: 5 * time.Millisecond, Stream: "stdout", Text: "alpha\n"}, {After: 10 * time.Millisecond, Stream: "stdout", Text: "beta\n"}}, result: RuntimeExecutionResult{OK: true}})
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, started.JobID, StateSucceeded)
	first, err := manager.Output(OutputRequest{JobID: started.JobID, Cursor: 0, LimitBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || first.NextCursor <= 0 {
		t.Fatalf("first=%+v", first)
	}
	second, err := manager.Output(OutputRequest{JobID: started.JobID, Cursor: first.NextCursor, LimitBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	if second.NextCursor <= first.NextCursor {
		t.Fatalf("cursor did not advance: first=%+v second=%+v", first, second)
	}
}

func TestRetentionKeepsNewestCompletedJobs(t *testing.T) {
	cfg := testConfig(t)
	cfg.Limits.Retention = time.Hour
	cfg.Limits.RetentionMaxCompletedJobs = 2
	store, err := newPersistence(cfg.StorageRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ids := []string{"73333333-3333-4333-8333-333333333331", "73333333-3333-4333-8333-333333333332", "73333333-3333-4333-8333-333333333333"}
	for i, id := range ids {
		finished := now.Add(-time.Duration(i+1) * 10 * time.Minute)
		record := Record{JobID: id, Workspace: t.TempDir(), Tool: "read", PermissionMode: "project", CreatedAt: finished.Add(-time.Minute), FinishedAt: &finished, State: StateSucceeded, ConcurrencyClass: ConcurrencyRead}
		if err := store.save(record); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManager(context.Background(), newFakeExecutor(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.List(ListFilter{Limit: 10})) != 2 {
		t.Fatalf("retained=%d want 2", len(manager.List(ListFilter{Limit: 10})))
	}
	if _, err := manager.Status(ids[2]); err == nil {
		t.Fatal("oldest completed job should have been pruned")
	}
}

func TestGlobalConcurrencyActuallyQueuesBeyondLimit(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.Limits.MaxConcurrentJobs = 2
	cfg.Limits.MaxConcurrentPerWorkspace = 2
	ids := []string{"74444444-4444-4444-8444-444444444441", "74444444-4444-4444-8444-444444444442", "74444444-4444-4444-8444-444444444443", "74444444-4444-4444-8444-444444444444"}
	i := 0
	cfg.IDFactory = func() string { id := ids[i]; i++; return id }
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []string
	for _, id := range ids {
		executor.setPlan(id, fakePlan{duration: 80 * time.Millisecond, result: RuntimeExecutionResult{OK: true}})
		started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "read", Arguments: map[string]any{}, PermissionMode: "project", ConcurrencyClass: ConcurrencyRead})
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, started.JobID)
	}
	for _, id := range jobs {
		waitState(t, manager, id, StateSucceeded)
	}
	executor.mu.Lock()
	maxActive := executor.maxActive
	executor.mu.Unlock()
	if maxActive != 2 {
		t.Fatalf("max active=%d want exactly 2", maxActive)
	}
}

func TestSameWorkspaceWriteJobsSerialize(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	ids := []string{"75555555-5555-4555-8555-555555555551", "75555555-5555-4555-8555-555555555552"}
	i := 0
	cfg.IDFactory = func() string { id := ids[i]; i++; return id }
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	executor.setPlan(ids[0], fakePlan{duration: 120 * time.Millisecond, result: RuntimeExecutionResult{OK: true}})
	executor.setPlan(ids[1], fakePlan{duration: 10 * time.Millisecond, result: RuntimeExecutionResult{OK: true}})
	first, err := manager.Start(StartRequest{Workspace: workspace, Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project", ConcurrencyClass: ConcurrencyDestructive})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(StartRequest{Workspace: workspace, Tool: "write", Arguments: map[string]any{}, PermissionMode: "project", ConcurrencyClass: ConcurrencyWrite})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, first.JobID, StateRunning)
	time.Sleep(20 * time.Millisecond)
	secondStatus, err := manager.Status(second.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if secondStatus.State != StateQueued {
		t.Fatalf("second write state=%s want QUEUED while first write runs", secondStatus.State)
	}
	waitState(t, manager, first.JobID, StateSucceeded)
	waitState(t, manager, second.JobID, StateSucceeded)
}

func TestManagerLifecycleIsDetachedFromConstructionContext(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return "76666666-6666-4666-8666-666666666666" }
	ctx, cancel := context.WithCancel(context.Background())
	manager, err := NewManager(ctx, executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	executor.setPlan("76666666-6666-4666-8666-666666666666", fakePlan{duration: 40 * time.Millisecond, result: RuntimeExecutionResult{OK: true, Summary: "survived"}})
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	status := waitState(t, manager, started.JobID, StateSucceeded)
	if status.ResultSummary != "survived" {
		t.Fatalf("status=%+v", status)
	}
}

func TestMetadataPersistenceConcurrentWritersRemainAtomic(t *testing.T) {
	root := t.TempDir()
	store, err := newPersistence(root)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "77777777-7777-4777-8777-777777777777"
	base := Record{JobID: jobID, Workspace: t.TempDir(), Tool: "read", PermissionMode: "project", CreatedAt: time.Now().UTC(), State: StateSucceeded, ConcurrencyClass: ConcurrencyRead}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			record := base
			record.ResultSummary = fmt.Sprintf("writer-%d", index)
			if err := store.save(record); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, jobID, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Record
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("final metadata is not valid JSON: %v\n%s", err, body)
	}
	if decoded.JobID != jobID {
		t.Fatalf("final job id=%q", decoded.JobID)
	}
	leftovers, err := filepath.Glob(filepath.Join(root, jobID, ".agentdock-atomic-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("atomic temp leftovers: %v", leftovers)
	}
}
