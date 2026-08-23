//go:build windows

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExternalAgentDockOpenCodeWindowsE2E(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_E2E_BINARY"))
	manifest := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if binary == "" || manifest == "" {
		t.Skip("set AGENTDOCK_OPENCODE_E2E_BINARY and AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST for the external Windows E2E")
	}

	root := t.TempDir()
	workspace := filepath.Join(root, "project with spaces")
	outside := filepath.Join(root, "outside")
	agentDockHome := filepath.Join(root, "agentdock-home")
	agentDockDefault := filepath.Join(root, "agentdock-default")
	openCodeLogs := filepath.Join(root, "opencode-logs")
	for _, dir := range []string{workspace, outside, agentDockHome, agentDockDefault, openCodeLogs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	mustWriteOuter(t, filepath.Join(workspace, "read.txt"), "outer-read-token\n")
	mustWriteOuter(t, filepath.Join(workspace, "test.py"), "def outer_symbol():\n    return 7\n")
	outsideFile := filepath.Join(outside, "outside.txt")
	mustWriteOuter(t, outsideFile, "outside-full-token\n")
	writeCancellationFixtures(t, workspace)
	gitOuter(t, workspace, "init")
	gitOuter(t, workspace, "config", "user.email", "c1a@example.invalid")
	gitOuter(t, workspace, "config", "user.name", "C1-A Outer E2E")
	gitOuter(t, workspace, "add", ".")
	gitOuter(t, workspace, "commit", "-m", "baseline")

	cmd := exec.Command(binary, "--stdio")
	cmd.Dir = agentDockDefault
	cmd.Env = append(os.Environ(),
		"AGENTDOCK_HOME="+agentDockHome,
		"AGENTDOCK_DEFAULT_DIR="+agentDockDefault,
		"AGENTDOCK_OPENCODE_ENABLED=1",
		"AGENTDOCK_OPENCODE_RUNTIME_MANIFEST="+manifest,
		"AGENTDOCK_OPENCODE_LOG_DIR="+openCodeLogs,
		"AGENTDOCK_LOG_LEVEL=info",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderrBuffer bytes.Buffer
	go func() { _, _ = io.Copy(&stderrBuffer, stderr) }()
	var rawProtocol bytes.Buffer
	protocolReader := &teeReadCloser{Reader: io.TeeReader(stdout, &rawProtocol), Closer: stdout}

	agentDockStarted := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	agentDockPID := cmd.Process.Pid
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	processClosed := false
	t.Cleanup(func() {
		if processClosed {
			return
		}
		_ = stdin.Close()
		select {
		case <-processDone:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-processDone
		}
	})

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "agentdock-opencode-outer-e2e", Version: "1"},
		&mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}},
	)
	connectStarted := time.Now()
	session, err := client.Connect(t.Context(), &mcpsdk.IOTransport{Reader: protocolReader, Writer: stdin}, nil)
	if err != nil {
		t.Fatalf("external initialize failed: %v\nstderr:\n%s", err, stderrBuffer.String())
	}
	externalInitialize := time.Since(connectStarted)
	agentDockSpawnToInitialize := time.Since(agentDockStarted)
	initialized := session.InitializeResult()
	if initialized == nil || strings.TrimSpace(initialized.ProtocolVersion) == "" {
		t.Fatal("AgentDock external initialize omitted protocolVersion")
	}

	listStarted := time.Now()
	outerTools := map[string]bool{}
	for tool, toolErr := range session.Tools(t.Context(), nil) {
		if toolErr != nil {
			t.Fatalf("external tools/list failed: %v", toolErr)
		}
		outerTools[tool.Name] = true
	}
	externalToolsList := time.Since(listStarted)
	requiredOuter := []string{
		"opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit",
		"opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git",
	}
	for _, name := range requiredOuter {
		if !outerTools[name] {
			t.Fatalf("external tools/list missing %s", name)
		}
	}

	call := func(ctx context.Context, tool, mode string, arguments map[string]any) (*mcpsdk.CallToolResult, time.Duration) {
		t.Helper()
		started := time.Now()
		result, callErr := session.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: tool,
			Arguments: map[string]any{
				"workspace": workspace, "permission_mode": mode, "arguments": arguments,
			},
		})
		duration := time.Since(started)
		if callErr != nil {
			t.Fatalf("%s transport call failed: %v", tool, callErr)
		}
		return result, duration
	}

	read, firstRead := call(t.Context(), "opencode_read", "project", map[string]any{"filePath": filepath.Join(workspace, "read.txt")})
	assertOuterSuccess(t, "opencode_read", read)
	assertOuterContains(t, "opencode_read", read, "outer-read-token")
	coldSummary := outerRuntimeSummary(t, read)
	oldRuntimePID := intNumber(coldSummary["pid"])
	if oldRuntimePID <= 0 {
		t.Fatalf("invalid runtime pid in first read: %#v", coldSummary)
	}

	warmReadResult, warmRead := call(t.Context(), "opencode_read", "project", map[string]any{"filePath": filepath.Join(workspace, "read.txt")})
	assertOuterSuccess(t, "warm opencode_read", warmReadResult)

	write, _ := call(t.Context(), "opencode_write", "project", map[string]any{
		"filePath": filepath.Join(workspace, "created.txt"), "content": "write-value\n",
	})
	assertOuterSuccess(t, "opencode_write", write)
	mustContainOuterFile(t, filepath.Join(workspace, "created.txt"), "write-value")

	edit, _ := call(t.Context(), "opencode_edit", "project", map[string]any{
		"filePath": filepath.Join(workspace, "created.txt"), "oldString": "write-value", "newString": "edit-value",
	})
	assertOuterSuccess(t, "opencode_edit", edit)
	mustContainOuterFile(t, filepath.Join(workspace, "created.txt"), "edit-value")

	patchText := "*** Begin Patch\n*** Update File: created.txt\n@@\n-edit-value\n+patch-value\n*** End Patch"
	patch, _ := call(t.Context(), "opencode_apply_patch", "project", map[string]any{"patchText": patchText})
	assertOuterSuccess(t, "opencode_apply_patch", patch)
	mustContainOuterFile(t, filepath.Join(workspace, "created.txt"), "patch-value")

	glob, _ := call(t.Context(), "opencode_glob", "project", map[string]any{"pattern": "**/*.txt"})
	assertOuterSuccess(t, "opencode_glob", glob)
	assertOuterContains(t, "opencode_glob", glob, "created.txt")

	grep, _ := call(t.Context(), "opencode_grep", "project", map[string]any{"pattern": "patch-value", "path": workspace})
	assertOuterSuccess(t, "opencode_grep", grep)
	assertOuterContains(t, "opencode_grep", grep, "patch-value")

	bash, bashEcho := call(t.Context(), "opencode_bash", "project", map[string]any{"command": "echo outer-bash-ok"})
	assertOuterSuccess(t, "opencode_bash", bash)
	assertOuterContains(t, "opencode_bash", bash, "outer-bash-ok")

	beforeLSP, _ := os.ReadDir(workspace)
	lsp, _ := call(t.Context(), "opencode_lsp", "project", map[string]any{
		"operation": "documentSymbol", "filePath": filepath.Join(workspace, "test.py"), "line": 1, "character": 1,
	})
	if lsp.IsError {
		assertOuterContains(t, "opencode_lsp offline", lsp, "No LSP server available")
	}
	afterLSP, _ := os.ReadDir(workspace)
	if !sameOuterNames(beforeLSP, afterLSP) {
		t.Fatalf("opencode_lsp mutated workspace while local server was unavailable: before=%v after=%v", namesOuter(beforeLSP), namesOuter(afterLSP))
	}

	gitRepository, gitRepositoryDuration := call(t.Context(), "opencode_git", "project", map[string]any{"operation": "repository"})
	assertOuterSuccess(t, "opencode_git repository", gitRepository)
	gitStatus, _ := call(t.Context(), "opencode_git", "project", map[string]any{"operation": "status"})
	assertOuterSuccess(t, "opencode_git status", gitStatus)
	gitDiff, _ := call(t.Context(), "opencode_git", "project", map[string]any{"operation": "diff", "mode": "working", "path": "created.txt"})
	assertOuterSuccess(t, "opencode_git diff", gitDiff)

	projectOutside, _ := call(t.Context(), "opencode_read", "project", map[string]any{"filePath": outsideFile})
	if !projectOutside.IsError {
		t.Fatalf("project mode unexpectedly allowed outside read: %s", outerJSON(projectOutside))
	}
	assertOuterContains(t, "project isolation", projectOutside, "OUTSIDE_WORKSPACE")

	fullOutside, _ := call(t.Context(), "opencode_read", "full", map[string]any{"filePath": outsideFile})
	assertOuterSuccess(t, "full mode outside read", fullOutside)
	assertOuterContains(t, "full mode outside read", fullOutside, "outside-full-token")
	fullSummary := outerRuntimeSummary(t, fullOutside)
	fullRuntimePID := intNumber(fullSummary["pid"])
	if fullRuntimePID <= 0 || fullRuntimePID == oldRuntimePID {
		t.Fatalf("full mode did not use isolated runtime slot: project=%d full=%d", oldRuntimePID, fullRuntimePID)
	}

	childPIDFile := filepath.Join(workspace, "child.pid")
	grandchildPIDFile := filepath.Join(workspace, "grandchild.pid")
	marker := filepath.Join(workspace, "cancel-marker.txt")
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelResult := make(chan struct {
		result *mcpsdk.CallToolResult
		err    error
	}, 1)
	go func() {
		result, callErr := session.CallTool(cancelCtx, &mcpsdk.CallToolParams{
			Name: "opencode_bash",
			Arguments: map[string]any{
				"workspace":       workspace,
				"permission_mode": "project",
				"arguments": map[string]any{
					"command": "powershell.exe -NoProfile -NonInteractive -Command \"node cancel-parent.cjs child.pid grandchild.pid cancel-marker.txt\"",
				},
			},
		})
		cancelResult <- struct {
			result *mcpsdk.CallToolResult
			err    error
		}{result: result, err: callErr}
	}()
	childPID := waitOuterPIDFile(t, childPIDFile, 8*time.Second)
	grandchildPID := waitOuterPIDFile(t, grandchildPIDFile, 8*time.Second)
	cancelStarted := time.Now()
	cancel()
	cancelled := <-cancelResult
	cancelDuration := time.Since(cancelStarted)
	if cancelled.err == nil && (cancelled.result == nil || !cancelled.result.IsError) {
		t.Fatalf("outer cancellation was not surfaced: result=%s err=%v", outerJSON(cancelled.result), cancelled.err)
	}
	waitOuterPIDGone(t, childPID, 8*time.Second)
	waitOuterPIDGone(t, grandchildPID, 8*time.Second)
	time.Sleep(750 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled descendant generated marker: err=%v", err)
	}

	reused, _ := call(t.Context(), "opencode_read", "project", map[string]any{"filePath": filepath.Join(workspace, "read.txt")})
	assertOuterSuccess(t, "read after outer cancellation", reused)
	reusedSummary := outerRuntimeSummary(t, reused)
	if intNumber(reusedSummary["pid"]) != oldRuntimePID {
		t.Fatalf("outer cancellation replaced healthy runtime unexpectedly: old=%d now=%d", oldRuntimePID, intNumber(reusedSummary["pid"]))
	}

	crashStarted := time.Now()
	process, err := os.FindProcess(oldRuntimePID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("terminate only OpenCode runtime pid %d: %v", oldRuntimePID, err)
	}
	seenCrashed := false
	seenRestarting := false
	newRuntimePID := 0
	restartCount := 0
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		info, infoErr := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
		if infoErr == nil && info != nil && !info.IsError {
			for _, runtimeSummary := range outerRuntimeSummaries(info) {
				if !strings.EqualFold(filepath.Clean(stringValue(runtimeSummary["workspace"])), filepath.Clean(workspace)) || stringValue(runtimeSummary["permission_mode"]) != "project" {
					continue
				}
				switch stringValue(runtimeSummary["state"]) {
				case "CRASHED":
					seenCrashed = true
				case "RESTARTING":
					seenRestarting = true
				case "READY":
					pid := intNumber(runtimeSummary["pid"])
					count := intNumber(runtimeSummary["restart_count"])
					if pid > 0 && pid != oldRuntimePID && count == 1 {
						newRuntimePID = pid
						restartCount = count
					}
				}
			}
		}
		if newRuntimePID > 0 && seenCrashed && seenRestarting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	restartLatency := time.Since(crashStarted)
	if !seenCrashed || !seenRestarting || newRuntimePID <= 0 || restartCount != 1 {
		t.Fatalf("real restart incomplete: crashed=%v restarting=%v new_pid=%d restart_count=%d", seenCrashed, seenRestarting, newRuntimePID, restartCount)
	}
	waitOuterPIDGone(t, oldRuntimePID, 5*time.Second)

	outerToolsAfterRestart := map[string]bool{}
	for tool, toolErr := range session.Tools(t.Context(), nil) {
		if toolErr != nil {
			t.Fatalf("tools/list after runtime restart: %v", toolErr)
		}
		outerToolsAfterRestart[tool.Name] = true
	}
	for _, name := range requiredOuter {
		if !outerToolsAfterRestart[name] {
			t.Fatalf("tools/list after restart missing %s", name)
		}
	}
	afterRestart, _ := call(t.Context(), "opencode_read", "project", map[string]any{"filePath": filepath.Join(workspace, "read.txt")})
	assertOuterSuccess(t, "read after real runtime restart", afterRestart)
	if intNumber(outerRuntimeSummary(t, afterRestart)["pid"]) != newRuntimePID {
		t.Fatalf("read after restart used unexpected runtime pid")
	}

	shutdownStarted := time.Now()
	if err := session.Close(); err != nil {
		t.Fatalf("close external MCP session: %v", err)
	}
	select {
	case waitErr := <-processDone:
		processClosed = true
		if waitErr != nil {
			t.Fatalf("AgentDock process exit: %v\nstderr:\n%s", waitErr, stderrBuffer.String())
		}
	case <-time.After(12 * time.Second):
		t.Fatal("AgentDock did not exit after external MCP transport closed")
	}
	shutdownDuration := time.Since(shutdownStarted)
	waitOuterPIDGone(t, agentDockPID, 5*time.Second)
	waitOuterPIDGone(t, newRuntimePID, 5*time.Second)
	waitOuterPIDGone(t, fullRuntimePID, 5*time.Second)
	waitOuterPIDGone(t, childPID, 5*time.Second)
	waitOuterPIDGone(t, grandchildPID, 5*time.Second)

	assertProtocolJSONLines(t, rawProtocol.Bytes())
	logEntries, err := os.ReadDir(openCodeLogs)
	if err != nil {
		t.Fatalf("read OpenCode log sink: %v", err)
	}
	if len(logEntries) == 0 {
		t.Fatal("OpenCode stderr log sink stayed empty/uncreated")
	}

	t.Logf("OUTER_E2E agentdock_pid=%d agentdock_spawn_to_initialize=%s external_initialize=%s external_tools_list=%s", agentDockPID, agentDockSpawnToInitialize, externalInitialize, externalToolsList)
	t.Logf("PERF supervisor_acquire_plus_first_read=%s warm_opencode_read=%s runtime_spawn=%sms runtime_initialize=%sms runtime_tools_list=%sms bash_echo=%s git_repository=%s shutdown=%s", firstRead, warmRead, stringNumber(coldSummary["spawn_ms"]), stringNumber(coldSummary["initialize_ms"]), stringNumber(coldSummary["tools_list_ms"]), bashEcho, gitRepositoryDuration, shutdownDuration)
	t.Logf("CANCEL child_pid=%d grandchild_pid=%d cancel_return=%s residual=0 reuse_pid=%d", childPID, grandchildPID, cancelDuration, oldRuntimePID)
	t.Logf("RESTART old_pid=%d new_pid=%d count=%d latency=%s observed=CRASHED->RESTARTING->READY", oldRuntimePID, newRuntimePID, restartCount, restartLatency)
	t.Logf("SHUTDOWN agentdock_pid=%d project_runtime_pid=%d full_runtime_pid=%d residual=0 protocol_json_lines=PASS opencode_log_files=%d", agentDockPID, newRuntimePID, fullRuntimePID, len(logEntries))
}

func writeCancellationFixtures(t *testing.T, workspace string) {
	t.Helper()
	parent := `const fs=require("fs"); const cp=require("child_process"); const [childPidFile,grandPidFile,marker]=process.argv.slice(2); fs.writeFileSync(childPidFile,String(process.pid)); const child=cp.spawn(process.execPath,["cancel-grandchild.cjs",grandPidFile,marker],{stdio:"ignore"}); child.on("exit",()=>{}); setTimeout(()=>{},10000);`
	grandchild := `const fs=require("fs"); const [pidFile,marker]=process.argv.slice(2); fs.writeFileSync(pidFile,String(process.pid)); setTimeout(()=>fs.writeFileSync(marker,"should-not-exist"),4000); setTimeout(()=>{},10000);`
	mustWriteOuter(t, filepath.Join(workspace, "cancel-parent.cjs"), parent)
	mustWriteOuter(t, filepath.Join(workspace, "cancel-grandchild.cjs"), grandchild)
}

func assertOuterSuccess(t *testing.T, label string, result *mcpsdk.CallToolResult) {
	t.Helper()
	if result == nil || result.IsError {
		t.Fatalf("%s failed: %s", label, outerJSON(result))
	}
}

func assertOuterContains(t *testing.T, label string, result *mcpsdk.CallToolResult, needle string) {
	t.Helper()
	if !strings.Contains(outerJSON(result), needle) {
		t.Fatalf("%s missing %q: %s", label, needle, outerJSON(result))
	}
}

func outerRuntimeSummary(t *testing.T, result *mcpsdk.CallToolResult) map[string]any {
	t.Helper()
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type = %T", result.StructuredContent)
	}
	runtimeSummary, ok := structured["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("runtime summary = %#v", structured["runtime"])
	}
	return runtimeSummary
}

func outerRuntimeSummaries(result *mcpsdk.CallToolResult) []map[string]any {
	structured, _ := result.StructuredContent.(map[string]any)
	items, _ := structured["opencode_runtimes"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			out = append(out, mapped)
		}
	}
	return out
}

func mustWriteOuter(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustContainOuterFile(t *testing.T, path, needle string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), needle) {
		t.Fatalf("file %s content=%q err=%v", path, content, err)
	}
}

func gitOuter(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func waitOuterPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for PID file %s", path)
	return 0
}

func waitOuterPIDGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 1 } else { exit 0 }", pid))
		if err := cmd.Run(); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("PID %d remained alive after %s", pid, timeout)
}

func assertProtocolJSONLines(t *testing.T, raw []byte) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("AgentDock MCP stdout was empty")
	}
	for index, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatalf("AgentDock MCP stdout line %d is not JSON-RPC: %v\n%s", index+1, err, line)
		}
		if value["jsonrpc"] != "2.0" {
			t.Fatalf("AgentDock MCP stdout line %d missing jsonrpc=2.0: %s", index+1, line)
		}
	}
}

func sameOuterNames(left, right []os.DirEntry) bool {
	leftNames := namesOuter(left)
	rightNames := namesOuter(right)
	if len(leftNames) != len(rightNames) {
		return false
	}
	for index := range leftNames {
		if leftNames[index] != rightNames[index] {
			return false
		}
	}
	return true
}

func namesOuter(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

func intNumber(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(string(typed))
		return parsed
	default:
		parsed, _ := strconv.Atoi(fmt.Sprint(value))
		return parsed
	}
}

func stringNumber(value any) string {
	return fmt.Sprint(intNumber(value))
}

func stringValue(value any) string { return fmt.Sprint(value) }

func outerJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

type teeReadCloser struct {
	io.Reader
	io.Closer
}
