package opencode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultRestartCap     = 3
	defaultRestartBackoff = 250 * time.Millisecond
	defaultShutdownGrace  = 3 * time.Second
)

type Options struct {
	Locator        RuntimeLocator
	Launcher       RuntimeLauncher
	LogDir         string
	RestartCap     int
	RestartBackoff time.Duration
	ShutdownGrace  time.Duration
}

type Supervisor struct {
	location RuntimeLocation
	launcher RuntimeLauncher
	logDir   string

	restartCap     int
	restartBackoff time.Duration
	shutdownGrace  time.Duration

	mu        sync.RWMutex
	instances map[string]*runtimeInstance
	closing   bool
}

type runtimeInstance struct {
	supervisor *Supervisor
	workspace  string
	mode       PermissionMode

	lifecycleMu      sync.Mutex
	mu               sync.RWMutex
	state            State
	conn             RuntimeConnection
	startedAt        time.Time
	lastError        string
	restartCount     int
	activeCalls      int
	generation       uint64
	restartScheduled bool
	metrics          ProcessMetrics
}

func NewSupervisor(ctx context.Context, options Options) (*Supervisor, error) {
	if options.Locator == nil {
		return nil, newError("OPENCODE_LOCATOR_REQUIRED", "OpenCode runtime locator is required", false, nil, nil)
	}
	if options.Launcher == nil {
		options.Launcher = ProcessLauncher{}
	}
	location, err := options.Locator.Locate(ctx)
	if err != nil {
		return nil, err
	}
	logDir := filepath.Clean(strings.TrimSpace(options.LogDir))
	if logDir == "" || logDir == "." || !filepath.IsAbs(logDir) {
		return nil, newError("OPENCODE_LOG_DIR_INVALID", "OpenCode log directory must be absolute", false, map[string]any{"log_dir": options.LogDir}, nil)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, newError("OPENCODE_LOG_INIT_FAILED", "create OpenCode log directory", false, map[string]any{"log_dir": logDir}, err)
	}
	if options.RestartCap <= 0 {
		options.RestartCap = defaultRestartCap
	}
	if options.RestartBackoff <= 0 {
		options.RestartBackoff = defaultRestartBackoff
	}
	if options.ShutdownGrace <= 0 {
		options.ShutdownGrace = defaultShutdownGrace
	}
	return &Supervisor{
		location: location, launcher: options.Launcher, logDir: logDir,
		restartCap: options.RestartCap, restartBackoff: options.RestartBackoff, shutdownGrace: options.ShutdownGrace,
		instances: map[string]*runtimeInstance{},
	}, nil
}

func (s *Supervisor) Execute(ctx context.Context, workspace string, mode PermissionMode, tool string, arguments map[string]any) (ExecutionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !requiredTool(tool) {
		return ExecutionResult{}, newError("OPENCODE_TOOL_NOT_ALLOWED", "tool is not part of the verified OpenCode runtime surface", false, map[string]any{"tool": tool}, nil)
	}
	if mode != PermissionProject && mode != PermissionFull {
		return ExecutionResult{}, newError("OPENCODE_PERMISSION_INVALID", "permission mode must be project or full", false, map[string]any{"permission_mode": mode}, nil)
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	instance, err := s.Acquire(ctx, workspace, mode)
	if err != nil {
		return ExecutionResult{}, err
	}
	conn, err := instance.beginCall()
	if err != nil {
		return ExecutionResult{}, err
	}
	result, callErr := conn.CallTool(ctx, tool, arguments)
	instance.finishCall(conn)
	if callErr != nil {
		return ExecutionResult{}, normalizeCallError(callErr, instance.summary())
	}
	summary := instance.summary()
	return ExecutionResult{
		Tool: tool, Workspace: summary.Workspace, PermissionMode: string(mode), Runtime: summary, Result: result,
	}, nil
}

// Acquire resolves an explicit workspace and makes its model-free runtime READY.
// It does not widen the workspace and never infers full permission mode.
func (s *Supervisor) Acquire(ctx context.Context, workspace string, mode PermissionMode) (*runtimeInstance, error) {
	canonical, key, err := canonicalWorkspace(workspace, mode)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, newError("OPENCODE_SUPERVISOR_CLOSING", "OpenCode supervisor is shutting down", true, nil, nil)
	}
	instance := s.instances[key]
	if instance == nil {
		instance = &runtimeInstance{supervisor: s, workspace: canonical, mode: mode, state: StateStopped}
		s.instances[key] = instance
	}
	s.mu.Unlock()
	if err := instance.ensureReady(ctx); err != nil {
		return nil, err
	}
	return instance, nil
}

// Release stops and removes one exact workspace+permission runtime slot.
func (s *Supervisor) Release(workspace string, mode PermissionMode) error {
	_, key, err := canonicalWorkspace(workspace, mode)
	if err != nil {
		return err
	}
	s.mu.Lock()
	instance := s.instances[key]
	delete(s.instances, key)
	s.mu.Unlock()
	if instance == nil {
		return nil
	}
	return instance.stop()
}

func (s *Supervisor) Get(workspace string, mode PermissionMode) (Summary, bool) {
	_, key, err := canonicalWorkspace(workspace, mode)
	if err != nil {
		return Summary{}, false
	}
	s.mu.RLock()
	instance := s.instances[key]
	s.mu.RUnlock()
	if instance == nil {
		return Summary{}, false
	}
	return instance.summary(), true
}

func (s *Supervisor) Summaries() []Summary {
	s.mu.RLock()
	instances := make([]*runtimeInstance, 0, len(s.instances))
	for _, instance := range s.instances {
		instances = append(instances, instance)
	}
	s.mu.RUnlock()
	items := make([]Summary, 0, len(instances))
	for _, instance := range instances {
		items = append(items, instance.summary())
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Workspace != items[j].Workspace {
			return items[i].Workspace < items[j].Workspace
		}
		return items[i].PermissionMode < items[j].PermissionMode
	})
	return items
}

func (s *Supervisor) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	instances := make([]*runtimeInstance, 0, len(s.instances))
	for _, instance := range s.instances {
		instances = append(instances, instance)
	}
	s.instances = map[string]*runtimeInstance{}
	s.mu.Unlock()
	var result error
	for _, instance := range instances {
		result = errors.Join(result, instance.stop())
	}
	return result
}

func (s *Supervisor) isClosing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closing
}

func (i *runtimeInstance) ensureReady(ctx context.Context) error {
	i.lifecycleMu.Lock()
	defer i.lifecycleMu.Unlock()
	if i.supervisor.isClosing() {
		return newError("OPENCODE_SUPERVISOR_CLOSING", "OpenCode supervisor is shutting down", true, nil, nil)
	}

	i.mu.RLock()
	state, conn, restarts := i.state, i.conn, i.restartCount
	i.mu.RUnlock()
	if (state == StateReady || state == StateBusy) && conn != nil && connectionAlive(conn) {
		return nil
	}
	if state == StateCrashed && restarts >= i.supervisor.restartCap {
		return newError("OPENCODE_RESTART_LIMIT", "OpenCode runtime restart limit reached", false, map[string]any{"workspace": i.workspace, "restart_count": restarts}, nil)
	}

	restarting := state == StateCrashed || state == StateRestarting
	i.mu.Lock()
	if restarting {
		i.restartCount++
		i.state = StateRestarting
	} else {
		i.state = StateStarting
	}
	i.lastError = ""
	i.mu.Unlock()

	connection, err := i.supervisor.launcher.Launch(ctx, i.supervisor.location, i.workspace, i.mode, i.supervisor.logDir)
	if err != nil {
		i.markCrashed(err)
		return err
	}
	if expected := strings.TrimSpace(i.supervisor.location.ProtocolVersion); expected != "" && connection.ProtocolVersion() != expected {
		_ = connection.Close(i.supervisor.shutdownGrace)
		err := newError("OPENCODE_PROTOCOL_MISMATCH", "OpenCode MCP protocol version does not match verified manifest", false, map[string]any{"expected": expected, "actual": connection.ProtocolVersion()}, nil)
		i.markCrashed(err)
		return err
	}
	tools, err := connection.ListTools(ctx)
	if err != nil {
		_ = connection.Close(i.supervisor.shutdownGrace)
		i.markCrashed(err)
		return err
	}
	if err := verifyTools(tools); err != nil {
		_ = connection.Close(i.supervisor.shutdownGrace)
		i.markCrashed(err)
		return err
	}

	i.mu.Lock()
	i.conn = connection
	i.startedAt = time.Now().UTC()
	i.state = StateReady
	i.lastError = ""
	i.metrics = connection.Metrics()
	i.generation++
	generation := i.generation
	i.restartScheduled = false
	i.mu.Unlock()
	slog.Info("OpenCode runtime ready", "workspace", i.workspace, "permission_mode", i.mode, "pid", connection.PID(), "sha", i.supervisor.location.VerifiedSHA)
	go i.watch(connection, generation)
	return nil
}

func (i *runtimeInstance) beginCall() (RuntimeConnection, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.conn == nil || (i.state != StateReady && i.state != StateBusy) || !connectionAlive(i.conn) {
		return nil, newError("OPENCODE_RUNTIME_NOT_READY", "OpenCode runtime is not ready", true, map[string]any{"workspace": i.workspace, "state": i.state}, nil)
	}
	i.activeCalls++
	i.state = StateBusy
	return i.conn, nil
}

func (i *runtimeInstance) finishCall(connection RuntimeConnection) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.activeCalls > 0 {
		i.activeCalls--
	}
	if i.conn == connection && (i.state == StateBusy || i.state == StateReady) {
		if i.activeCalls == 0 {
			i.state = StateReady
		} else {
			i.state = StateBusy
		}
	}
}

func (i *runtimeInstance) watch(connection RuntimeConnection, generation uint64) {
	<-connection.Done()
	i.lifecycleMu.Lock()
	defer i.lifecycleMu.Unlock()
	i.mu.RLock()
	current := i.conn == connection && i.generation == generation
	state := i.state
	i.mu.RUnlock()
	if !current || state == StateStopping || state == StateStopped || i.supervisor.isClosing() {
		return
	}
	exitErr := connection.ExitError()
	message := "OpenCode runtime exited unexpectedly"
	if exitErr != nil {
		message = exitErr.Error()
	}
	if stderr := connection.StderrTail(); stderr != "" {
		message = message + ": " + stderr
	}
	_ = connection.Close(0)
	i.mu.Lock()
	if i.conn == connection && i.generation == generation {
		i.conn = nil
		i.state = StateCrashed
		i.lastError = message
	}
	i.mu.Unlock()
	slog.Warn("OpenCode runtime crashed", "workspace", i.workspace, "permission_mode", i.mode, "error", message)
	i.scheduleRestart()
}

func (i *runtimeInstance) markCrashed(err error) {
	i.mu.Lock()
	i.conn = nil
	i.state = StateCrashed
	i.lastError = err.Error()
	i.mu.Unlock()
	slog.Warn("OpenCode runtime failed", "workspace", i.workspace, "permission_mode", i.mode, "error", err)
	i.scheduleRestart()
}

func (i *runtimeInstance) scheduleRestart() {
	i.mu.Lock()
	if i.restartScheduled || i.restartCount >= i.supervisor.restartCap || i.state != StateCrashed || i.supervisor.isClosing() {
		i.mu.Unlock()
		return
	}
	i.restartScheduled = true
	attempt := i.restartCount + 1
	i.mu.Unlock()
	delay := i.supervisor.restartBackoff
	for n := 1; n < attempt && n < 6; n++ {
		delay *= 2
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		i.mu.Lock()
		i.restartScheduled = false
		i.mu.Unlock()
		if i.supervisor.isClosing() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_ = i.ensureReady(ctx)
	}()
}

func (i *runtimeInstance) stop() error {
	i.lifecycleMu.Lock()
	defer i.lifecycleMu.Unlock()
	i.mu.Lock()
	if i.state == StateStopped {
		i.mu.Unlock()
		return nil
	}
	i.state = StateStopping
	i.restartScheduled = false
	connection := i.conn
	i.mu.Unlock()
	started := time.Now()
	var err error
	if connection != nil {
		err = connection.Close(i.supervisor.shutdownGrace)
	}
	i.mu.Lock()
	i.conn = nil
	i.activeCalls = 0
	i.state = StateStopped
	if err != nil {
		i.lastError = err.Error()
	} else {
		i.lastError = ""
	}
	i.mu.Unlock()
	slog.Info("OpenCode runtime stopped", "workspace", i.workspace, "permission_mode", i.mode, "shutdown_ms", time.Since(started).Milliseconds(), "error", err)
	return err
}

func (i *runtimeInstance) summary() Summary {
	i.mu.RLock()
	defer i.mu.RUnlock()
	pid := 0
	protocolVersion := ""
	stderr := ""
	if i.conn != nil {
		pid = i.conn.PID()
		protocolVersion = i.conn.ProtocolVersion()
		stderr = i.conn.StderrTail()
	}
	startedAt := ""
	if !i.startedAt.IsZero() {
		startedAt = i.startedAt.Format(time.RFC3339Nano)
	}
	return Summary{
		Workspace: i.workspace, PermissionMode: string(i.mode), State: i.state,
		RuntimePath: i.supervisor.location.Executable, RuntimeVersion: i.supervisor.location.RuntimeVersion,
		RuntimeSHA: i.supervisor.location.VerifiedSHA, ProtocolVersion: protocolVersion, PID: pid, StartedAt: startedAt,
		Health: healthForState(i.state), RestartCount: i.restartCount, ActiveCalls: i.activeCalls, LastError: i.lastError, StderrTail: stderr,
		SpawnMS: i.metrics.Spawn.Milliseconds(), InitializeMS: i.metrics.Initialize.Milliseconds(), ToolsListMS: i.metrics.ToolsList.Milliseconds(),
	}
}

func connectionAlive(connection RuntimeConnection) bool {
	if connection == nil {
		return false
	}
	select {
	case <-connection.Done():
		return false
	default:
		return true
	}
}

func healthForState(state State) string {
	switch state {
	case StateReady, StateBusy:
		return "healthy"
	case StateCrashed:
		return "unhealthy"
	case StateStarting, StateRestarting, StateStopping:
		return "transitioning"
	default:
		return "stopped"
	}
}

func requiredTool(name string) bool {
	for _, required := range RequiredTools {
		if name == required {
			return true
		}
	}
	return false
}

func verifyTools(tools []Tool) error {
	actual := make([]string, 0, len(tools))
	seen := map[string]struct{}{}
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return newError("OPENCODE_TOOLS_INVALID", "OpenCode tools/list returned an empty tool name", false, nil, nil)
		}
		if _, exists := seen[name]; exists {
			return newError("OPENCODE_TOOLS_INVALID", "OpenCode tools/list returned duplicate tool names", false, map[string]any{"tool": name}, nil)
		}
		seen[name] = struct{}{}
		actual = append(actual, name)
	}
	expected := append([]string(nil), RequiredTools...)
	sort.Strings(actual)
	sort.Strings(expected)
	if len(actual) != len(expected) {
		return newError("OPENCODE_TOOLS_MISMATCH", "OpenCode runtime tool surface does not match the verified nine-tool contract", false, map[string]any{"expected": expected, "actual": actual}, nil)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return newError("OPENCODE_TOOLS_MISMATCH", "OpenCode runtime tool surface does not match the verified nine-tool contract", false, map[string]any{"expected": expected, "actual": actual}, nil)
		}
	}
	return nil
}

func canonicalWorkspace(raw string, mode PermissionMode) (string, string, error) {
	if mode != PermissionProject && mode != PermissionFull {
		return "", "", newError("OPENCODE_PERMISSION_INVALID", "permission mode must be project or full", false, map[string]any{"permission_mode": mode}, nil)
	}
	workspace := strings.TrimSpace(raw)
	if workspace == "" {
		return "", "", newError("OPENCODE_WORKSPACE_REQUIRED", "OpenCode workspace is required", false, nil, nil)
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", newError("OPENCODE_WORKSPACE_INVALID", "resolve OpenCode workspace", false, map[string]any{"workspace": raw}, err)
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", newError("OPENCODE_WORKSPACE_INVALID", "resolve OpenCode workspace symlinks", false, map[string]any{"workspace": absolute}, err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", "", newError("OPENCODE_WORKSPACE_INVALID", "OpenCode workspace must be an existing directory", false, map[string]any{"workspace": real}, err)
	}
	if filepath.Dir(real) == real {
		return "", "", newError("OPENCODE_WORKSPACE_INVALID", "refusing to use filesystem root as OpenCode workspace", false, map[string]any{"workspace": real}, nil)
	}
	keyPath := filepath.Clean(real)
	if runtime.GOOS == "windows" {
		keyPath = strings.ToLower(keyPath)
	}
	return real, fmt.Sprintf("%s\x00%s", keyPath, mode), nil
}

func normalizeCallError(err error, summary Summary) error {
	if err == nil {
		return nil
	}
	var runtimeErr *Error
	if errors.As(err, &runtimeErr) {
		if runtimeErr.Details == nil {
			runtimeErr.Details = map[string]any{}
		}
		runtimeErr.Details["runtime"] = summary
		return runtimeErr
	}
	code, message, retryable := "OPENCODE_CALL_FAILED", "OpenCode tool call failed", true
	if errors.Is(err, context.Canceled) {
		code, message, retryable = "OPENCODE_ABORTED", "OpenCode tool call cancelled", false
	} else if errors.Is(err, context.DeadlineExceeded) {
		code, message = "OPENCODE_TIMEOUT", "OpenCode tool call timed out"
	}
	return newError(code, message, retryable, map[string]any{"runtime": summary}, err)
}
