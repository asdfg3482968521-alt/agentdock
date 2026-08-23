package opencode

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeLocator struct{ location RuntimeLocation }

func (f fakeLocator) Locate(context.Context) (RuntimeLocation, error) { return f.location, nil }

type fakeLauncher struct {
	mu          sync.Mutex
	connections []*fakeConnection
	launches    []fakeLaunch
	factory     func(int) *fakeConnection
}

type fakeLaunch struct {
	workspace string
	mode      PermissionMode
}

func (f *fakeLauncher) Launch(_ context.Context, _ RuntimeLocation, workspace string, mode PermissionMode, _ string) (RuntimeConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := len(f.launches)
	f.launches = append(f.launches, fakeLaunch{workspace: workspace, mode: mode})
	connection := newFakeConnection()
	if f.factory != nil {
		connection = f.factory(index)
	}
	f.connections = append(f.connections, connection)
	return connection, nil
}

func (f *fakeLauncher) launchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.launches)
}

type fakeConnection struct {
	mu              sync.Mutex
	pid             int
	protocolVersion string
	tools           []Tool
	done            chan struct{}
	exitErr         error
	closed          bool
	calls           []fakeCall
	callFn          func(context.Context, string, map[string]any) (map[string]any, error)
}

type fakeCall struct {
	tool string
	args map[string]any
}

func newFakeConnection() *fakeConnection {
	tools := make([]Tool, 0, len(RequiredTools))
	for _, name := range RequiredTools {
		tools = append(tools, Tool{Name: name})
	}
	return &fakeConnection{pid: 1001, protocolVersion: "test-protocol", tools: tools, done: make(chan struct{})}
}

func (f *fakeConnection) PID() int                { return f.pid }
func (f *fakeConnection) ProtocolVersion() string { return f.protocolVersion }
func (f *fakeConnection) Metrics() ProcessMetrics {
	return ProcessMetrics{Spawn: time.Millisecond, Initialize: 2 * time.Millisecond, ToolsList: 3 * time.Millisecond}
}
func (f *fakeConnection) Done() <-chan struct{} { return f.done }
func (f *fakeConnection) StderrTail() string    { return "" }
func (f *fakeConnection) ExitError() error      { f.mu.Lock(); defer f.mu.Unlock(); return f.exitErr }
func (f *fakeConnection) ListTools(context.Context) ([]Tool, error) {
	return append([]Tool(nil), f.tools...), nil
}

func (f *fakeConnection) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{tool: name, args: cloneAnyMap(args)})
	fn := f.callFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, name, args)
	}
	return map[string]any{"isError": false, "structuredContent": map[string]any{"ok": true}}, nil
}

func (f *fakeConnection) Close(time.Duration) error {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		close(f.done)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeConnection) crash(err error) {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		f.exitErr = err
		close(f.done)
	}
	f.mu.Unlock()
}

func cloneAnyMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func testSupervisor(t *testing.T, launcher *fakeLauncher) *Supervisor {
	t.Helper()
	supervisor, err := NewSupervisor(context.Background(), Options{
		Locator: fakeLocator{location: RuntimeLocation{
			RuntimeVersion: "rc1", VerifiedSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247",
			Executable: `C:\fake\bun.exe`, Cwd: `C:\fake\opencode`, SourceRoot: `C:\fake\opencode`, ProtocolVersion: "test-protocol",
		}},
		Launcher: launcher, LogDir: t.TempDir(), RestartCap: 2, RestartBackoff: 5 * time.Millisecond, ShutdownGrace: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return supervisor
}

func TestSupervisorExecutesThroughOneWorkspaceBoundRuntime(t *testing.T) {
	launcher := &fakeLauncher{}
	supervisor := testSupervisor(t, launcher)
	workspace := t.TempDir()
	arguments := map[string]any{"path": "notes.txt"}

	first, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "read", arguments)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "grep", map[string]any{"pattern": "x"}); err != nil {
		t.Fatal(err)
	}
	if launcher.launchCount() != 1 {
		t.Fatalf("launch count = %d, want 1", launcher.launchCount())
	}
	if first.Tool != "read" || first.PermissionMode != "project" || first.Runtime.State != StateReady {
		t.Fatalf("unexpected execution result: %#v", first)
	}
	connection := launcher.connections[0]
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if len(connection.calls) != 2 || connection.calls[0].tool != "read" || connection.calls[0].args["path"] != "notes.txt" {
		t.Fatalf("proxied calls = %#v", connection.calls)
	}
}

func TestSupervisorRejectsUnexpectedToolSurface(t *testing.T) {
	launcher := &fakeLauncher{factory: func(int) *fakeConnection {
		connection := newFakeConnection()
		connection.tools = connection.tools[:len(connection.tools)-1]
		return connection
	}}
	supervisor := testSupervisor(t, launcher)
	_, err := supervisor.Acquire(context.Background(), t.TempDir(), PermissionProject)
	var runtimeErr *Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "OPENCODE_TOOLS_MISMATCH" {
		t.Fatalf("error = %#v, want OPENCODE_TOOLS_MISMATCH", err)
	}
}

func TestSupervisorPropagatesCancellationToToolCall(t *testing.T) {
	started := make(chan struct{})
	launcher := &fakeLauncher{factory: func(int) *fakeConnection {
		connection := newFakeConnection()
		connection.callFn = func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return connection
	}}
	supervisor := testSupervisor(t, launcher)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := supervisor.Execute(ctx, t.TempDir(), PermissionProject, "bash", map[string]any{"command": "sleep"})
		result <- err
	}()
	<-started
	cancel()
	err := <-result
	var runtimeErr *Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "OPENCODE_ABORTED" {
		t.Fatalf("error = %#v, want OPENCODE_ABORTED", err)
	}
}

func TestSupervisorIsolatesWorkspaceAndPermissionMode(t *testing.T) {
	launcher := &fakeLauncher{}
	supervisor := testSupervisor(t, launcher)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	for _, request := range []struct {
		workspace string
		mode      PermissionMode
	}{
		{workspaceA, PermissionProject},
		{workspaceB, PermissionProject},
		{workspaceA, PermissionFull},
	} {
		if _, err := supervisor.Execute(context.Background(), request.workspace, request.mode, "read", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if launcher.launchCount() != 3 {
		t.Fatalf("launch count = %d, want 3 isolated runtime slots", launcher.launchCount())
	}
	if filepath.Clean(launcher.launches[0].workspace) == filepath.Clean(launcher.launches[1].workspace) {
		t.Fatal("distinct workspaces were collapsed into one runtime")
	}
	if launcher.launches[0].mode == launcher.launches[2].mode {
		t.Fatal("project and full modes were not isolated")
	}
}

func TestSupervisorRestartsAfterUnexpectedExitWithCap(t *testing.T) {
	launcher := &fakeLauncher{}
	supervisor := testSupervisor(t, launcher)
	workspace := t.TempDir()
	if _, err := supervisor.Acquire(context.Background(), workspace, PermissionProject); err != nil {
		t.Fatal(err)
	}
	launcher.connections[0].crash(errors.New("boom"))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		summary, ok := supervisor.Get(workspace, PermissionProject)
		if ok && summary.State == StateReady && summary.RestartCount == 1 && launcher.launchCount() == 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	summary, _ := supervisor.Get(workspace, PermissionProject)
	t.Fatalf("runtime did not recover: summary=%#v launches=%d", summary, launcher.launchCount())
}

func TestSupervisorCloseStopsRuntimeWithoutRestart(t *testing.T) {
	launcher := &fakeLauncher{}
	supervisor := testSupervisor(t, launcher)
	if _, err := supervisor.Acquire(context.Background(), t.TempDir(), PermissionProject); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if launcher.launchCount() != 1 {
		t.Fatalf("runtime restarted during supervisor close; launches=%d", launcher.launchCount())
	}
	if len(supervisor.Summaries()) != 0 {
		t.Fatalf("summaries after close = %#v", supervisor.Summaries())
	}
}
