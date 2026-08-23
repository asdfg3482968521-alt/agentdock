//go:build windows

package opencode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRealVerifiedOpenCodeRuntimeIntegration(t *testing.T) {
	manifestPath := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if manifestPath == "" {
		t.Skip("set AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST to run the real Windows OpenCode runtime integration test")
	}
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(filepath.Join(workspace, "test.txt"), []byte("value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	slowScript := `const fs = require("fs"); const marker = process.argv[2]; const pidFile = process.argv[3]; fs.writeFileSync(pidFile, String(process.pid)); setTimeout(() => fs.writeFileSync(marker, "should-not-exist"), 4000); setTimeout(() => {}, 8000);`
	if err := os.WriteFile(filepath.Join(workspace, "slow.cjs"), []byte(slowScript), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workspace, "init")
	gitRun(t, workspace, "config", "user.email", "c1a@example.invalid")
	gitRun(t, workspace, "config", "user.name", "C1-A Integration")
	gitRun(t, workspace, "add", "test.txt")
	gitRun(t, workspace, "commit", "-m", "baseline")

	supervisor, err := NewSupervisor(context.Background(), Options{
		Locator:        ManifestLocator{Path: manifestPath},
		LogDir:         filepath.Join(t.TempDir(), "logs"),
		RestartCap:     2,
		RestartBackoff: 50 * time.Millisecond,
		ShutdownGrace:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = supervisor.Close()
		}
	})

	instance, err := supervisor.Acquire(context.Background(), workspace, PermissionProject)
	if err != nil {
		t.Fatal(err)
	}
	ready := instance.summary()
	if ready.State != StateReady || ready.PID <= 0 || ready.RuntimeSHA != "d5664f38002d8326eca179cf2efb5774c8ce1247" || ready.ProtocolVersion == "" {
		t.Fatalf("unexpected ready summary: %#v", ready)
	}
	runtimePID := ready.PID
	t.Logf("READY pid=%d protocol=%s spawn=%dms initialize=%dms tools_list=%dms", ready.PID, ready.ProtocolVersion, ready.SpawnMS, ready.InitializeMS, ready.ToolsListMS)

	read, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "read", map[string]any{"filePath": filepath.Join(workspace, "test.txt")})
	if err != nil {
		t.Fatal(err)
	}
	assertRemoteSuccess(t, read.Result, "read")
	if !strings.Contains(remoteText(read.Result), "value = 1") {
		t.Fatalf("read output = %q", remoteText(read.Result))
	}

	edit, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "edit", map[string]any{
		"filePath": filepath.Join(workspace, "test.txt"), "oldString": "value = 1", "newString": "value = 2",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRemoteSuccess(t, edit.Result, "edit")
	contents, err := os.ReadFile(filepath.Join(workspace, "test.txt"))
	if err != nil || !strings.Contains(string(contents), "value = 2") {
		t.Fatalf("edited file = %q err=%v", contents, err)
	}

	bash, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "bash", map[string]any{"command": `node -p "6 * 7"`})
	if err != nil {
		t.Fatal(err)
	}
	assertRemoteSuccess(t, bash.Result, "bash")
	if !strings.Contains(remoteText(bash.Result), "42") {
		t.Fatalf("bash output = %q", remoteText(bash.Result))
	}

	gitStatus, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "git", map[string]any{"operation": "status"})
	if err != nil {
		t.Fatal(err)
	}
	assertRemoteSuccess(t, gitStatus.Result, "git")

	outside, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "read", map[string]any{"filePath": outsideFile})
	if err != nil {
		t.Fatal(err)
	}
	if isError, _ := outside.Result["isError"].(bool); !isError {
		t.Fatalf("project mode unexpectedly read outside workspace: %#v", outside.Result)
	}
	if !strings.Contains(remoteText(outside.Result), "OUTSIDE_WORKSPACE") {
		t.Fatalf("outside-workspace error lost category: %q", remoteText(outside.Result))
	}

	marker := filepath.Join(workspace, "cancelled-marker.txt")
	pidFile := filepath.Join(workspace, "cancelled-child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		_, callErr := supervisor.Execute(ctx, workspace, PermissionProject, "bash", map[string]any{
			"command": fmt.Sprintf(`node slow.cjs %q %q`, filepath.Base(marker), filepath.Base(pidFile)),
		})
		callDone <- callErr
	}()
	childPID := waitPIDFile(t, pidFile, 5*time.Second)
	cancel()
	callErr := <-callDone
	var runtimeErr *Error
	if !errors.As(callErr, &runtimeErr) || runtimeErr.Code != "OPENCODE_ABORTED" {
		t.Fatalf("cancel error = %#v, want OPENCODE_ABORTED", callErr)
	}
	waitWindowsPIDGone(t, childPID, 5*time.Second)
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled descendant wrote marker: err=%v", err)
	}

	recovered, err := supervisor.Execute(context.Background(), workspace, PermissionProject, "read", map[string]any{"filePath": filepath.Join(workspace, "test.txt")})
	if err != nil {
		t.Fatal(err)
	}
	assertRemoteSuccess(t, recovered.Result, "read after cancellation")

	shutdownStarted := time.Now()
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	shutdown := time.Since(shutdownStarted)
	waitWindowsPIDGone(t, runtimePID, 5*time.Second)
	if len(supervisor.Summaries()) != 0 {
		t.Fatalf("runtime summaries remain after shutdown: %#v", supervisor.Summaries())
	}
	t.Logf("SHUTDOWN pid=%d duration=%s residual=0", runtimePID, shutdown)
}

func assertRemoteSuccess(t *testing.T, result map[string]any, label string) {
	t.Helper()
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("%s remote error: %s", label, remoteText(result))
	}
}

func remoteText(result map[string]any) string {
	var parts []string
	content, _ := result["content"].([]any)
	for _, item := range content {
		entry, _ := item.(map[string]any)
		if text, _ := entry["text"].(string); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func gitRun(t *testing.T, workdir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", workdir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func waitPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pid file %s", path)
	return 0
}

func waitWindowsPIDGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
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
