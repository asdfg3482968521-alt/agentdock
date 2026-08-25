package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/credential"
	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
	"github.com/uvwt/agentdock/internal/opencodeworker"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

type fakeC25OpenCodeWorker struct {
	calls   int
	request opencodeworker.Request
	result  opencodeworker.Result
	err     error
	events  []opencodeworker.Event
	run     func(context.Context, opencodeworker.Request, opencodeworker.EventSink) (opencodeworker.Result, error)
}

func (f *fakeC25OpenCodeWorker) Execute(ctx context.Context, request opencodeworker.Request, sink opencodeworker.EventSink) (opencodeworker.Result, error) {
	f.calls++
	f.request = request
	if f.run != nil {
		return f.run(ctx, request, sink)
	}
	for _, event := range f.events {
		if sink != nil {
			sink(event)
		}
	}
	return f.result, f.err
}

func c25TaskPrompt(t *testing.T, instruction string, steps []opencodeworker.Step) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"instruction": instruction, "steps": steps})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestOpenCodeBackendMapsBoundedTaskToWorker(t *testing.T) {
	workspace := t.TempDir()
	worker := &fakeC25OpenCodeWorker{
		result: opencodeworker.Result{Completion: opencodeworker.CompletionSucceeded},
		events: []opencodeworker.Event{{Kind: opencodeworker.EventStepCompleted, Tool: "read", Output: map[string]any{"ok": true}}},
	}
	backend := newOpenCodeBackend(worker)
	model, err := backend.ResolveModel("")
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	if model != openCodeBackendModel {
		t.Fatalf("model=%q want=%q", model, openCodeBackendModel)
	}

	var emitted []agentruntime.BackendEvent
	result, err := backend.Run(context.Background(), agentruntime.BackendRequest{
		AgentID: "agent_a", SessionID: "session_a", Workspace: workspace, RuntimeScope: "scope-a",
		PermissionMode: "project", Provider: "opencode", Model: model, Role: "implementation",
		Prompt:  c25TaskPrompt(t, "read one file", []opencodeworker.Step{{ID: "read", Tool: "read", Arguments: map[string]any{"filePath": "README.md"}}}),
		Timeout: 2 * time.Second, Budget: agentruntime.AgentBudget{MaxRequests: 1},
	}, func(event agentruntime.BackendEvent) error {
		emitted = append(emitted, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if worker.calls != 1 {
		t.Fatalf("worker calls=%d want=1", worker.calls)
	}
	if worker.request.Scope.ID != "agent_a" || worker.request.Scope.Workspace != workspace {
		t.Fatalf("worker scope=%+v", worker.request.Scope)
	}
	if worker.request.PermissionMode != opencoderuntime.PermissionProject || worker.request.Timeout != 2*time.Second {
		t.Fatalf("worker execution mapping=%+v", worker.request)
	}
	if len(worker.request.Steps) != 1 || worker.request.Steps[0].Tool != "read" {
		t.Fatalf("worker steps=%+v", worker.request.Steps)
	}
	if result.ResolvedModel != openCodeBackendModel || result.StopReason != "stop" || !strings.Contains(result.Summary, "1 step") {
		t.Fatalf("backend result=%+v", result)
	}
	if len(emitted) == 0 || !strings.Contains(emitted[0].Text, "read") {
		t.Fatalf("emitted=%+v", emitted)
	}
}

func TestOpenCodeBackendReadOnlyRejectsMutatingStepBeforeWorker(t *testing.T) {
	worker := &fakeC25OpenCodeWorker{}
	backend := newOpenCodeBackend(worker)
	_, err := backend.Run(context.Background(), agentruntime.BackendRequest{
		AgentID: "agent_ro", SessionID: "session_ro", Workspace: t.TempDir(), RuntimeScope: "scope-ro",
		PermissionMode: "read-only", Provider: "opencode", Model: openCodeBackendModel,
		Prompt:  c25TaskPrompt(t, "write", []opencodeworker.Step{{Tool: "write", Arguments: map[string]any{"filePath": "x.txt", "content": "x"}}}),
		Timeout: time.Second, Budget: agentruntime.AgentBudget{MaxRequests: 1},
	}, nil)
	if !errors.Is(err, agentruntime.ErrAgentWriteDenied) {
		t.Fatalf("error=%v want AGENT_WRITE_DENIED", err)
	}
	if worker.calls != 0 {
		t.Fatalf("worker called %d times for denied write", worker.calls)
	}
}

func TestOpenCodeBackendRejectsUnboundedPromptAsBadRequest(t *testing.T) {
	worker := &fakeC25OpenCodeWorker{}
	backend := newOpenCodeBackend(worker)
	_, err := backend.Run(context.Background(), agentruntime.BackendRequest{
		AgentID: "agent_bad", SessionID: "session_bad", Workspace: t.TempDir(), RuntimeScope: "scope-bad",
		PermissionMode: "project", Provider: "opencode", Model: openCodeBackendModel,
		Prompt: "please modify whatever seems useful", Timeout: time.Second, Budget: agentruntime.AgentBudget{MaxRequests: 1},
	}, nil)
	var backendErr *agentruntime.BackendError
	if !errors.As(err, &backendErr) || backendErr.Code != agentruntime.BackendErrorBadRequest {
		t.Fatalf("error=%v want BAD_REQUEST backend error", err)
	}
	if worker.calls != 0 {
		t.Fatalf("worker called %d times for invalid prompt", worker.calls)
	}
}

type fakeC25OpenCodeExecutor struct{}

func (fakeC25OpenCodeExecutor) Execute(_ context.Context, workspace string, mode opencoderuntime.PermissionMode, tool string, _ map[string]any) (opencoderuntime.ExecutionResult, error) {
	return opencoderuntime.ExecutionResult{
		Tool: tool, Workspace: workspace, PermissionMode: string(mode),
		Runtime: opencoderuntime.Summary{Workspace: workspace, PermissionMode: string(mode), RuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", State: opencoderuntime.StateReady},
		Result:  map[string]any{"isError": false, "structuredContent": map[string]any{"ok": true}},
	}, nil
}

func TestNewOpenCodeAgentBackendWrapsWorkerEngine(t *testing.T) {
	backend, err := newOpenCodeAgentBackend(fakeC25OpenCodeExecutor{})
	if err != nil {
		t.Fatalf("newOpenCodeAgentBackend: %v", err)
	}
	metadata, ok := backend.(agentruntime.AgentBackendMetadata)
	if !ok || metadata.BackendIdentity() != agentruntime.BackendIdentityOpenCode || metadata.RequiresCredential() {
		t.Fatalf("backend metadata mismatch: %#v", backend)
	}
}

func newC25PublicOpenCodeRuntime(t *testing.T, worker openCodeWorker) (*Runtime, sessionruntime.Record, string) {
	t.Helper()
	rt, root := newSessionToolsRuntime(t)
	if rt.agents != nil {
		_ = rt.agents.Close()
	}
	manager, err := agentruntime.NewManager(context.Background(), agentruntime.ManagerOptions{
		StorageRoot: filepath.Join(root, "c25-public-opencode-agents"),
		Sessions:    newAgentSessionResolver(rt), Credentials: rt.credentialStore,
		Backends: map[string]agentruntime.AgentBackend{"opencode": newOpenCodeBackend(worker, func(sessionID, runtimeScope string) error {
			_, bindErr := rt.sessions.SetRuntime(sessionID, runtimeScope)
			return bindErr
		})},
		Limits:    agentruntime.DefaultAgentLimits(),
		IDFactory: func() string { return "agent_c25_public_opencode" },
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.agents = manager
	workspace := filepath.Join(root, "c25-public-opencode-workspace")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "c25-public-opencode", "repo_root": workspace})
	return rt, session, filepath.Join(root, "c25-public-opencode-agents")
}

func TestC25OpenCodePublicAgentStartDispatchesWorkerWithoutStoredCredential(t *testing.T) {
	worker := &fakeC25OpenCodeWorker{
		result: opencodeworker.Result{Completion: opencodeworker.CompletionSucceeded},
		events: []opencodeworker.Event{{Kind: opencodeworker.EventStepCompleted, Tool: "read", Output: map[string]any{"text": "public-opencode-ok"}}},
	}
	rt, session, storageRoot := newC25PublicOpenCodeRuntime(t, worker)
	args := map[string]any{
		"session_id": session.SessionID, "provider": "opencode", "model": nil, "role": "implementation",
		"credential_ref": "cred_opencode_local", "permission": "project", "timeout_ms": 30000,
		"budget": map[string]any{"max_requests": 1},
		"prompt": c25TaskPrompt(t, "read one file", []opencodeworker.Step{{ID: "read", Tool: "read", Arguments: map[string]any{"filePath": "README.md"}}}),
	}
	started, err := rt.Call(context.Background(), "agent_start", args)
	if err != nil {
		t.Fatalf("agent_start: %v", err)
	}
	agent := publicAgentFromResult(t, started)
	agentID, _ := agent["agent_id"].(string)
	if agentID == "" {
		t.Fatalf("agent_start=%#v", started)
	}
	finished, err := rt.agents.Wait(context.Background(), agentID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if finished.State != agentruntime.AgentStateSucceeded || finished.Backend != agentruntime.BackendIdentityOpenCode {
		t.Fatalf("finished=%+v", finished)
	}
	if worker.calls != 1 || worker.request.Scope.ID != agentID || worker.request.Scope.Workspace != session.Workspace() {
		t.Fatalf("worker request=%+v session=%+v", worker.request, session)
	}
	if _, err := rt.credentialStore.Describe(context.Background(), "cred_opencode_local"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("OpenCode public path unexpectedly required/stored provider credential: %v", err)
	}
	output, err := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": session.SessionID, "agent_id": agentID, "cursor": 0, "limit": 65536})
	if err != nil || !strings.Contains(fmt.Sprint(output["output"]), "public-opencode-ok") {
		t.Fatalf("agent_output=%#v err=%v", output, err)
	}
	data, err := os.ReadFile(filepath.Join(storageRoot, "records", agentID+".json"))
	if err != nil {
		t.Fatalf("read durable Agent record: %v", err)
	}
	if !strings.Contains(string(data), `"backend":"opencode"`) || !strings.Contains(string(data), `"state":"SUCCEEDED"`) {
		t.Fatalf("durable Agent record missing OpenCode terminal identity: %s", data)
	}
}

func TestC25OpenCodePublicAgentBindsSessionRuntimeOwnership(t *testing.T) {
	worker := &fakeC25OpenCodeWorker{result: opencodeworker.Result{Completion: opencodeworker.CompletionSucceeded}}
	rt, session, _ := newC25PublicOpenCodeRuntime(t, worker)
	started, err := rt.Call(context.Background(), "agent_start", map[string]any{
		"session_id": session.SessionID, "provider": "opencode", "model": nil, "role": "implementation",
		"credential_ref": "cred_opencode_local", "permission": "project", "timeout_ms": 30000,
		"budget": map[string]any{"max_requests": 1},
		"prompt": c25TaskPrompt(t, "read one file", []opencodeworker.Step{{ID: "read", Tool: "read", Arguments: map[string]any{"filePath": "README.md"}}}),
	})
	if err != nil {
		t.Fatalf("agent_start: %v", err)
	}
	agent := publicAgentFromResult(t, started)
	agentID, _ := agent["agent_id"].(string)
	if agentID == "" {
		t.Fatalf("agent_start=%#v", started)
	}
	if _, err := rt.agents.Wait(context.Background(), agentID); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	bound, err := rt.sessions.Get(session.SessionID)
	if err != nil {
		t.Fatalf("session get: %v", err)
	}
	wantRuntime := d1RuntimeScope(session.SessionID)
	if bound.RuntimeID == nil || *bound.RuntimeID != wantRuntime {
		t.Fatalf("session runtime ownership=%v want=%q", bound.RuntimeID, wantRuntime)
	}
}
