package app

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/credential"
	jobruntime "github.com/uvwt/agentdock/internal/job"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

type staticAgentCredentialResolver struct{}

func (staticAgentCredentialResolver) Resolve(context.Context, string) (credential.Secret, error) {
	return credential.Secret("D2_TEST_SECRET_close_policy"), nil
}

type closeBlockingProvider struct {
	started  chan string
	mu       sync.Mutex
	onCancel func(string)
}

func (p *closeBlockingProvider) ResolveModel(raw string) (string, error) {
	if raw == "" || raw == "default" {
		return "fake-model", nil
	}
	return raw, nil
}

func (p *closeBlockingProvider) Run(ctx context.Context, request agentruntime.ProviderRequest, _ func(agentruntime.ProviderEvent) error) (agentruntime.ProviderResult, error) {
	select {
	case p.started <- request.AgentID:
	default:
	}
	<-ctx.Done()
	p.mu.Lock()
	callback := p.onCancel
	p.mu.Unlock()
	if callback != nil {
		callback(request.AgentID)
	}
	return agentruntime.ProviderResult{}, ctx.Err()
}

func waitAgentAppState(t *testing.T, manager *agentruntime.Manager, sessionID, agentID string, states ...agentruntime.AgentState) agentruntime.Record {
	t.Helper()
	want := map[agentruntime.AgentState]bool{}
	for _, state := range states {
		want[state] = true
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		record, err := manager.Get(sessionID, agentID)
		if err == nil && want[record.State] {
			return record
		}
		time.Sleep(5 * time.Millisecond)
	}
	record, _ := manager.Get(sessionID, agentID)
	t.Fatalf("timeout waiting for Agent %s states=%v last=%s", agentID, states, record.State)
	return agentruntime.Record{}
}

func TestSessionCloseCancelsAgentsBeforeJobsAndPreservesOtherSession(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	aDir := filepath.Join(root, "close-agent-a")
	bDir := filepath.Join(root, "close-agent-b")
	if err := ensureDir(aDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(bDir); err != nil {
		t.Fatal(err)
	}
	a := mustOpenSession(t, rt, map[string]any{"project_id": "close-agent-a", "repo_root": aDir})
	b := mustOpenSession(t, rt, map[string]any{"project_id": "close-agent-b", "repo_root": bDir})

	jobExecutor := newBlockingSessionJobExecutor()
	jobManager, err := jobruntime.NewManager(context.Background(), jobExecutor, jobruntime.Config{StorageRoot: filepath.Join(root, "close-agent-jobs"), Limits: jobruntime.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	rt.jobs = jobManager

	if rt.agents != nil {
		_ = rt.agents.Close()
	}
	provider := &closeBlockingProvider{started: make(chan string, 4)}
	ids := []string{"agent_close_a", "agent_close_b"}
	idIndex := 0
	agentManager, err := agentruntime.NewManager(context.Background(), agentruntime.ManagerOptions{
		StorageRoot: filepath.Join(root, "close-agents"), Sessions: newAgentSessionResolver(rt), Credentials: staticAgentCredentialResolver{},
		Providers: map[string]agentruntime.ProviderAdapter{"fake": provider}, Tools: newAgentToolBridge(rt), Limits: agentruntime.DefaultAgentLimits(),
		IDFactory: func() string { id := ids[idIndex]; idIndex++; return id },
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.agents = agentManager

	startAgent := func(session sessionruntime.Record) agentruntime.Record {
		record, err := agentManager.Start(context.Background(), agentruntime.StartRequest{
			SessionID: session.SessionID, Provider: "fake", CredentialRef: "cred_close_test", Prompt: "wait", PermissionMode: "project", MaxRequests: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return waitAgentAppState(t, agentManager, session.SessionID, record.AgentID, agentruntime.AgentStateRunning)
	}
	agentA := startAgent(a)
	agentB := startAgent(b)

	startJob := func(sessionID string) string {
		result, err := rt.Call(context.Background(), "job_start", map[string]any{"session_id": sessionID, "tool": "read", "arguments": map[string]any{"filePath": "marker.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		id := result["job_id"].(string)
		waitAppJobState(t, jobManager, id, jobruntime.StateRunning)
		return id
	}
	jobA := startJob(a.SessionID)
	jobB := startJob(b.SessionID)

	var agentSawJobAActive bool
	provider.mu.Lock()
	provider.onCancel = func(agentID string) {
		if agentID != agentA.AgentID {
			return
		}
		status, statusErr := jobManager.Status(jobA)
		if statusErr == nil && !status.State.Terminal() {
			agentSawJobAActive = true
		}
	}
	provider.mu.Unlock()

	closed, err := rt.Call(context.Background(), "session_close", map[string]any{"session_id": a.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !agentSawJobAActive {
		t.Fatal("Session close did not cancel/await Agent A before cancelling Job A")
	}
	if got := waitAgentAppState(t, agentManager, a.SessionID, agentA.AgentID, agentruntime.AgentStateCancelled); got.State != agentruntime.AgentStateCancelled {
		t.Fatalf("Agent A state=%s", got.State)
	}
	if got, _ := jobManager.Status(jobA); got.State != jobruntime.StateCancelled {
		t.Fatalf("Job A state=%s", got.State)
	}
	cancelled, _ := closed["jobs_cancelled"].([]any)
	if len(cancelled) != 1 || cancelled[0] != jobA {
		t.Fatalf("session_close jobs_cancelled=%+v", closed)
	}

	if got, err := agentManager.Get(b.SessionID, agentB.AgentID); err != nil || (got.State != agentruntime.AgentStateRunning && got.State != agentruntime.AgentStateSucceeded) {
		t.Fatalf("Agent B changed: %+v err=%v", got, err)
	}
	if got, _ := jobManager.Status(jobB); got.State != jobruntime.StateRunning {
		t.Fatalf("Job B state=%s", got.State)
	}
	bStatus, err := rt.sessions.Get(b.SessionID)
	if err != nil || bStatus.State != sessionruntime.StateActive {
		t.Fatalf("Session B=%+v err=%v", bStatus, err)
	}
	if aStatus, err := rt.sessions.Get(a.SessionID); err != nil || aStatus.State != sessionruntime.StateClosed {
		t.Fatalf("Session A=%+v err=%v", aStatus, err)
	}

	_, _ = agentManager.Cancel(context.Background(), b.SessionID, agentB.AgentID, "test cleanup")
	_, _ = jobManager.Cancel(context.Background(), jobB, "test cleanup")
}
