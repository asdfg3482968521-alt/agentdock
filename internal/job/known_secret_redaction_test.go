package job

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentOwnedJobKnownSecretRedactedBeforePersistence(t *testing.T) {
	const (
		jobID  = "71111111-1111-4111-8111-111111111111"
		secret = "D2_TEST_SECRET_agent_job_persistence"
	)
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return jobID }
	executor.setPlan(jobID, fakePlan{
		duration: 30 * time.Millisecond,
		outputs: []RuntimeOutputEvent{
			{After: 5 * time.Millisecond, Stream: "stdout", Text: "stdout " + secret + "\n"},
			{After: 10 * time.Millisecond, Stream: "stderr", Text: "stderr " + secret + "\n"},
		},
		result: RuntimeExecutionResult{OK: true, ExitCode: 0, Summary: "summary " + secret},
	})
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.Start(StartRequest{
		Workspace: t.TempDir(), AgentID: "agent-a", SessionID: "session-a", RuntimeScope: "scope-a",
		Tool: "bash", Arguments: map[string]any{"cmd": "echo synthetic"}, PermissionMode: "project",
		KnownSecrets: [][]byte{[]byte(secret)},
	})
	if err != nil {
		t.Fatal(err)
	}
	status := waitState(t, manager, started.JobID, StateSucceeded)
	assertJobPublicTextHasNoKnownSecret(t, secret, status.ResultSummary, status.Error, status.CancelReason)
	out, err := manager.Output(OutputRequest{JobID: started.JobID, Cursor: 0, LimitBytes: DefaultLimits().MaxOutputBytesPerResponse})
	if err != nil {
		t.Fatal(err)
	}
	assertJobPublicTextHasNoKnownSecret(t, secret, out.Text)
	if !strings.Contains(out.Text, "[REDACTED]") {
		t.Fatalf("known secret was not visibly redacted from job output: %q", out.Text)
	}
	assertJobDiskHasNoKnownSecret(t, cfg.StorageRoot, started.JobID, secret)
	waitJobMetadataState(t, cfg.StorageRoot, started.JobID, StateSucceeded)

	manager.mu.RLock()
	retained := len(manager.jobs[started.JobID].knownSecrets)
	manager.mu.RUnlock()
	if retained != 0 {
		t.Fatalf("terminal Agent-owned Job retained %d known secrets in memory", retained)
	}

	recovered, err := NewManager(context.Background(), newFakeExecutor(), cfg)
	if err != nil {
		t.Fatalf("recover without persisted secret: %v", err)
	}
	recoveredStatus, err := recovered.Status(started.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredStatus.State != StateSucceeded {
		t.Fatalf("recovered state=%s want SUCCEEDED", recoveredStatus.State)
	}
	assertJobPublicTextHasNoKnownSecret(t, secret, recoveredStatus.ResultSummary, recoveredStatus.Error, recoveredStatus.CancelReason)
	recoveredOut, err := recovered.Output(OutputRequest{JobID: started.JobID, Cursor: 0, LimitBytes: DefaultLimits().MaxOutputBytesPerResponse})
	if err != nil {
		t.Fatal(err)
	}
	assertJobPublicTextHasNoKnownSecret(t, secret, recoveredOut.Text)
}

func TestAgentOwnedJobKnownSecretRedactsFailureCancelAndProgress(t *testing.T) {
	const secret = "D2_TEST_SECRET_agent_job_failure"
	t.Run("failure and progress", func(t *testing.T) {
		const jobID = "72222222-2222-4222-8222-222222222222"
		cfg := testConfig(t)
		cfg.IDFactory = func() string { return jobID }
		executor := RuntimeExecutorFunc{
			ExecuteFunc: func(_ context.Context, request RuntimeExecutionRequest) (RuntimeExecutionHandle, error) {
				request.OnProgress(map[string]any{"message": "progress " + secret})
				_ = request.OnOutput(RuntimeOutputEvent{Stream: "stdout", Text: "runtime " + secret + "\n"})
				completion := make(chan RuntimeExecutionResult, 1)
				completion <- RuntimeExecutionResult{Err: errors.New("runtime failure " + secret)}
				close(completion)
				return RuntimeExecutionHandle{RuntimeID: "failure-runtime", Completion: completion}, nil
			},
		}
		manager, err := NewManager(context.Background(), executor, cfg)
		if err != nil {
			t.Fatal(err)
		}
		started, err := manager.Start(StartRequest{Workspace: t.TempDir(), AgentID: "agent-f", SessionID: "session-f", RuntimeScope: "scope-f", Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project", KnownSecrets: [][]byte{[]byte(secret)}})
		if err != nil {
			t.Fatal(err)
		}
		status := waitState(t, manager, started.JobID, StateFailed)
		assertJobPublicTextHasNoKnownSecret(t, secret, status.ResultSummary, status.Error, status.CancelReason)
		if summary, _ := status.ProgressMetadata["summary"].(string); strings.Contains(summary, secret) {
			t.Fatalf("progress metadata leaked known secret: %q", summary)
		}
		assertJobDiskHasNoKnownSecret(t, cfg.StorageRoot, started.JobID, secret)
	})

	t.Run("cancel reason", func(t *testing.T) {
		const jobID = "73333333-3333-4333-8333-333333333333"
		executor := newFakeExecutor()
		cfg := testConfig(t)
		cfg.IDFactory = func() string { return jobID }
		executor.setPlan(jobID, fakePlan{duration: 2 * time.Second})
		manager, err := NewManager(context.Background(), executor, cfg)
		if err != nil {
			t.Fatal(err)
		}
		started, err := manager.Start(StartRequest{Workspace: t.TempDir(), AgentID: "agent-c", SessionID: "session-c", RuntimeScope: "scope-c", Tool: "bash", Arguments: map[string]any{}, PermissionMode: "project", KnownSecrets: [][]byte{[]byte(secret)}})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, manager, started.JobID, StateRunning)
		status, err := manager.Cancel(context.Background(), started.JobID, "cancel "+secret)
		if err != nil {
			t.Fatal(err)
		}
		assertJobPublicTextHasNoKnownSecret(t, secret, status.CancelReason, status.Error, status.ResultSummary)
		assertJobDiskHasNoKnownSecret(t, cfg.StorageRoot, started.JobID, secret)
	})
}

func TestLegacyJobGenericRedactionUnchanged(t *testing.T) {
	const jobID = "74444444-4444-4444-8444-444444444444"
	executor := newFakeExecutor()
	cfg := testConfig(t)
	cfg.IDFactory = func() string { return jobID }
	executor.setPlan(jobID, fakePlan{
		duration: 20 * time.Millisecond,
		outputs:  []RuntimeOutputEvent{{After: 5 * time.Millisecond, Stream: "stderr", Text: "Authorization: Bearer legacy-secret\n"}},
		result:   RuntimeExecutionResult{OK: true, ExitCode: 0, Summary: "done token=legacy-result-secret"},
	})
	manager, err := NewManager(context.Background(), executor, cfg)
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "bash", Arguments: map[string]any{"password": "legacy-argument-secret"}, Metadata: map[string]any{"api_key": "legacy-metadata-secret"}, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	status := waitState(t, manager, started.JobID, StateSucceeded)
	out, err := manager.Output(OutputRequest{JobID: started.JobID, Cursor: 0, LimitBytes: DefaultLimits().MaxOutputBytesPerResponse})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"legacy-secret", "legacy-result-secret", "legacy-argument-secret", "legacy-metadata-secret"} {
		if strings.Contains(out.Text, marker) || strings.Contains(status.ResultSummary, marker) {
			t.Fatalf("legacy generic redaction regressed for %q", marker)
		}
	}
	assertJobDiskHasNoKnownSecret(t, cfg.StorageRoot, started.JobID, "legacy-result-secret")
}

func waitJobMetadataState(t *testing.T, root, jobID string, state State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	path := filepath.Join(root, jobID, "metadata.json")
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			var record Record
			if json.Unmarshal(body, &record) == nil && record.State == state {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	body, _ := os.ReadFile(path)
	t.Fatalf("metadata did not persist terminal state %s: %s", state, body)
}

func assertJobPublicTextHasNoKnownSecret(t *testing.T, secret string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(value, secret) {
			t.Fatalf("job public text leaked known secret: %q", value)
		}
	}
}

func assertJobDiskHasNoKnownSecret(t *testing.T, root, jobID, secret string) {
	t.Helper()
	for _, name := range []string{"metadata.json", "events.ndjson"} {
		body, err := os.ReadFile(filepath.Join(root, jobID, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(body), secret) {
			t.Fatalf("%s persisted known secret %q", name, secret)
		}
	}
}

// RuntimeExecutorFunc keeps the regression focused on Job Manager persistence.
type RuntimeExecutorFunc struct {
	ExecuteFunc func(context.Context, RuntimeExecutionRequest) (RuntimeExecutionHandle, error)
	CancelFunc  func(context.Context, RuntimeCancelRequest) error
}

func (f RuntimeExecutorFunc) Execute(ctx context.Context, request RuntimeExecutionRequest) (RuntimeExecutionHandle, error) {
	return f.ExecuteFunc(ctx, request)
}

func (f RuntimeExecutorFunc) Cancel(ctx context.Context, request RuntimeCancelRequest) error {
	if f.CancelFunc != nil {
		return f.CancelFunc(ctx, request)
	}
	return nil
}
