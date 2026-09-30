package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	jobruntime "github.com/uvwt/agentdock/internal/job"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

type blockingSessionJobExecutor struct {
	mu         sync.Mutex
	completion map[string]chan jobruntime.RuntimeExecutionResult
	cancelled  map[string]bool
}

func newBlockingSessionJobExecutor() *blockingSessionJobExecutor {
	return &blockingSessionJobExecutor{completion: map[string]chan jobruntime.RuntimeExecutionResult{}, cancelled: map[string]bool{}}
}

func (e *blockingSessionJobExecutor) Execute(_ context.Context, request jobruntime.RuntimeExecutionRequest) (jobruntime.RuntimeExecutionHandle, error) {
	e.mu.Lock()
	ch := make(chan jobruntime.RuntimeExecutionResult, 1)
	e.completion[request.JobID] = ch
	e.mu.Unlock()
	return jobruntime.RuntimeExecutionHandle{RuntimeID: "test-" + request.JobID, Completion: ch}, nil
}

func (e *blockingSessionJobExecutor) Cancel(_ context.Context, request jobruntime.RuntimeCancelRequest) error {
	e.mu.Lock()
	ch := e.completion[request.JobID]
	if ch == nil {
		e.mu.Unlock()
		return errors.New("job not running")
	}
	if !e.cancelled[request.JobID] {
		e.cancelled[request.JobID] = true
		ch <- jobruntime.RuntimeExecutionResult{Err: context.Canceled}
		close(ch)
	}
	e.mu.Unlock()
	return nil
}

func TestResolveD1SessionRouteRejectsMismatchAndReadonlyMutation(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	aDir := filepath.Join(root, "a")
	bDir := filepath.Join(root, "b")
	roDir := filepath.Join(root, "ro")
	for _, dir := range []string{aDir, bDir, roDir} {
		if err := ensureDir(dir); err != nil {
			t.Fatal(err)
		}
	}
	a := mustOpenSession(t, rt, map[string]any{"project_id": "a", "repo_root": aDir})
	_ = mustOpenSession(t, rt, map[string]any{"project_id": "b", "repo_root": bDir})
	ro := mustOpenSession(t, rt, map[string]any{"project_id": "ro", "repo_root": roDir, "isolation_mode": "shared-readonly"})

	if _, err := rt.resolveD1SessionRoute(a.SessionID, bDir, false); toolErrorCode(err) != sessionruntime.CodeWorkspaceMismatch {
		t.Fatalf("expected workspace mismatch, got %v", err)
	}
	if _, err := rt.resolveD1SessionRoute(ro.SessionID, "", true); toolErrorCode(err) != sessionruntime.CodeWriteDenied {
		t.Fatalf("expected write denied, got %v", err)
	}
	if got, err := rt.resolveD1SessionRoute(a.SessionID, "", false); err != nil || got.Workspace() != a.Workspace() {
		t.Fatalf("session route failed: got=%+v err=%v", got, err)
	}
}

func TestJobToolsBindAndEnforceSessionOwnership(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	aDir := filepath.Join(root, "a")
	bDir := filepath.Join(root, "b")
	for _, dir := range []string{aDir, bDir} {
		if err := ensureDir(dir); err != nil {
			t.Fatal(err)
		}
	}
	a := mustOpenSession(t, rt, map[string]any{"project_id": "a", "repo_root": aDir})
	b := mustOpenSession(t, rt, map[string]any{"project_id": "b", "repo_root": bDir})
	executor := newBlockingSessionJobExecutor()
	manager, err := jobruntime.NewManager(context.Background(), executor, jobruntime.Config{StorageRoot: filepath.Join(root, "jobs-test"), Limits: jobruntime.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	rt.jobs = manager

	started, err := rt.Call(context.Background(), "job_start", map[string]any{
		"session_id": a.SessionID, "tool": "read", "arguments": map[string]any{"filePath": "foo.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := started["job_id"].(string)
	if jobID == "" {
		t.Fatalf("job_start missing job_id: %+v", started)
	}
	waitAppJobState(t, manager, jobID, jobruntime.StateRunning)
	status, err := rt.Call(context.Background(), "job_status", map[string]any{"session_id": a.SessionID, "job_id": jobID})
	if err != nil {
		t.Fatal(err)
	}
	if status["session_id"] != a.SessionID || status["workspace"] != a.Workspace() || status["runtime_scope"] != d1RuntimeScope(a.SessionID) {
		t.Fatalf("job binding mismatch: %+v", status)
	}
	if _, err := rt.Call(context.Background(), "job_status", map[string]any{"session_id": b.SessionID, "job_id": jobID}); toolErrorCode(err) != sessionruntime.CodeJobOwnership {
		t.Fatalf("cross-session status must fail ownership: %v", err)
	}
	if _, err := rt.Call(context.Background(), "job_output", map[string]any{"session_id": b.SessionID, "job_id": jobID}); toolErrorCode(err) != sessionruntime.CodeJobOwnership {
		t.Fatalf("cross-session output must fail ownership: %v", err)
	}
	if _, err := rt.Call(context.Background(), "job_cancel", map[string]any{"session_id": b.SessionID, "job_id": jobID}); toolErrorCode(err) != sessionruntime.CodeJobOwnership {
		t.Fatalf("cross-session cancel must fail ownership: %v", err)
	}
	listed, err := rt.Call(context.Background(), "job_list", map[string]any{"session_id": a.SessionID, "limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	if listed["count"] != 1 {
		t.Fatalf("session job_list mismatch: %+v", listed)
	}
	sessionStatus, err := rt.Call(context.Background(), "session_status", map[string]any{"session_id": a.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	sessionMap := sessionStatus["session"].(map[string]any)
	jobIDs := sessionMap["job_ids"].([]any)
	if len(jobIDs) != 1 || jobIDs[0] != jobID {
		t.Fatalf("session did not persist owned job id: %+v", sessionMap)
	}
	if _, err := manager.Cancel(context.Background(), jobID, "test cleanup"); err != nil {
		t.Fatal(err)
	}
	waitAppJobState(t, manager, jobID, jobruntime.StateCancelled)
}

func TestSessionCloseCancelsOwnedJobWithoutAffectingOtherSession(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	aDir := filepath.Join(root, "a")
	bDir := filepath.Join(root, "b")
	for _, dir := range []string{aDir, bDir} {
		if err := ensureDir(dir); err != nil {
			t.Fatal(err)
		}
	}
	a := mustOpenSession(t, rt, map[string]any{"project_id": "a", "repo_root": aDir})
	b := mustOpenSession(t, rt, map[string]any{"project_id": "b", "repo_root": bDir})
	executor := newBlockingSessionJobExecutor()
	manager, err := jobruntime.NewManager(context.Background(), executor, jobruntime.Config{StorageRoot: filepath.Join(root, "jobs-close"), Limits: jobruntime.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	rt.jobs = manager

	startJob := func(sessionID string) string {
		result, err := rt.Call(context.Background(), "job_start", map[string]any{"session_id": sessionID, "tool": "bash", "arguments": map[string]any{"command": "long"}})
		if err != nil {
			t.Fatal(err)
		}
		id := result["job_id"].(string)
		waitAppJobState(t, manager, id, jobruntime.StateRunning)
		return id
	}
	jobA := startJob(a.SessionID)
	jobB := startJob(b.SessionID)
	closed, err := rt.Call(context.Background(), "session_close", map[string]any{"session_id": a.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, _ := closed["jobs_cancelled"].([]any)
	if len(cancelled) != 1 || cancelled[0] != jobA {
		t.Fatalf("close did not report only A job cancellation: %+v", closed)
	}
	if got, _ := manager.Status(jobA); got.State != jobruntime.StateCancelled {
		t.Fatalf("job A state=%s", got.State)
	}
	if got, _ := manager.Status(jobB); got.State != jobruntime.StateRunning {
		t.Fatalf("job B must remain RUNNING, got %s", got.State)
	}
	bStatus, err := rt.Call(context.Background(), "session_status", map[string]any{"session_id": b.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if bStatus["session"].(map[string]any)["state"] != "ACTIVE" {
		t.Fatalf("session B changed: %+v", bStatus)
	}
	if _, err := rt.Call(context.Background(), "job_cancel", map[string]any{"session_id": b.SessionID, "job_id": jobB}); err != nil {
		t.Fatal(err)
	}
}

func mustOpenSession(t *testing.T, rt *Runtime, args map[string]any) sessionruntime.Record {
	t.Helper()
	result, err := rt.Call(context.Background(), "session_open", args)
	if err != nil {
		t.Fatal(err)
	}
	body, err := jsonMarshal(result["session"])
	if err != nil {
		t.Fatal(err)
	}
	var record sessionruntime.Record
	if err := jsonUnmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func waitAppJobState(t *testing.T, manager *jobruntime.Manager, jobID string, want jobruntime.State) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(jobID)
		if err == nil && status.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := manager.Status(jobID)
	t.Fatalf("timeout waiting job %s for %s; last=%s", jobID, want, status.State)
}

func toolErrorCode(err error) string {
	var toolErr *toolcore.ToolError
	if errors.As(err, &toolErr) {
		return toolErr.Code
	}
	return ""
}

var jsonMarshal = json.Marshal
var jsonUnmarshal = json.Unmarshal
