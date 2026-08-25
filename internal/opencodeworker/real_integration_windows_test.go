//go:build windows

package opencodeworker

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

	"github.com/uvwt/agentdock/internal/opencode"
)

func TestRealVerifiedRuntimeWorkerConfinementCancellationAndCleanup(t *testing.T) {
	manifestPath := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if manifestPath == "" {
		t.Skip("set AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST to run the real Windows worker integration test")
	}
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(filepath.Join(workspace, "inside.txt"), []byte("worker-ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	slowScript := `const fs = require("fs"); const marker = process.argv[2]; const pidFile = process.argv[3]; fs.writeFileSync(pidFile, String(process.pid)); setTimeout(() => fs.writeFileSync(marker, "orphan"), 4000); setTimeout(() => {}, 8000);`
	if err := os.WriteFile(filepath.Join(workspace, "slow.cjs"), []byte(slowScript), 0o600); err != nil {
		t.Fatal(err)
	}

	supervisor, err := opencode.NewSupervisor(context.Background(), opencode.Options{
		Locator: opencode.ManifestLocator{Path: manifestPath}, LogDir: filepath.Join(t.TempDir(), "logs"),
		RestartCap: 2, RestartBackoff: 50 * time.Millisecond, ShutdownGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	engine := newTestEngine(t, supervisor)

	readResult, err := engine.Execute(context.Background(), Request{
		Scope: Scope{ID: "real-read", Workspace: workspace}, PermissionMode: opencode.PermissionProject,
		Instruction: "read one file", Steps: []Step{{Tool: "read", Arguments: map[string]any{"filePath": filepath.Join(workspace, "inside.txt")}}},
	}, nil)
	if err != nil || readResult.Completion != CompletionSucceeded || len(readResult.Steps) != 1 {
		t.Fatalf("real read result=%#v err=%v", readResult, err)
	}
	if readResult.Steps[0].Runtime.RuntimeSHA != "d5664f38002d8326eca179cf2efb5774c8ce1247" {
		t.Fatalf("unexpected runtime SHA: %#v", readResult.Steps[0].Runtime)
	}

	outsideResult, outsideErr := engine.Execute(context.Background(), Request{
		Scope: Scope{ID: "real-outside", Workspace: workspace}, PermissionMode: opencode.PermissionProject,
		Instruction: "attempt outside read", Steps: []Step{{Tool: "read", Arguments: map[string]any{"filePath": outsideFile}}},
	}, nil)
	if ErrorCode(outsideErr) != "OPENCODE_WORKER_TOOL_FAILED" || outsideResult.Completion != CompletionFailed {
		t.Fatalf("outside result=%#v err=%v", outsideResult, outsideErr)
	}

	marker := filepath.Join(workspace, "cancelled-marker.txt")
	pidFile := filepath.Join(workspace, "cancelled-child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, runErr := engine.Execute(ctx, Request{
			Scope: Scope{ID: "real-cancel", Workspace: workspace}, PermissionMode: opencode.PermissionProject,
			Instruction: "run and cancel a bounded child process", Steps: []Step{{Tool: "bash", Arguments: map[string]any{
				"command": fmt.Sprintf(`node slow.cjs %q %q`, filepath.Base(marker), filepath.Base(pidFile)),
			}}},
		}, nil)
		resultCh <- result
		errCh <- runErr
	}()
	childPID := waitWorkerPIDFile(t, pidFile, 5*time.Second)
	cancel()
	cancelResult := <-resultCh
	cancelErr := <-errCh
	if ErrorCode(cancelErr) != "OPENCODE_WORKER_CANCELLED" || cancelResult.Completion != CompletionCancelled {
		t.Fatalf("cancel result=%#v err=%v", cancelResult, cancelErr)
	}
	waitWorkerWindowsPIDGone(t, childPID, 5*time.Second)
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled descendant wrote marker: err=%v", err)
	}

	var runtimeErr *opencode.Error
	if errors.As(cancelErr, &runtimeErr) && runtimeErr.Code != "OPENCODE_ABORTED" {
		t.Fatalf("unexpected runtime cancel category: %s", runtimeErr.Code)
	}
}

func waitWorkerPIDFile(t *testing.T, path string, timeout time.Duration) int {
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

func waitWorkerWindowsPIDGone(t *testing.T, pid int, timeout time.Duration) {
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
