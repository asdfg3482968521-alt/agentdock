package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	processcontrol "github.com/uvwt/agentdock/internal/process"
)

type ProcessLauncher struct{}

func (ProcessLauncher) Launch(ctx context.Context, location RuntimeLocation, workspace string, mode PermissionMode, logDir string) (RuntimeConnection, error) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, newError("OPENCODE_LOG_INIT_FAILED", "create OpenCode runtime log directory", false, map[string]any{"log_dir": logDir}, err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("runtime-%s-%d.stderr.log", safeLogName(filepath.Base(workspace)), time.Now().UTC().UnixNano()))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, newError("OPENCODE_LOG_INIT_FAILED", "open OpenCode stderr log", false, map[string]any{"log_path": logPath}, err)
	}
	tail := newTailBuffer(64 << 10)
	args := append([]string(nil), location.Args...)
	args = append(args, "mcp-serve", workspace, "--permission-mode", string(mode))
	cmd := exec.Command(location.Executable, args...)
	cmd.Dir = location.Cwd
	cmd.Env = append(os.Environ(), "OPENCODE_DISABLE_LSP_DOWNLOAD=1")
	cmd.Stderr = io.MultiWriter(logFile, tail)
	processcontrol.Configure(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, newError("OPENCODE_START_FAILED", "open OpenCode stdout pipe", true, nil, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, newError("OPENCODE_START_FAILED", "open OpenCode stdin pipe", true, nil, err)
	}

	spawnStarted := time.Now()
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, newError("OPENCODE_START_FAILED", "start OpenCode model-free runtime", true, map[string]any{"executable": location.Executable}, err)
	}
	spawnDuration := time.Since(spawnStarted)
	controller, err := processcontrol.Attach(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		return nil, newError("OPENCODE_START_FAILED", "attach OpenCode runtime process controller", true, map[string]any{"pid": cmd.Process.Pid}, err)
	}

	connection := &processConnection{
		location: location, cmd: cmd, controller: controller, stdin: stdin, stdout: stdout,
		stderr: tail, logFile: logFile, logPath: logPath, done: make(chan struct{}),
		metrics: ProcessMetrics{Spawn: spawnDuration},
	}
	go connection.wait()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agentdock-opencode-supervisor", Version: "1"}, &mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}})
	initializeStarted := time.Now()
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{Reader: stdout, Writer: stdin}, nil)
	connection.metricsMu.Lock()
	connection.metrics.Initialize = time.Since(initializeStarted)
	connection.metricsMu.Unlock()
	if err != nil {
		_ = connection.Close(0)
		return nil, newError("OPENCODE_INITIALIZE_FAILED", "initialize OpenCode MCP runtime", true, map[string]any{"pid": cmd.Process.Pid, "stderr": tail.String()}, err)
	}
	initialized := session.InitializeResult()
	if initialized == nil || strings.TrimSpace(initialized.ProtocolVersion) == "" {
		connection.session = session
		_ = connection.Close(0)
		return nil, newError("OPENCODE_INITIALIZE_FAILED", "OpenCode initialize response omitted protocolVersion", false, map[string]any{"pid": cmd.Process.Pid}, nil)
	}
	connection.mu.Lock()
	connection.session = session
	connection.protocolVersion = strings.TrimSpace(initialized.ProtocolVersion)
	connection.mu.Unlock()
	return connection, nil
}

func safeLogName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == string(filepath.Separator) {
		return "workspace"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "workspace"
	}
	return builder.String()
}

type processConnection struct {
	location   RuntimeLocation
	cmd        *exec.Cmd
	controller *processcontrol.Controller
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     *tailBuffer
	logFile    *os.File
	logPath    string

	mu              sync.RWMutex
	session         *mcpsdk.ClientSession
	protocolVersion string
	exitErr         error
	done            chan struct{}

	metricsMu sync.RWMutex
	metrics   ProcessMetrics
	closeOnce sync.Once
	closeErr  error
}

func (c *processConnection) PID() int {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

func (c *processConnection) ProtocolVersion() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.protocolVersion
}

func (c *processConnection) Metrics() ProcessMetrics {
	c.metricsMu.RLock()
	defer c.metricsMu.RUnlock()
	return c.metrics
}

func (c *processConnection) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return nil, newError("OPENCODE_CONNECTION_CLOSED", "OpenCode MCP session is not initialized", true, nil, nil)
	}
	started := time.Now()
	tools := make([]Tool, 0, len(RequiredTools))
	for remote, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, wrapConnectionError("OPENCODE_TOOLS_LIST_FAILED", "list OpenCode MCP tools", err, c)
		}
		if remote == nil || strings.TrimSpace(remote.Name) == "" {
			return nil, newError("OPENCODE_TOOLS_INVALID", "OpenCode tools/list returned an empty tool name", false, map[string]any{"pid": c.PID()}, nil)
		}
		tools = append(tools, Tool{Name: strings.TrimSpace(remote.Name)})
	}
	c.metricsMu.Lock()
	c.metrics.ToolsList = time.Since(started)
	c.metricsMu.Unlock()
	return tools, nil
}

func (c *processConnection) CallTool(ctx context.Context, name string, arguments map[string]any) (map[string]any, error) {
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return nil, newError("OPENCODE_CONNECTION_CLOSED", "OpenCode MCP session is closed", true, map[string]any{"pid": c.PID()}, nil)
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, wrapConnectionError("OPENCODE_CALL_FAILED", "call OpenCode MCP tool", err, c)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, newError("OPENCODE_INVALID_RESPONSE", "encode OpenCode tool result", false, map[string]any{"tool": name}, err)
	}
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, newError("OPENCODE_INVALID_RESPONSE", "decode OpenCode tool result", false, map[string]any{"tool": name}, err)
	}
	return output, nil
}

func (c *processConnection) Close(grace time.Duration) error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		session := c.session
		c.session = nil
		c.mu.Unlock()
		if session != nil {
			c.closeErr = errors.Join(c.closeErr, session.Close())
		} else if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if grace < 0 {
			grace = 0
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-c.done:
		case <-timer.C:
			if c.controller != nil {
				if err := c.controller.Terminate(); err != nil {
					select {
					case <-c.done:
					default:
						c.closeErr = errors.Join(c.closeErr, err)
					}
				}
			}
			<-c.done
		}
		if c.controller != nil {
			c.closeErr = errors.Join(c.closeErr, c.controller.Close())
			c.controller = nil
		}
		if c.stdout != nil {
			_ = c.stdout.Close()
		}
		if c.logFile != nil {
			c.closeErr = errors.Join(c.closeErr, c.logFile.Close())
			c.logFile = nil
		}
	})
	return c.closeErr
}

func (c *processConnection) Done() <-chan struct{} { return c.done }

func (c *processConnection) ExitError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.exitErr
}

func (c *processConnection) StderrTail() string {
	if c == nil || c.stderr == nil {
		return ""
	}
	return c.stderr.String()
}

func (c *processConnection) wait() {
	err := c.cmd.Wait()
	c.mu.Lock()
	c.exitErr = err
	c.mu.Unlock()
	close(c.done)
}

func wrapConnectionError(code, message string, err error, c *processConnection) error {
	retryable := true
	if errors.Is(err, context.Canceled) {
		code, message, retryable = "OPENCODE_ABORTED", "OpenCode tool call cancelled", false
	} else if errors.Is(err, context.DeadlineExceeded) {
		code, message = "OPENCODE_TIMEOUT", "OpenCode tool call timed out"
	}
	return newError(code, message, retryable, map[string]any{"pid": c.PID(), "stderr": c.StderrTail()}, err)
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(value), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}
