package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePlan struct {
	acceptDelay time.Duration
	duration    time.Duration
	outputs     []RuntimeOutputEvent
	result      RuntimeExecutionResult
}

type fakeExecutor struct {
	mu                sync.Mutex
	plans             map[string]fakePlan
	cancelCalls       []RuntimeCancelRequest
	started           []string
	active            int
	maxActive         int
	activeByWorkspace map[string]int
	maxByWorkspace    map[string]int
	cancels           map[string]context.CancelFunc
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		plans:             map[string]fakePlan{},
		activeByWorkspace: map[string]int{},
		maxByWorkspace:    map[string]int{},
		cancels:           map[string]context.CancelFunc{},
	}
}

func (f *fakeExecutor) setPlan(jobID string, plan fakePlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[jobID] = plan
}

func (f *fakeExecutor) Execute(ctx context.Context, request RuntimeExecutionRequest) (RuntimeExecutionHandle, error) {
	f.mu.Lock()
	plan := f.plans[request.JobID]
	f.mu.Unlock()
	if plan.acceptDelay > 0 {
		select {
		case <-time.After(plan.acceptDelay):
		case <-ctx.Done():
			return RuntimeExecutionHandle{}, ctx.Err()
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	completion := make(chan RuntimeExecutionResult, 1)
	f.mu.Lock()
	f.started = append(f.started, request.JobID)
	f.cancels[request.JobID] = cancel
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.activeByWorkspace[request.Workspace]++
	if f.activeByWorkspace[request.Workspace] > f.maxByWorkspace[request.Workspace] {
		f.maxByWorkspace[request.Workspace] = f.activeByWorkspace[request.Workspace]
	}
	f.mu.Unlock()

	go func() {
		defer func() {
			f.mu.Lock()
			f.active--
			f.activeByWorkspace[request.Workspace]--
			delete(f.cancels, request.JobID)
			f.mu.Unlock()
		}()
		start := time.Now()
		for _, out := range plan.outputs {
			wait := out.After
			if wait <= 0 {
				continue
			}
			timer := time.NewTimer(wait - time.Since(start))
			select {
			case <-timer.C:
				_ = request.OnOutput(RuntimeOutputEvent{Stream: out.Stream, Text: out.Text})
			case <-runCtx.Done():
				timer.Stop()
				completion <- RuntimeExecutionResult{Err: context.Canceled}
				close(completion)
				return
			}
		}
		remaining := plan.duration - time.Since(start)
		if remaining < 0 {
			remaining = 0
		}
		timer := time.NewTimer(remaining)
		select {
		case <-timer.C:
			completion <- plan.result
		case <-runCtx.Done():
			timer.Stop()
			completion <- RuntimeExecutionResult{Err: context.Canceled}
		}
		close(completion)
	}()

	return RuntimeExecutionHandle{RuntimeID: "fake-" + request.JobID, Completion: completion}, nil
}

func (f *fakeExecutor) Cancel(_ context.Context, request RuntimeCancelRequest) error {
	f.mu.Lock()
	f.cancelCalls = append(f.cancelCalls, request)
	cancel := f.cancels[request.JobID]
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{StorageRoot: t.TempDir(), Limits: DefaultLimits()}
}

func waitState(t *testing.T, manager *Manager, jobID string, states ...State) StatusResult {
	t.Helper()
	want := map[State]bool{}
	for _, state := range states {
		want[state] = true
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(jobID)
		if err == nil && want[status.State] {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := manager.Status(jobID)
	t.Fatalf("timeout waiting for %v, last=%s", states, status.State)
	return StatusResult{}
}

func TestStateTransitionsRejectInvalid(t *testing.T) {
	if !CanTransition(StateQueued, StateStarting) {
		t.Fatal("QUEUED -> STARTING should be valid")
	}
	if CanTransition(StateSucceeded, StateRunning) {
		t.Fatal("terminal state must reject transition")
	}
	if err := ValidateTransition(StateRunning, StateQueued); err == nil {
		t.Fatal("expected invalid transition error")
	}
}

func TestStartReturnsBeforeRuntimeAcceptanceAndSucceeds(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return "11111111-1111-4111-8111-111111111111" }
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan("11111111-1111-4111-8111-111111111111", fakePlan{
		acceptDelay: 250 * time.Millisecond,
		duration:    120 * time.Millisecond,
		result:      RuntimeExecutionResult{OK: true, ExitCode: 0, Summary: "done"},
	})
	startedAt := time.Now()
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{"cmd": "echo ok"}, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if latency := time.Since(startedAt); latency >= 200*time.Millisecond {
		t.Fatalf("start latency too high: %v", latency)
	}
	if started.State != StateQueued {
		t.Fatalf("state=%s", started.State)
	}
	status := waitState(t, manager, started.JobID, StateSucceeded)
	if status.ResultSummary != "done" || status.ExitCode == nil || *status.ExitCode != 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestCancelIsIdempotentAndUsesExecutor(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return "22222222-2222-4222-8222-222222222222" }
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan("22222222-2222-4222-8222-222222222222", fakePlan{duration: 3 * time.Second})
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{"cmd": "sleep"}, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, started.JobID, StateRunning)
	if _, err := manager.Cancel(context.Background(), started.JobID, "user token=secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(context.Background(), started.JobID, "again"); err != nil {
		t.Fatal(err)
	}
	status := waitState(t, manager, started.JobID, StateCancelled)
	if strings.Contains(status.CancelReason, "secret") {
		t.Fatalf("cancel reason leaked secret: %q", status.CancelReason)
	}
	executor.mu.Lock()
	calls := len(executor.cancelCalls)
	executor.mu.Unlock()
	if calls != 1 {
		t.Fatalf("cancel calls=%d want 1", calls)
	}
}

func TestTimeoutCancelsBeforeTimedOut(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return "33333333-3333-4333-8333-333333333333" }
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan("33333333-3333-4333-8333-333333333333", fakePlan{duration: time.Second})
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{"cmd": "slow"}, PermissionMode: "project", Timeout: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	status := waitState(t, manager, started.JobID, StateTimedOut)
	if status.CancelReason != "job timeout" {
		t.Fatalf("cancel reason=%q", status.CancelReason)
	}
	executor.mu.Lock()
	calls := len(executor.cancelCalls)
	executor.mu.Unlock()
	if calls != 1 {
		t.Fatalf("cancel calls=%d", calls)
	}
}

func TestIncrementalOutputCursorAndRedaction(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return "44444444-4444-4444-8444-444444444444" }
	cfg.Limits.MaxOutputBytesPerResponse = 64
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan("44444444-4444-4444-8444-444444444444", fakePlan{
		duration: 80 * time.Millisecond,
		outputs:  []RuntimeOutputEvent{{After: 10 * time.Millisecond, Stream: "stdout", Text: "one\n"}, {After: 25 * time.Millisecond, Stream: "stderr", Text: "Authorization: Bearer super-secret\n"}},
		result:   RuntimeExecutionResult{OK: true, ExitCode: 0, Summary: "done token=result-secret"},
	})
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{"password": "argument-secret"}, Metadata: map[string]any{"api_key": "metadata-secret"}, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, _ := manager.Status(started.JobID)
		if status.OutputCursor >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	first, err := manager.Output(OutputRequest{JobID: started.JobID, Cursor: 0, LimitBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Chunks) == 0 || first.NextCursor <= 0 {
		t.Fatalf("unexpected first output: %+v", first)
	}
	joined := ""
	for _, chunk := range first.Chunks {
		joined += chunk.Text
	}
	if strings.Contains(joined, "super-secret") {
		t.Fatalf("output leaked secret: %q", joined)
	}
	waitState(t, manager, started.JobID, StateSucceeded)
	rest, err := manager.Output(OutputRequest{JobID: started.JobID, Cursor: first.NextCursor, LimitBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	if rest.NextCursor < first.NextCursor {
		t.Fatalf("cursor regressed")
	}
	metadata, err := os.ReadFile(filepath.Join(cfg.StorageRoot, started.JobID, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"argument-secret", "metadata-secret", "result-secret"} {
		if strings.Contains(string(metadata), secret) {
			t.Fatalf("metadata leaked %s", secret)
		}
	}
}

func TestRecoveryMarksRunningInterruptedAndRecoversCursorFromSpool(t *testing.T) {
	cfg := testConfig(t)
	jobID := "55555555-5555-4555-8555-555555555555"
	jobDir := filepath.Join(cfg.StorageRoot, jobID)
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := Record{JobID: jobID, Workspace: t.TempDir(), Tool: "bash", PermissionMode: "project", CreatedAt: time.Now().Add(-time.Minute), StartedAt: ptrTime(time.Now().Add(-30 * time.Second)), State: StateRunning, ConcurrencyClass: ConcurrencyWrite}
	body, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(jobDir, "metadata.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	events := "{\"cursor\":0,\"stream\":\"stdout\",\"text\":\"persist-one\\n\",\"timestamp\":\"2026-08-23T00:00:00Z\",\"bytes\":12}\n" +
		"{\"cursor\":1,\"stream\":\"stderr\",\"text\":\"persist-two\\n\",\"timestamp\":\"2026-08-23T00:00:01Z\",\"bytes\":12}\n"
	if err := os.WriteFile(filepath.Join(jobDir, "events.ndjson"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(context.Background(), newFakeExecutor(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateInterrupted {
		t.Fatalf("state=%s", status.State)
	}
	if status.OutputCursor != 2 {
		t.Fatalf("cursor=%d want 2", status.OutputCursor)
	}
	out, err := manager.Output(OutputRequest{JobID: jobID, Cursor: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Chunks) != 2 || !out.Complete {
		t.Fatalf("output=%+v", out)
	}
}

func TestConcurrencyGlobalWorkspaceAndWriteSerialization(t *testing.T) {
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.Limits.MaxConcurrentJobs = 3
	cfg.Limits.MaxConcurrentPerWorkspace = 2
	cfg.Limits.MaxConcurrentWritePerWorkspace = 1
	ids := []string{"61111111-1111-4111-8111-111111111111", "62222222-2222-4222-8222-222222222222", "63333333-3333-4333-8333-333333333333"}
	idx := 0
	cfg.IDFactory = func() string { id := ids[idx]; idx++; return id }
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wa, wb := t.TempDir(), t.TempDir()
	for _, id := range ids {
		executor.setPlan(id, fakePlan{duration: 80 * time.Millisecond, result: RuntimeExecutionResult{OK: true, ExitCode: 0}})
	}
	j1, _ := manager.Start(StartRequest{Workspace: wa, Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project", ConcurrencyClass: ConcurrencyWrite})
	j2, _ := manager.Start(StartRequest{Workspace: wa, Tool: "read", Arguments: map[string]any{}, PermissionMode: "project", ConcurrencyClass: ConcurrencyRead})
	j3, _ := manager.Start(StartRequest{Workspace: wb, Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project", ConcurrencyClass: ConcurrencyWrite})
	for _, id := range []string{j1.JobID, j2.JobID, j3.JobID} {
		waitState(t, manager, id, StateSucceeded)
	}
	executor.mu.Lock()
	maxActive := executor.maxActive
	maxWA := executor.maxByWorkspace[wa]
	executor.mu.Unlock()
	if maxActive > 3 || maxWA > 2 {
		t.Fatalf("limits violated global=%d workspace=%d", maxActive, maxWA)
	}
}

func TestInvalidJobIDPathTraversalRejected(t *testing.T) {
	manager, err := NewManager(context.Background(), newFakeExecutor(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Status("..\\..\\secrets"); err == nil {
		t.Fatal("expected invalid job id")
	}
}

func ptrTime(v time.Time) *time.Time { return &v }
