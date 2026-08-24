package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	jobruntime "github.com/uvwt/agentdock/internal/job"
)

type capturingAgentJobExecutor struct {
	mu       sync.Mutex
	requests []jobruntime.RuntimeExecutionRequest
}

func (e *capturingAgentJobExecutor) Execute(_ context.Context, request jobruntime.RuntimeExecutionRequest) (jobruntime.RuntimeExecutionHandle, error) {
	e.mu.Lock()
	e.requests = append(e.requests, request)
	e.mu.Unlock()
	completion := make(chan jobruntime.RuntimeExecutionResult, 1)
	completion <- jobruntime.RuntimeExecutionResult{OK: true, ExitCode: 0, Summary: "tool-ok"}
	close(completion)
	return jobruntime.RuntimeExecutionHandle{RuntimeID: "agent-test-" + request.JobID, Completion: completion}, nil
}

func (e *capturingAgentJobExecutor) Cancel(context.Context, jobruntime.RuntimeCancelRequest) error {
	return nil
}

func (e *capturingAgentJobExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.requests)
}

func newAgentToolBridgeRuntime(t *testing.T) (*Runtime, *capturingAgentJobExecutor, string) {
	t.Helper()
	rt, root := newSessionToolsRuntime(t)
	executor := &capturingAgentJobExecutor{}
	limits := jobruntime.DefaultLimits()
	manager, err := jobruntime.NewManager(context.Background(), executor, jobruntime.Config{StorageRoot: filepath.Join(root, "agent-jobs"), Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	rt.jobs = manager
	return rt, executor, root
}

func agentToolRequest(agentID string, sessionID string, workspace string, permission string, tool string, arguments map[string]any) agentruntime.ToolExecutionRequest {
	return agentruntime.ToolExecutionRequest{
		AgentID: agentID, SessionID: sessionID, Workspace: workspace, RuntimeScope: d1RuntimeScope(sessionID),
		PermissionMode: permission, Tool: tool, Arguments: arguments,
	}
}

func TestAgentToolBridgeReadonlyAllowlistAndMutationDenial(t *testing.T) {
	rt, executor, root := newAgentToolBridgeRuntime(t)
	workspace := filepath.Join(root, "project")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "agent-ro-policy", "repo_root": workspace})
	bridge := newAgentToolBridge(rt)

	allowed := map[string]bool{}
	for _, tool := range bridge.Tools("read-only") {
		allowed[tool.Name] = true
	}
	for _, name := range []string{"opencode_read", "opencode_glob", "opencode_grep", "opencode_lsp", "opencode_git"} {
		if !allowed[name] {
			t.Fatalf("read-only tool %s missing", name)
		}
	}
	for _, name := range []string{"opencode_write", "opencode_edit", "opencode_apply_patch", "opencode_bash"} {
		if allowed[name] {
			t.Fatalf("read-only tool %s must not be exposed", name)
		}
	}

	for _, name := range []string{"opencode_read", "opencode_glob", "opencode_grep", "opencode_lsp"} {
		if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-a", session.SessionID, session.Workspace(), "read-only", name, map[string]any{})); err != nil {
			t.Fatalf("%s read-only execution failed: %v", name, err)
		}
	}
	beforeDenied := executor.count()
	for _, name := range []string{"opencode_write", "opencode_edit", "opencode_apply_patch", "opencode_bash"} {
		if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-a", session.SessionID, session.Workspace(), "read-only", name, map[string]any{})); !errors.Is(err, agentruntime.ErrAgentWriteDenied) {
			t.Fatalf("%s error=%v want agent write denied", name, err)
		}
	}
	if executor.count() != beforeDenied {
		t.Fatal("denied mutation reached Job Manager executor")
	}
}

func TestAgentToolBridgeReadonlyGitClassifiesOperation(t *testing.T) {
	rt, executor, root := newAgentToolBridgeRuntime(t)
	workspace := filepath.Join(root, "git-policy")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "git-policy", "repo_root": workspace})
	bridge := newAgentToolBridge(rt)
	for _, operation := range []string{"repository", "status", "diff"} {
		if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-git", session.SessionID, session.Workspace(), "read-only", "opencode_git", map[string]any{"operation": operation})); err != nil {
			t.Fatalf("git %s should be allowed: %v", operation, err)
		}
	}
	before := executor.count()
	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-git", session.SessionID, session.Workspace(), "read-only", "opencode_git", map[string]any{"operation": "commit"})); !errors.Is(err, agentruntime.ErrAgentWriteDenied) {
		t.Fatalf("git mutation error=%v want agent write denied", err)
	}
	if executor.count() != before {
		t.Fatal("read-only Git mutation reached executor")
	}
}

func TestAgentToolBridgeFreshSessionTupleAndAgentOwnedJobBinding(t *testing.T) {
	rt, _, root := newAgentToolBridgeRuntime(t)
	aDir := filepath.Join(root, "a")
	bDir := filepath.Join(root, "b")
	if err := ensureDir(aDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(bDir); err != nil {
		t.Fatal(err)
	}
	a := mustOpenSession(t, rt, map[string]any{"project_id": "agent-a", "repo_root": aDir})
	b := mustOpenSession(t, rt, map[string]any{"project_id": "agent-b", "repo_root": bDir})
	bridge := newAgentToolBridge(rt)

	result, err := bridge.Execute(context.Background(), agentToolRequest("agent-a", a.SessionID, a.Workspace(), "project", "opencode_write", map[string]any{"filePath": "x.txt", "content": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID == "" {
		t.Fatal("agent tool execution missing job_id")
	}
	status, err := rt.jobs.Status(result.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.AgentID != "agent-a" || status.SessionID != a.SessionID || status.Workspace != a.Workspace() || status.RuntimeScope != d1RuntimeScope(a.SessionID) || status.Tool != "write" {
		t.Fatalf("agent job binding mismatch: %+v", status)
	}

	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-a", a.SessionID, b.Workspace(), "project", "opencode_read", map[string]any{})); !errors.Is(err, agentruntime.ErrAgentSessionOwnershipMismatch) {
		t.Fatalf("forged workspace error=%v", err)
	}
	forged := agentToolRequest("agent-a", b.SessionID, a.Workspace(), "project", "opencode_read", map[string]any{})
	forged.RuntimeScope = d1RuntimeScope(a.SessionID)
	if _, err := bridge.Execute(context.Background(), forged); !errors.Is(err, agentruntime.ErrAgentSessionOwnershipMismatch) {
		t.Fatalf("cross-session tuple error=%v", err)
	}
	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-a", a.SessionID, a.Workspace(), "project", "opencode_read", map[string]any{"workspace": b.Workspace()})); !errors.Is(err, agentruntime.ErrInvalidArguments) {
		t.Fatalf("workspace injection error=%v", err)
	}
	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-a", a.SessionID, a.Workspace(), "project", "opencode_read", map[string]any{"permission_mode": "full"})); !errors.Is(err, agentruntime.ErrInvalidArguments) {
		t.Fatalf("permission injection error=%v", err)
	}
}

func TestAgentToolBridgeAgentCannotExceedReadonlySession(t *testing.T) {
	rt, _, root := newAgentToolBridgeRuntime(t)
	workspace := filepath.Join(root, "readonly")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "readonly", "repo_root": workspace, "isolation_mode": "shared-readonly"})
	bridge := newAgentToolBridge(rt)
	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-ro", session.SessionID, session.Workspace(), "project", "opencode_write", map[string]any{})); !errors.Is(err, agentruntime.ErrAgentWriteDenied) {
		t.Fatalf("project agent exceeded read-only session: %v", err)
	}
	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-ro", session.SessionID, session.Workspace(), "read-only", "opencode_read", map[string]any{})); err != nil {
		t.Fatalf("read-only agent should read in read-only session: %v", err)
	}
}

func TestAgentSessionResolverUsesFreshD1PermissionAndClosedState(t *testing.T) {
	rt, _, root := newAgentToolBridgeRuntime(t)
	rwDir := filepath.Join(root, "resolver-rw")
	roDir := filepath.Join(root, "resolver-ro")
	if err := ensureDir(rwDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(roDir); err != nil {
		t.Fatal(err)
	}
	rw := mustOpenSession(t, rt, map[string]any{"project_id": "resolver-rw", "repo_root": rwDir})
	ro := mustOpenSession(t, rt, map[string]any{"project_id": "resolver-ro", "repo_root": roDir, "isolation_mode": "shared-readonly"})
	resolver := newAgentSessionResolver(rt)
	if binding, err := resolver.ResolveAgentSession(context.Background(), rw.SessionID); err != nil || binding.PermissionMode != "project" || binding.Workspace != rw.Workspace() {
		t.Fatalf("rw binding=%+v err=%v", binding, err)
	}
	if binding, err := resolver.ResolveAgentSession(context.Background(), ro.SessionID); err != nil || binding.PermissionMode != "read-only" || binding.Workspace != ro.Workspace() {
		t.Fatalf("ro binding=%+v err=%v", binding, err)
	}
	if _, err := rt.sessions.Close(context.Background(), rw.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveAgentSession(context.Background(), rw.SessionID); !errors.Is(err, agentruntime.ErrAgentSessionClosed) {
		t.Fatalf("closed session resolver error=%v", err)
	}
}

func TestAgentToolBridgeRejectsUnknownToolAndInvalidGitArguments(t *testing.T) {
	rt, executor, root := newAgentToolBridgeRuntime(t)
	workspace := filepath.Join(root, "schema")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "schema", "repo_root": workspace})
	bridge := newAgentToolBridge(rt)
	before := executor.count()
	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-schema", session.SessionID, session.Workspace(), "project", "arbitrary_shell", map[string]any{})); !errors.Is(err, agentruntime.ErrToolError) {
		t.Fatalf("unknown tool error=%v", err)
	}
	if _, err := bridge.Execute(context.Background(), agentToolRequest("agent-schema", session.SessionID, session.Workspace(), "read-only", "opencode_git", map[string]any{})); !errors.Is(err, agentruntime.ErrInvalidArguments) {
		t.Fatalf("missing git operation error=%v", err)
	}
	if executor.count() != before {
		t.Fatal("invalid tool call reached executor")
	}
}

func TestAgentToolBridgeCancellationCancelsOwnedJob(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	workspace := filepath.Join(root, "cancel")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "cancel", "repo_root": workspace})
	executor := newBlockingSessionJobExecutor()
	manager, err := jobruntime.NewManager(context.Background(), executor, jobruntime.Config{StorageRoot: filepath.Join(root, "agent-cancel-jobs"), Limits: jobruntime.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	rt.jobs = manager
	bridge := newAgentToolBridge(rt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := bridge.Execute(ctx, agentToolRequest("agent-cancel", session.SessionID, session.Workspace(), "project", "opencode_read", map[string]any{}))
		done <- err
	}()
	var jobID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items := manager.List(jobruntime.ListFilter{SessionID: session.SessionID, Limit: 10})
		if len(items) == 1 && items[0].State == jobruntime.StateRunning {
			jobID = items[0].JobID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if jobID == "" {
		t.Fatal("Agent-owned Job never reached RUNNING")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool bridge cancellation did not return")
	}
	status, err := manager.Status(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != jobruntime.StateCancelled {
		t.Fatalf("Agent-owned Job state=%s want CANCELLED", status.State)
	}
	if status.AgentID != "agent-cancel" {
		t.Fatalf("cancelled Job lost agent ownership: %+v", status)
	}
}
