package opencodeworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/opencode"
)

type fakeCall struct {
	workspace string
	mode      opencode.PermissionMode
	tool      string
	args      map[string]any
}

type fakeExecutor struct {
	mu     sync.Mutex
	calls  []fakeCall
	callFn func(context.Context, string, opencode.PermissionMode, string, map[string]any) (opencode.ExecutionResult, error)
}

func (f *fakeExecutor) Execute(ctx context.Context, workspace string, mode opencode.PermissionMode, tool string, args map[string]any) (opencode.ExecutionResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{workspace: workspace, mode: mode, tool: tool, args: args})
	fn := f.callFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, workspace, mode, tool, args)
	}
	return opencode.ExecutionResult{
		Tool: tool, Workspace: workspace, PermissionMode: string(mode),
		Runtime: opencode.Summary{Workspace: workspace, PermissionMode: string(mode), RuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", State: opencode.StateReady},
		Result:  map[string]any{"isError": false, "structuredContent": map[string]any{"ok": true}},
	}, nil
}

func newTestEngine(t *testing.T, executor Executor) *Engine {
	t.Helper()
	engine, err := New(executor, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func testRequest(workspace string) Request {
	return Request{
		Scope: Scope{ID: "session-test", Workspace: workspace}, PermissionMode: opencode.PermissionProject,
		Instruction: "inspect then update one file",
		Steps: []Step{
			{ID: "read", Tool: "read", Arguments: map[string]any{"filePath": "a.txt"}},
			{ID: "edit", Tool: "edit", Arguments: map[string]any{"filePath": "a.txt", "oldString": "a", "newString": "b"}},
		},
	}
}

func TestWorkerSuccessPropagatesResultOutputAndProgress(t *testing.T) {
	executor := &fakeExecutor{}
	engine := newTestEngine(t, executor)
	workspace := t.TempDir()
	var streamed []Event

	result, err := engine.Execute(context.Background(), testRequest(workspace), func(event Event) { streamed = append(streamed, event) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Completion != CompletionSucceeded || len(result.Steps) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Steps[0].Runtime.RuntimeSHA != "d5664f38002d8326eca179cf2efb5774c8ce1247" {
		t.Fatalf("runtime SHA not propagated: %#v", result.Steps[0].Runtime)
	}
	if len(streamed) != 6 || len(result.Events) != len(streamed) {
		t.Fatalf("progress events streamed=%d captured=%d", len(streamed), len(result.Events))
	}
	if streamed[0].Kind != EventTaskStarted || streamed[len(streamed)-1].Kind != EventTaskCompleted {
		t.Fatalf("unexpected event sequence: %#v", streamed)
	}
}

func TestWorkerToolFailureStopsRemainingSteps(t *testing.T) {
	executor := &fakeExecutor{callFn: func(_ context.Context, workspace string, mode opencode.PermissionMode, tool string, _ map[string]any) (opencode.ExecutionResult, error) {
		return opencode.ExecutionResult{Tool: tool, Workspace: workspace, PermissionMode: string(mode), Result: map[string]any{
			"isError": true, "content": []any{map[string]any{"text": "OUTSIDE_WORKSPACE"}},
		}}, nil
	}}
	engine := newTestEngine(t, executor)
	result, err := engine.Execute(context.Background(), testRequest(t.TempDir()), nil)
	if ErrorCode(err) != "OPENCODE_WORKER_TOOL_FAILED" || result.Completion != CompletionFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(executor.calls))
	}
}

func TestWorkerContextCancellationPropagatesAndLeavesNoActiveExecution(t *testing.T) {
	started := make(chan struct{})
	var active atomic.Int32
	executor := &fakeExecutor{callFn: func(ctx context.Context, workspace string, mode opencode.PermissionMode, tool string, _ map[string]any) (opencode.ExecutionResult, error) {
		active.Add(1)
		defer active.Add(-1)
		close(started)
		<-ctx.Done()
		return opencode.ExecutionResult{Tool: tool, Workspace: workspace, PermissionMode: string(mode)}, ctx.Err()
	}}
	engine := newTestEngine(t, executor)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := engine.Execute(ctx, Request{
			Scope: Scope{ID: "session-cancel", Workspace: t.TempDir()}, PermissionMode: opencode.PermissionProject,
			Instruction: "run bounded command", Steps: []Step{{Tool: "bash", Arguments: map[string]any{"command": "slow"}}},
		}, nil)
		resultCh <- result
		errCh <- err
	}()
	<-started
	cancel()
	result := <-resultCh
	err := <-errCh
	if ErrorCode(err) != "OPENCODE_WORKER_CANCELLED" || result.Completion != CompletionCancelled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active executions after cancellation = %d", got)
	}
}

func TestWorkerTimeoutPropagatesDeadline(t *testing.T) {
	executor := &fakeExecutor{callFn: func(ctx context.Context, workspace string, mode opencode.PermissionMode, tool string, _ map[string]any) (opencode.ExecutionResult, error) {
		<-ctx.Done()
		return opencode.ExecutionResult{Tool: tool, Workspace: workspace, PermissionMode: string(mode)}, ctx.Err()
	}}
	engine := newTestEngine(t, executor)
	request := Request{
		Scope: Scope{ID: "session-timeout", Workspace: t.TempDir()}, PermissionMode: opencode.PermissionProject,
		Instruction: "bounded command", Timeout: 20 * time.Millisecond, Steps: []Step{{Tool: "bash"}},
	}
	result, err := engine.Execute(context.Background(), request, nil)
	if ErrorCode(err) != "OPENCODE_WORKER_TIMEOUT" || result.Completion != CompletionTimedOut {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestWorkerWorkspaceConfinementAndPermissionPropagation(t *testing.T) {
	executor := &fakeExecutor{}
	engine := newTestEngine(t, executor)
	workspace := t.TempDir()
	request := testRequest(filepath.Join(workspace, "."))
	request.PermissionMode = opencode.PermissionFull
	result, err := engine.Execute(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if result.Workspace != filepath.Clean(real) || result.PermissionMode != opencode.PermissionFull {
		t.Fatalf("result scope widened or mode changed: %#v", result)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	for _, call := range executor.calls {
		if call.workspace != filepath.Clean(real) || call.mode != opencode.PermissionFull {
			t.Fatalf("executor call scope/mode mismatch: %#v", call)
		}
	}
}

func TestWorkerRejectsInvalidWorkspaceAndScopeBeforeRuntime(t *testing.T) {
	executor := &fakeExecutor{}
	engine := newTestEngine(t, executor)
	request := testRequest(t.TempDir())
	request.Scope.ID = ""
	if _, err := engine.Execute(context.Background(), request, nil); ErrorCode(err) != "OPENCODE_WORKER_SCOPE_REQUIRED" {
		t.Fatalf("scope error=%v", err)
	}
	request = testRequest(filepath.Join(t.TempDir(), "missing"))
	if _, err := engine.Execute(context.Background(), request, nil); ErrorCode(err) != "OPENCODE_WORKER_WORKSPACE_INVALID" {
		t.Fatalf("workspace error=%v", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.calls) != 0 {
		t.Fatalf("runtime invoked for invalid scope: %#v", executor.calls)
	}
}

func TestWorkerRuntimeStartupFailureIsStructured(t *testing.T) {
	executor := &fakeExecutor{callFn: func(context.Context, string, opencode.PermissionMode, string, map[string]any) (opencode.ExecutionResult, error) {
		return opencode.ExecutionResult{}, &opencode.Error{Code: "OPENCODE_START_FAILED", Message: "start failed", Retryable: true}
	}}
	engine := newTestEngine(t, executor)
	result, err := engine.Execute(context.Background(), testRequest(t.TempDir()), nil)
	if ErrorCode(err) != "OPENCODE_WORKER_RUNTIME_FAILED" || result.Completion != CompletionFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var runtimeErr *opencode.Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "OPENCODE_START_FAILED" {
		t.Fatalf("runtime cause lost: %v", err)
	}
}

func TestWorkerRuntimeCrashFailureIsStructured(t *testing.T) {
	executor := &fakeExecutor{callFn: func(context.Context, string, opencode.PermissionMode, string, map[string]any) (opencode.ExecutionResult, error) {
		return opencode.ExecutionResult{}, &opencode.Error{Code: "OPENCODE_CALL_FAILED", Message: "runtime exited", Retryable: true}
	}}
	engine := newTestEngine(t, executor)
	result, err := engine.Execute(context.Background(), testRequest(t.TempDir()), nil)
	if ErrorCode(err) != "OPENCODE_WORKER_RUNTIME_FAILED" || result.Completion != CompletionFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestWorkerRejectsInvalidPermissionAndStepLimit(t *testing.T) {
	executor := &fakeExecutor{}
	engine, err := New(executor, Options{MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest(t.TempDir())
	request.PermissionMode = opencode.PermissionMode("admin")
	if _, err := engine.Execute(context.Background(), request, nil); ErrorCode(err) != "OPENCODE_WORKER_PERMISSION_INVALID" {
		t.Fatalf("permission error=%v", err)
	}
	request.PermissionMode = opencode.PermissionProject
	if _, err := engine.Execute(context.Background(), request, nil); ErrorCode(err) != "OPENCODE_WORKER_STEP_LIMIT" {
		t.Fatalf("step limit error=%v", err)
	}
}

func TestWorkerRejectsFilesystemRoot(t *testing.T) {
	executor := &fakeExecutor{}
	engine := newTestEngine(t, executor)
	volume := filepath.VolumeName(t.TempDir())
	root := string(os.PathSeparator)
	if volume != "" {
		root = volume + string(os.PathSeparator)
	}
	request := testRequest(root)
	if _, err := engine.Execute(context.Background(), request, nil); ErrorCode(err) != "OPENCODE_WORKER_WORKSPACE_INVALID" {
		t.Fatalf("root workspace error=%v", err)
	}
}
