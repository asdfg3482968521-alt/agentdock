//go:build windows

package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type jobHTTPProcess struct {
	cmd    *exec.Cmd
	done   chan error
	stderr *bytes.Buffer
}

func TestExternalAgentDockPersistentJobsWindowsE2E(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_E2E_BINARY"))
	manifest := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if binary == "" || manifest == "" {
		t.Skip("set AGENTDOCK_OPENCODE_E2E_BINARY and AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST for the persistent-job Windows E2E")
	}

	root := t.TempDir()
	agentDockHome := filepath.Join(root, "agentdock-home")
	agentDockDefault := filepath.Join(root, "agentdock-default")
	openCodeLogs := filepath.Join(root, "opencode-logs")
	jobsRoot := filepath.Join(root, "jobs")
	workspace := filepath.Join(root, "workspace-main")
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	for _, dir := range []string{agentDockHome, agentDockDefault, openCodeLogs, jobsRoot, workspace, workspaceA, workspaceB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeCancellationFixtures(t, workspace)
	writeJobTimeoutFixtures(t, workspace)

	port := reserveJobHTTPPort(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	process := startJobHTTPAgentDock(t, binary, manifest, port, agentDockHome, agentDockDefault, openCodeLogs, jobsRoot)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("AgentDock stderr on failure:\n%s", process.stderr.String())
			entries, _ := os.ReadDir(openCodeLogs)
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				body, readErr := os.ReadFile(filepath.Join(openCodeLogs, entry.Name()))
				if readErr != nil {
					continue
				}
				if len(body) > 8192 {
					body = body[len(body)-8192:]
				}
				t.Logf("OpenCode log %s tail:\n%s", entry.Name(), body)
			}
		}
		stopJobHTTPProcess(process)
	})
	waitJobHTTPReady(t, port, process, 15*time.Second)

	session, closeSession := connectJobHTTPClient(t, endpoint)
	defer func() {
		if closeSession != nil {
			closeSession()
		}
	}()
	assertJobToolsListed(t, session)

	var statusLatency, outputLatency, cancelLatency time.Duration

	t.Run("30s real job and disconnect survival", func(t *testing.T) {
		startArgs := map[string]any{
			"workspace":       workspace,
			"tool":            "bash",
			"permission_mode": "project",
			"arguments":       map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "Write-Output 'job-begin'; Start-Sleep -Seconds 30; Write-Output 'job-30s-ok'"`},
			"metadata":        map[string]any{"purpose": "30s-disconnect-e2e", "token": "must-not-persist-this-secret"},
		}
		started, startLatency := callJobTool(t, session, "job_start", startArgs)
		jobID := stringValue(started["job_id"])
		if jobID == "" {
			t.Fatalf("job_start omitted job_id: %#v", started)
		}
		if startLatency > 1500*time.Millisecond {
			t.Fatalf("job_start blocked too long: %s", startLatency)
		}

		status, latency := waitJobState(t, session, jobID, 15*time.Second, "RUNNING")
		statusLatency = latency
		if stringValue(status["state"]) != "RUNNING" {
			t.Fatalf("status=%#v", status)
		}
		firstOutput, latency := callJobTool(t, session, "job_output", map[string]any{"job_id": jobID, "cursor": 0, "limit": 262144})
		outputLatency = latency
		firstCursor := int64Number(firstOutput["next_cursor"])
		if firstCursor < 2 || !strings.Contains(stringValue(firstOutput["text"]), "QUEUED") {
			t.Fatalf("initial incremental output missing lifecycle events: %#v", firstOutput)
		}

		// Closing this HTTP MCP session must not stop AgentDock or cancel the job.
		closeSession()
		closeSession = nil
		time.Sleep(1500 * time.Millisecond)
		select {
		case err := <-process.done:
			t.Fatalf("AgentDock exited when only the MCP client disconnected: %v\nstderr:\n%s", err, process.stderr.String())
		default:
		}

		var reconnectClose func()
		session, reconnectClose = connectJobHTTPClient(t, endpoint)
		closeSession = reconnectClose
		afterReconnect, _ := callJobTool(t, session, "job_status", map[string]any{"job_id": jobID})
		if state := stringValue(afterReconnect["state"]); state == "CANCELLED" || state == "FAILED" || state == "INTERRUPTED" {
			t.Fatalf("disconnect changed long job to %s: %#v", state, afterReconnect)
		}

		finalStatus, _ := waitJobState(t, session, jobID, 70*time.Second, "SUCCEEDED")
		if !strings.Contains(stringValue(finalStatus["result_summary"]), "job-30s-ok") {
			t.Fatalf("30s job result mismatch: %#v", finalStatus)
		}
		secondOutput, _ := callJobTool(t, session, "job_output", map[string]any{"job_id": jobID, "cursor": firstCursor, "limit": 262144})
		if int64Number(secondOutput["next_cursor"]) <= firstCursor || !strings.Contains(stringValue(secondOutput["text"]), "job-30s-ok") {
			t.Fatalf("incremental output after reconnect did not advance: %#v", secondOutput)
		}
		if complete, _ := secondOutput["complete"].(bool); !complete {
			t.Fatalf("terminal output not complete: %#v", secondOutput)
		}
		t.Logf("JOB_30S job_id=%s start_latency=%s status_latency=%s output_latency=%s cursor=%d->%d disconnect_survival=PASS", jobID, startLatency, statusLatency, outputLatency, firstCursor, int64Number(secondOutput["next_cursor"]))
	})

	t.Run("HTTP disconnect survival", func(t *testing.T) {
		started, startLatency := callJobTool(t, session, "job_start", map[string]any{
			"workspace": workspace,
			"tool":      "bash",
			"arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "Start-Sleep -Seconds 12; Write-Output 'disconnect-survived'"`},
		})
		jobID := stringValue(started["job_id"])
		waitJobState(t, session, jobID, 15*time.Second, "RUNNING")
		before, _ := callJobTool(t, session, "job_output", map[string]any{"job_id": jobID, "cursor": 0, "limit": 262144})
		cursor := int64Number(before["next_cursor"])
		if cursor == 0 {
			t.Fatalf("disconnect job produced no persisted lifecycle output: %#v", before)
		}

		closeSession()
		closeSession = nil
		time.Sleep(2 * time.Second)
		select {
		case err := <-process.done:
			t.Fatalf("AgentDock exited after HTTP client disconnect: %v\nstderr:\n%s", err, process.stderr.String())
		default:
		}

		var reconnectClose func()
		session, reconnectClose = connectJobHTTPClient(t, endpoint)
		closeSession = reconnectClose
		afterReconnect, _ := callJobTool(t, session, "job_status", map[string]any{"job_id": jobID})
		state := stringValue(afterReconnect["state"])
		if state != "RUNNING" && state != "SUCCEEDED" {
			t.Fatalf("disconnect survival state=%s: %#v", state, afterReconnect)
		}
		continued, _ := callJobTool(t, session, "job_output", map[string]any{"job_id": jobID, "cursor": cursor, "limit": 262144})
		if int64Number(continued["next_cursor"]) < cursor {
			t.Fatalf("cursor regressed after reconnect: before=%d output=%#v", cursor, continued)
		}
		finalStatus, _ := waitJobState(t, session, jobID, 50*time.Second, "SUCCEEDED")
		if !strings.Contains(stringValue(finalStatus["result_summary"]), "disconnect-survived") {
			t.Fatalf("disconnect job final result mismatch: %#v", finalStatus)
		}
		t.Logf("JOB_DISCONNECT job_id=%s start_latency=%s state_after_reconnect=%s cursor=%d survival=PASS", jobID, startLatency, state, cursor)
	})

	t.Run("cancel descendant tree", func(t *testing.T) {
		childFile := filepath.Join(workspace, "job-child.pid")
		grandFile := filepath.Join(workspace, "job-grandchild.pid")
		marker := filepath.Join(workspace, "job-cancel-marker.txt")
		started, startLatency := callJobTool(t, session, "job_start", map[string]any{
			"workspace": workspace, "tool": "bash", "permission_mode": "project",
			"arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "node cancel-parent.cjs job-child.pid job-grandchild.pid job-cancel-marker.txt"`},
		})
		jobID := stringValue(started["job_id"])
		childPID := waitOuterPIDFile(t, childFile, 10*time.Second)
		grandPID := waitOuterPIDFile(t, grandFile, 10*time.Second)
		cancelled, latency := callJobTool(t, session, "job_cancel", map[string]any{"job_id": jobID, "reason": "e2e cancellation"})
		cancelLatency = latency
		if stringValue(cancelled["state"]) != "CANCELLED" {
			t.Fatalf("cancel state=%#v", cancelled)
		}
		waitOuterPIDGone(t, childPID, 8*time.Second)
		waitOuterPIDGone(t, grandPID, 8*time.Second)
		time.Sleep(500 * time.Millisecond)
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("cancelled descendant generated marker: %v", err)
		}
		t.Logf("JOB_CANCEL job_id=%s start_latency=%s cancel_latency=%s child_pid=%d grandchild_pid=%d residual_pid=0 marker=0", jobID, startLatency, cancelLatency, childPID, grandPID)
	})

	t.Run("timeout descendant tree", func(t *testing.T) {
		childFile := filepath.Join(workspace, "timeout-child.pid")
		grandFile := filepath.Join(workspace, "timeout-grandchild.pid")
		marker := filepath.Join(workspace, "timeout-marker.txt")
		started, _ := callJobTool(t, session, "job_start", map[string]any{
			"workspace": workspace, "tool": "bash", "permission_mode": "project", "timeout": 12000,
			"arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "node timeout-parent.cjs timeout-child.pid timeout-grandchild.pid timeout-marker.txt"`},
		})
		jobID := stringValue(started["job_id"])
		childPID := waitOuterPIDFile(t, childFile, 12*time.Second)
		grandPID := waitOuterPIDFile(t, grandFile, 12*time.Second)
		status, _ := waitJobState(t, session, jobID, 20*time.Second, "TIMED_OUT")
		if stringValue(status["state"]) != "TIMED_OUT" {
			t.Fatalf("timeout status=%#v", status)
		}
		waitOuterPIDGone(t, childPID, 8*time.Second)
		waitOuterPIDGone(t, grandPID, 8*time.Second)
		time.Sleep(500 * time.Millisecond)
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("timed out descendant generated marker: %v", err)
		}
		t.Logf("JOB_TIMEOUT job_id=%s child_pid=%d grandchild_pid=%d residual_pid=0 marker=0", jobID, childPID, grandPID)
	})

	t.Run("workspace concurrency isolation", func(t *testing.T) {
		j1, _ := callJobTool(t, session, "job_start", map[string]any{"workspace": workspaceA, "tool": "bash", "arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "Start-Sleep -Seconds 8; Write-Output 'A1-done'"`}})
		j2, _ := callJobTool(t, session, "job_start", map[string]any{"workspace": workspaceA, "tool": "bash", "arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "Start-Sleep -Seconds 1; Write-Output 'A2-done'"`}})
		j3, _ := callJobTool(t, session, "job_start", map[string]any{"workspace": workspaceB, "tool": "bash", "arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "Start-Sleep -Seconds 4; Write-Output 'B3-done'"`}})
		id1, id2, id3 := stringValue(j1["job_id"]), stringValue(j2["job_id"]), stringValue(j3["job_id"])
		waitJobState(t, session, id1, 15*time.Second, "RUNNING")
		waitJobState(t, session, id3, 15*time.Second, "RUNNING", "SUCCEEDED")
		status2, _ := callJobTool(t, session, "job_status", map[string]any{"job_id": id2})
		if stringValue(status2["state"]) != "QUEUED" {
			t.Fatalf("same-workspace destructive job should stay queued: %#v", status2)
		}
		callJobTool(t, session, "job_cancel", map[string]any{"job_id": id1, "reason": "concurrency isolation check"})
		status3, _ := callJobTool(t, session, "job_status", map[string]any{"job_id": id3})
		if state := stringValue(status3["state"]); state == "CANCELLED" || state == "FAILED" || state == "TIMED_OUT" {
			t.Fatalf("cancelling workspace A killed workspace B: %#v", status3)
		}
		final2, _ := waitJobState(t, session, id2, 35*time.Second, "SUCCEEDED")
		final3, _ := waitJobState(t, session, id3, 35*time.Second, "SUCCEEDED")
		if !strings.Contains(stringValue(final2["result_summary"]), "A2-done") || !strings.Contains(stringValue(final3["result_summary"]), "B3-done") {
			t.Fatalf("concurrency results incorrect A2=%#v B3=%#v", final2, final3)
		}
		t.Logf("JOB_CONCURRENCY A1=%s(cancelled) A2=%s(serialized-success) B3=%s(parallel-success) cross_kill=0", id1, id2, id3)
	})

	t.Run("AgentDock restart marks running interrupted", func(t *testing.T) {
		childFile := filepath.Join(workspace, "restart-child.pid")
		grandFile := filepath.Join(workspace, "restart-grandchild.pid")
		marker := filepath.Join(workspace, "restart-marker.txt")
		started, _ := callJobTool(t, session, "job_start", map[string]any{
			"workspace": workspace,
			"tool":      "bash",
			"arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "node cancel-parent.cjs restart-child.pid restart-grandchild.pid restart-marker.txt"`},
		})
		jobID := stringValue(started["job_id"])
		waitJobState(t, session, jobID, 12*time.Second, "RUNNING")
		childPID := waitOuterPIDFile(t, childFile, 30*time.Second)
		grandPID := waitOuterPIDFile(t, grandFile, 30*time.Second)
		oldRuntimePID := jobRuntimePIDForWorkspace(t, session, workspace)
		oldAgentDockPID := process.cmd.Process.Pid
		before, _ := callJobTool(t, session, "job_output", map[string]any{"job_id": jobID, "cursor": 0, "limit": 262144})
		if int64Number(before["next_cursor"]) == 0 {
			t.Fatalf("pre-restart output is empty: %#v", before)
		}

		closeSession()
		closeSession = nil
		if err := process.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-process.done:
		case <-time.After(8 * time.Second):
			t.Fatal("first AgentDock process did not exit after forced restart")
		}
		time.Sleep(1500 * time.Millisecond)
		runtimeResidual := boolInt(outerPIDAlive(oldRuntimePID))
		childResidual := boolInt(outerPIDAlive(childPID))
		grandResidual := boolInt(outerPIDAlive(grandPID))
		t.Logf("JOB_CRASH_RESIDUAL agentdock_pid=%d old_runtime_pid=%d runtime_residual=%d child_pid=%d child_residual=%d grandchild_pid=%d grandchild_residual=%d", oldAgentDockPID, oldRuntimePID, runtimeResidual, childPID, childResidual, grandPID, grandResidual)

		process = startJobHTTPAgentDock(t, binary, manifest, port, agentDockHome, agentDockDefault, openCodeLogs, jobsRoot)
		waitJobHTTPReady(t, port, process, 15*time.Second)
		var reconnectClose func()
		session, reconnectClose = connectJobHTTPClient(t, endpoint)
		closeSession = reconnectClose
		recovered, _ := callJobTool(t, session, "job_status", map[string]any{"job_id": jobID})
		if stringValue(recovered["state"]) != "INTERRUPTED" {
			cleanupResidualPIDs(oldRuntimePID, childPID, grandPID)
			t.Fatalf("restart recovery state=%#v", recovered)
		}
		history, _ := callJobTool(t, session, "job_output", map[string]any{"job_id": jobID, "cursor": 0, "limit": 262144})
		if int64Number(history["next_cursor"]) == 0 || !strings.Contains(stringValue(history["text"]), "RUNNING") {
			cleanupResidualPIDs(oldRuntimePID, childPID, grandPID)
			t.Fatalf("restart lost durable historical output: %#v", history)
		}
		if runtimeResidual != 0 || childResidual != 0 || grandResidual != 0 {
			cleanupResidualPIDs(oldRuntimePID, childPID, grandPID)
			t.Fatalf("AgentDock crash left orphan processes: runtime=%d child=%d grandchild=%d", runtimeResidual, childResidual, grandResidual)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("restart descendant marker unexpectedly exists: %v", err)
		}
		t.Logf("JOB_RESTART job_id=%s state=INTERRUPTED historical_cursor=%d old_runtime_residual=0 old_descendant_residual=0", jobID, int64Number(history["next_cursor"]))
	})

	t.Run("job list remains bounded and queryable", func(t *testing.T) {
		listed, latency := callJobTool(t, session, "job_list", map[string]any{"limit": 10})
		if int64Number(listed["count"]) <= 0 {
			t.Fatalf("job_list returned no history: %#v", listed)
		}
		t.Logf("JOB_LIST latency=%s count=%d", latency, int64Number(listed["count"]))
	})

	t.Logf("JOB_PERF job_status=%s job_output=%s job_cancel=%s", statusLatency, outputLatency, cancelLatency)
}

func TestExternalAgentDockPersistentJobsOfflineToolSurfaceWindowsE2E(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_E2E_BINARY"))
	if binary == "" {
		t.Skip("set AGENTDOCK_OPENCODE_E2E_BINARY for the offline persistent-job Windows E2E")
	}
	root := t.TempDir()
	home := filepath.Join(root, "agentdock-home")
	defaultDir := filepath.Join(root, "agentdock-default")
	jobsRoot := filepath.Join(root, "jobs")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{home, defaultDir, jobsRoot, workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	port := reserveJobHTTPPort(t)
	process := startJobHTTPAgentDockOffline(t, binary, port, home, defaultDir, jobsRoot)
	defer stopJobHTTPProcess(process)
	waitJobHTTPReady(t, port, process, 15*time.Second)
	session, closeSession := connectJobHTTPClient(t, fmt.Sprintf("http://127.0.0.1:%d/mcp", port))
	defer closeSession()
	assertJobToolsListed(t, session)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "job_start", Arguments: map[string]any{
		"workspace": workspace, "tool": "bash", "arguments": map[string]any{"command": "echo offline"},
	}})
	if err != nil {
		t.Fatalf("offline job_start transport error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("offline job_start should return structured tool error: %s", outerJSON(result))
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || stringValue(structured["code"]) != "JOB_RUNTIME_UNAVAILABLE" {
		t.Fatalf("offline job_start code mismatch: %s", outerJSON(result))
	}
	retryable, _ := structured["retryable"].(bool)
	if !retryable {
		t.Fatalf("offline job_start must be retryable: %s", outerJSON(result))
	}
	t.Logf("JOB_OFFLINE tools_list=PASS job_start_code=JOB_RUNTIME_UNAVAILABLE retryable=true")
}

func writeJobTimeoutFixtures(t *testing.T, workspace string) {
	t.Helper()
	parent := `const fs=require("fs"); const cp=require("child_process"); const [childPidFile,grandPidFile,marker]=process.argv.slice(2); fs.writeFileSync(childPidFile,String(process.pid)); const child=cp.spawn(process.execPath,["timeout-grandchild.cjs",grandPidFile,marker],{stdio:"ignore"}); child.on("exit",()=>{}); setTimeout(()=>{},30000);`
	grandchild := `const fs=require("fs"); const [pidFile,marker]=process.argv.slice(2); fs.writeFileSync(pidFile,String(process.pid)); setTimeout(()=>fs.writeFileSync(marker,"should-not-exist"),20000); setTimeout(()=>{},30000);`
	mustWriteOuter(t, filepath.Join(workspace, "timeout-parent.cjs"), parent)
	mustWriteOuter(t, filepath.Join(workspace, "timeout-grandchild.cjs"), grandchild)
}

func reserveJobHTTPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func startJobHTTPAgentDockOffline(t *testing.T, binary string, port int, home, defaultDir, jobsRoot string) *jobHTTPProcess {
	t.Helper()
	cmd := exec.Command(binary, "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Dir = defaultDir
	cmd.Env = append(os.Environ(),
		"USERPROFILE="+filepath.Dir(home),
		"AGENTDOCK_HOME="+home,
		"AGENTDOCK_DEFAULT_DIR="+defaultDir,
		"AGENTDOCK_AUTH_TOKEN=",
		"AGENTDOCK_OAUTH_ENABLED=0",
		"AGENTDOCK_OPENCODE_ENABLED=0",
		"AGENTDOCK_JOB_STORAGE_ROOT="+jobsRoot,
		"AGENTDOCK_LOG_LEVEL=info",
	)
	stderr := &bytes.Buffer{}
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return &jobHTTPProcess{cmd: cmd, done: done, stderr: stderr}
}

func startJobHTTPAgentDock(t *testing.T, binary, manifest string, port int, home, defaultDir, logDir, jobsRoot string) *jobHTTPProcess {
	t.Helper()
	cmd := exec.Command(binary, "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Dir = defaultDir
	cmd.Env = append(os.Environ(),
		"USERPROFILE="+filepath.Dir(home),
		"AGENTDOCK_HOME="+home,
		"AGENTDOCK_DEFAULT_DIR="+defaultDir,
		"AGENTDOCK_AUTH_TOKEN=",
		"AGENTDOCK_OAUTH_ENABLED=0",
		"AGENTDOCK_OPENCODE_ENABLED=1",
		"AGENTDOCK_OPENCODE_RUNTIME_MANIFEST="+manifest,
		"AGENTDOCK_OPENCODE_LOG_DIR="+logDir,
		"AGENTDOCK_JOB_STORAGE_ROOT="+jobsRoot,
		"AGENTDOCK_LOG_LEVEL=info",
	)
	stderr := &bytes.Buffer{}
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return &jobHTTPProcess{cmd: cmd, done: done, stderr: stderr}
}

func stopJobHTTPProcess(process *jobHTTPProcess) {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return
	}
	select {
	case <-process.done:
		return
	default:
	}
	_ = process.cmd.Process.Kill()
	select {
	case <-process.done:
	case <-time.After(3 * time.Second):
	}
}

func waitJobHTTPReady(t *testing.T, port int, process *jobHTTPProcess, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-process.done:
			t.Fatalf("AgentDock exited before HTTP became ready: %v\nstderr:\n%s", err, process.stderr.String())
		default:
		}
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("AgentDock HTTP did not become ready\nstderr:\n%s", process.stderr.String())
}

func connectJobHTTPClient(t *testing.T, endpoint string) (*mcpsdk.ClientSession, func()) {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agentdock-persistent-job-e2e", Version: "1"}, &mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Timeout: 15 * time.Second},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("HTTP MCP connect failed: %v", err)
	}
	return session, func() { _ = session.Close() }
}

func assertJobToolsListed(t *testing.T, session *mcpsdk.ClientSession) {
	t.Helper()
	seen := map[string]bool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		seen[tool.Name] = true
	}
	for _, name := range []string{"job_start", "job_status", "job_output", "job_cancel", "job_list"} {
		if !seen[name] {
			t.Fatalf("tools/list missing %s", name)
		}
	}
}

func callJobTool(t *testing.T, session *mcpsdk.ClientSession, name string, arguments map[string]any) (map[string]any, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	started := time.Now()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	latency := time.Since(started)
	if err != nil {
		t.Fatalf("%s transport error after %s: %v", name, latency, err)
	}
	if result == nil || result.IsError {
		t.Fatalf("%s failed after %s: %s", name, latency, outerJSON(result))
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s structured content type=%T body=%s", name, result.StructuredContent, outerJSON(result))
	}
	return structured, latency
}

func waitJobState(t *testing.T, session *mcpsdk.ClientSession, jobID string, timeout time.Duration, states ...string) (map[string]any, time.Duration) {
	t.Helper()
	want := map[string]bool{}
	for _, state := range states {
		want[state] = true
	}
	deadline := time.Now().Add(timeout)
	var last map[string]any
	var lastLatency time.Duration
	for time.Now().Before(deadline) {
		status, latency := callJobTool(t, session, "job_status", map[string]any{"job_id": jobID})
		last, lastLatency = status, latency
		if want[stringValue(status["state"])] {
			return status, latency
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting job %s for %v, last=%#v", jobID, states, last)
	return last, lastLatency
}

func jobRuntimePIDForWorkspace(t *testing.T, session *mcpsdk.ClientSession, workspace string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("server_info transport error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("server_info failed: %s", outerJSON(result))
	}
	for _, summary := range outerRuntimeSummaries(result) {
		if strings.EqualFold(filepath.Clean(stringValue(summary["workspace"])), filepath.Clean(workspace)) && stringValue(summary["permission_mode"]) == "project" {
			if pid := intNumber(summary["pid"]); pid > 0 {
				return pid
			}
		}
	}
	t.Fatalf("no OpenCode runtime PID found for workspace %s: %s", workspace, outerJSON(result))
	return 0
}

func outerPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }", pid))
	return cmd.Run() == nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func cleanupResidualPIDs(pids ...int) {
	for _, pid := range pids {
		if pid <= 0 || !outerPIDAlive(pid) {
			continue
		}
		process, err := os.FindProcess(pid)
		if err == nil {
			_ = process.Kill()
		}
	}
}

func int64Number(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case jsonNumber:
		parsed, _ := strconv.ParseInt(string(typed), 10, 64)
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}

// jsonNumber is deliberately a local string alias so this helper accepts SDK
// decoders that preserve textual JSON numbers without importing another codec.
type jsonNumber string
