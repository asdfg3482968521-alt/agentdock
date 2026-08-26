package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/opencodeautonomous"
)

type d3G1BlockingRunner struct {
	mu       sync.Mutex
	requests map[string]opencodeautonomous.Request
	started  chan string
	release  map[string]chan struct{}
}

func newD3G1BlockingRunner(ids ...string) *d3G1BlockingRunner {
	r := &d3G1BlockingRunner{
		requests: map[string]opencodeautonomous.Request{},
		started:  make(chan string, len(ids)),
		release:  map[string]chan struct{}{},
	}
	for _, id := range ids {
		r.release[id] = make(chan struct{})
	}
	return r
}

func (r *d3G1BlockingRunner) Run(ctx context.Context, request opencodeautonomous.Request, sink func(opencodeautonomous.TelemetryEvent) error) (opencodeautonomous.Result, error) {
	r.mu.Lock()
	r.requests[request.AgentID] = request
	r.mu.Unlock()
	if sink != nil {
		if err := sink(opencodeautonomous.TelemetryEvent{EventID: "evt-" + request.AgentID, Cursor: 1, SessionID: "root-" + request.AgentID, Tool: "read", CallID: request.AgentID, Status: "completed"}); err != nil {
			return opencodeautonomous.Result{}, err
		}
	}
	r.started <- request.AgentID
	select {
	case <-ctx.Done():
		return opencodeautonomous.Result{}, ctx.Err()
	case <-r.release[request.AgentID]:
		return opencodeautonomous.Result{
			SubagentID: request.AgentID, RootOpenCodeSessionID: "root-" + request.AgentID,
			Provider: "opencode", Model: request.Model, FinalText: "done-" + request.AgentID,
		}, nil
	}
}

func (r *d3G1BlockingRunner) releaseAgent(id string) { close(r.release[id]) }

func (r *d3G1BlockingRunner) request(id string) opencodeautonomous.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[id]
}

func TestD3G1PublicAgentPoolRunsABCWithTargetedCancelAndIndependentOutput(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	if rt.agents != nil {
		_ = rt.agents.Close()
	}

	ids := []string{"agent-g1-a", "agent-g1-b", "agent-g1-c"}
	runner := newD3G1BlockingRunner(ids...)
	idIndex := 0
	manager, err := agentruntime.NewManager(context.Background(), agentruntime.ManagerOptions{
		StorageRoot: filepath.Join(root, "d3-g1-pool-agents"),
		Sessions:    newAgentSessionResolver(rt),
		Credentials: rt.credentialStore,
		Backends: map[string]agentruntime.AgentBackend{
			d3NativeAutonomousProvider: newOpenCodeNativeAutonomousAgentBackend(runner),
		},
		Limits: d3G1AgentLimits(true),
		IDFactory: func() string {
			id := ids[idIndex]
			idIndex++
			return id
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.agents = manager

	sessions := map[string]string{}
	agents := map[string]string{}
	for _, label := range []string{"a", "b", "c"} {
		workspace := filepath.Join(root, "workspace-"+label)
		if err := ensureDir(workspace); err != nil {
			t.Fatal(err)
		}
		session := mustOpenSession(t, rt, map[string]any{"project_id": "d3-g1-" + label, "repo_root": workspace})
		sessions[label] = session.SessionID
		started, err := rt.Call(context.Background(), "agent_start", map[string]any{
			"session_id": session.SessionID, "provider": d3NativeAutonomousProvider, "model": nil, "role": "implementation",
			"credential_ref": "cred_d3_g1_local_" + label, "permission": "project", "timeout_ms": 60000,
			"budget": map[string]any{"max_requests": 1}, "prompt": "autonomous task " + label,
		})
		if err != nil {
			t.Fatalf("agent_start %s: %v", label, err)
		}
		agents[label] = publicAgentFromResult(t, started)["agent_id"].(string)
	}

	seen := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for len(seen) < 3 {
		select {
		case id := <-runner.started:
			seen[id] = true
		case <-deadline:
			t.Fatalf("A/B/C did not overlap; started=%v", seen)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("parallel roots=%v", seen)
	}

	for label, agentID := range agents {
		record, err := rt.agents.Get(sessions[label], agentID)
		if err != nil {
			t.Fatal(err)
		}
		if record.State != agentruntime.AgentStateRunning {
			t.Fatalf("%s state=%s want RUNNING", label, record.State)
		}
		request := runner.request(agentID)
		if request.AgentDockSessionID != sessions[label] || request.Workspace != record.Workspace || request.PermissionMode != "project" {
			t.Fatalf("%s authority mapping request=%+v record=%+v", label, request, record)
		}
		if record.RuntimeScope != d1RuntimeScope(sessions[label]) {
			t.Fatalf("%s runtime_scope=%q want=%q", label, record.RuntimeScope, d1RuntimeScope(sessions[label]))
		}
		output, err := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": sessions[label], "agent_id": agentID, "cursor": 0, "limit": 65536})
		if err != nil {
			t.Fatalf("agent_output %s: %v", label, err)
		}
		text := fmt.Sprint(output["output"])
		if !strings.Contains(text, agentID) {
			t.Fatalf("%s output missing own identity: %q", label, text)
		}
		for otherLabel, otherID := range agents {
			if otherLabel != label && strings.Contains(text, otherID) {
				t.Fatalf("%s output leaked %s identity: %q", label, otherLabel, text)
			}
		}
	}

	cancelled, err := rt.Call(context.Background(), "agent_cancel", map[string]any{"session_id": sessions["b"], "agent_id": agents["b"], "reason": "D3-G1 targeted B cancel"})
	if err != nil {
		t.Fatalf("cancel B: %v", err)
	}
	if cancelled["state"] != string(agentruntime.AgentStateCancelled) {
		t.Fatalf("cancel B result=%+v", cancelled)
	}
	for _, label := range []string{"a", "c"} {
		record, err := rt.agents.Get(sessions[label], agents[label])
		if err != nil || record.State != agentruntime.AgentStateRunning {
			t.Fatalf("%s corrupted by B cancel: record=%+v err=%v", label, record, err)
		}
	}

	runner.releaseAgent(agents["a"])
	if record, err := rt.agents.Wait(context.Background(), agents["a"]); err != nil || record.State != agentruntime.AgentStateSucceeded {
		t.Fatalf("A finish record=%+v err=%v", record, err)
	}
	if record, err := rt.agents.Get(sessions["c"], agents["c"]); err != nil || record.State != agentruntime.AgentStateRunning {
		t.Fatalf("C corrupted by A terminal state: record=%+v err=%v", record, err)
	}
	runner.releaseAgent(agents["c"])
	if record, err := rt.agents.Wait(context.Background(), agents["c"]); err != nil || record.State != agentruntime.AgentStateSucceeded {
		t.Fatalf("C finish record=%+v err=%v", record, err)
	}
}
