//go:build windows

package app

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

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/credential"
	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
	"github.com/uvwt/agentdock/internal/opencodeworker"
)

func newC25RealOpenCodeRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	manifest := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if manifest == "" {
		t.Skip("set AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST for real C2.5 OpenCode Agent integration")
	}
	root := t.TempDir()
	cfg := config.Config{
		AgentDockDefaultDir:     root,
		AgentDockHome:           filepath.Join(root, ".agentdock"),
		OpenCodeEnabled:         true,
		OpenCodeRuntimeManifest: manifest,
		OpenCodeLogDir:          filepath.Join(root, "opencode-logs"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime(real OpenCode): %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt, root
}

func startC25RealOpenCodeAgent(t *testing.T, rt *Runtime, sessionID string, prompt string, permission string, timeoutMS int) string {
	t.Helper()
	result, err := rt.Call(context.Background(), "agent_start", map[string]any{
		"session_id": sessionID, "provider": "opencode", "model": nil, "role": "implementation",
		"credential_ref": "cred_opencode_local", "prompt": prompt, "permission": permission,
		"timeout_ms": timeoutMS, "budget": map[string]any{"max_requests": 8},
	})
	if err != nil {
		t.Fatalf("agent_start: %v", err)
	}
	agent := publicAgentFromResult(t, result)
	id, _ := agent["agent_id"].(string)
	if id == "" {
		t.Fatalf("agent_start result=%#v", result)
	}
	return id
}

func waitC25AgentStepCompleted(t *testing.T, rt *Runtime, sessionID, agentID, stepID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		output, err := rt.agents.Output(sessionID, agentID, 0, 256<<10)
		if err == nil {
			last = output.Text
			for _, line := range strings.Split(output.Text, "\n") {
				if strings.Contains(line, `"kind":"step_completed"`) && strings.Contains(line, `"step_id":"`+stepID+`"`) {
					t.Logf("OpenCode worker readiness step completed agent=%s step=%s", agentID, stepID)
					return
				}
			}
		}
		if record, recordErr := rt.agents.Get(sessionID, agentID); recordErr == nil && record.State.Terminal() {
			t.Fatalf("agent became terminal before readiness step completed agent=%s step=%s state=%s output=%q", agentID, stepID, record.State, last)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for Agent readiness step completion agent=%s step=%s output=%q", agentID, stepID, last)
}

func waitC25OpenCodeRuntimeReady(t *testing.T, rt *Runtime, workspace string, mode opencoderuntime.PermissionMode, timeout time.Duration) opencoderuntime.Summary {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last opencoderuntime.Summary
	for time.Now().Before(deadline) {
		summary, ok := rt.openCode.Get(workspace, mode)
		if ok {
			last = summary
			if (summary.State == opencoderuntime.StateReady || summary.State == opencoderuntime.StateBusy) &&
				summary.PID > 0 &&
				summary.RuntimeSHA == "d5664f38002d8326eca179cf2efb5774c8ce1247" &&
				summary.ProtocolVersion != "" {
				t.Logf("OpenCode runtime executable phase reached state=%s pid=%d sha=%s protocol=%s initialize=%dms tools_list=%dms", summary.State, summary.PID, summary.RuntimeSHA, summary.ProtocolVersion, summary.InitializeMS, summary.ToolsListMS)
				return summary
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for OpenCode runtime readiness workspace=%q mode=%s last=%+v", workspace, mode, last)
	return opencoderuntime.Summary{}
}

func TestC25RealOpenCodePublicSuccessAndFailure(t *testing.T) {
	rt, root := newC25RealOpenCodeRuntime(t)
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	insideFile := filepath.Join(workspace, "inside.txt")
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(insideFile, []byte("c25-real-success\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("must-not-read\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	session := mustOpenSession(t, rt, map[string]any{"project_id": "c25-real", "repo_root": workspace, "permission_mode": "project"})
	successID := startC25RealOpenCodeAgent(t, rt, session.SessionID,
		c25TaskPrompt(t, "read inside workspace", []opencodeworker.Step{{Tool: "read", Arguments: map[string]any{"filePath": insideFile}}}), "project", 30000)
	success, err := rt.agents.Wait(context.Background(), successID)
	if err != nil {
		t.Fatalf("success Wait: %v", err)
	}
	if success.State != agentruntime.AgentStateSucceeded || success.Backend != agentruntime.BackendIdentityOpenCode {
		t.Fatalf("success record=%+v", success)
	}
	if success.Workspace != session.Workspace() {
		t.Fatalf("workspace mapping=%q want=%q", success.Workspace, session.Workspace())
	}
	if _, err := rt.credentialStore.Describe(context.Background(), "cred_opencode_local"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("real OpenCode path touched provider credential store: %v", err)
	}
	output, err := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": session.SessionID, "agent_id": successID})
	if err != nil || !strings.Contains(output["output"].(string), `"kind":"step_completed"`) {
		t.Fatalf("success output=%#v err=%v", output, err)
	}
	result, err := rt.Call(context.Background(), "agent_result", map[string]any{"session_id": session.SessionID, "agent_id": successID})
	if err != nil || result["state"] != string(agentruntime.AgentStateSucceeded) {
		t.Fatalf("success result=%#v err=%v", result, err)
	}
	durable, err := os.ReadFile(filepath.Join(rt.cfg.AgentDockHome, "agents", "records", successID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(durable), `"backend":"opencode"`) || !strings.Contains(string(durable), `"state":"SUCCEEDED"`) {
		t.Fatalf("success durable=%s", durable)
	}

	failureID := startC25RealOpenCodeAgent(t, rt, session.SessionID,
		c25TaskPrompt(t, "deny outside workspace", []opencodeworker.Step{{Tool: "read", Arguments: map[string]any{"filePath": outsideFile}}}), "project", 30000)
	failure, err := rt.agents.Wait(context.Background(), failureID)
	if err != nil {
		t.Fatalf("failure Wait: %v", err)
	}
	if failure.State != agentruntime.AgentStateFailed || failure.Backend != agentruntime.BackendIdentityOpenCode {
		t.Fatalf("failure record=%+v", failure)
	}
	failureResult, err := rt.Call(context.Background(), "agent_result", map[string]any{"session_id": session.SessionID, "agent_id": failureID})
	if err != nil || failureResult["state"] != string(agentruntime.AgentStateFailed) || failureResult["failure_reason"] == nil {
		t.Fatalf("failure result=%#v err=%v", failureResult, err)
	}
	if data, readErr := os.ReadFile(outsideFile); readErr != nil || string(data) != "must-not-read\n" {
		t.Fatalf("outside file changed/read fixture invalid data=%q err=%v", data, readErr)
	}
}

func TestC25RealOpenCodeAgentCancelAndTimeoutCleanup(t *testing.T) {
	rt, root := newC25RealOpenCodeRuntime(t)
	workspace := filepath.Join(root, "cancel-timeout-workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "c25-cancel-timeout", "repo_root": workspace, "permission_mode": "project"})

	cancelMarker := filepath.Join(workspace, "cancel-marker.txt")
	cancelPIDFile := filepath.Join(workspace, "cancel-child.pid")
	cancelID := startC25RealOpenCodeAgent(t, rt, session.SessionID,
		c25TaskPrompt(t, "run then cancel", []opencodeworker.Step{
			{ID: "cancel-readiness", Tool: "bash", Arguments: map[string]any{"command": "echo C25_BASH_READY", "timeout": 30000}},
			{ID: "cancel-step", Tool: "bash", Arguments: map[string]any{"command": c25SlowPowerShellCommand(cancelMarker, cancelPIDFile)}},
		}), "project", 90000)
	waitC25AgentStepCompleted(t, rt, session.SessionID, cancelID, "cancel-readiness", 75*time.Second)
	waitC25OpenCodeRuntimeReady(t, rt, session.Workspace(), opencoderuntime.PermissionProject, 8*time.Second)
	cancelChildPID := waitC25PIDFile(t, cancelPIDFile, 8*time.Second)
	cancelled, err := rt.Call(context.Background(), "agent_cancel", map[string]any{"session_id": session.SessionID, "agent_id": cancelID, "reason": "c25 cancel proof"})
	if err != nil {
		t.Fatalf("agent_cancel: %v", err)
	}
	if cancelled["state"] != string(agentruntime.AgentStateCancelled) {
		t.Fatalf("cancel result=%#v", cancelled)
	}
	cancelRecord, err := rt.agents.Wait(context.Background(), cancelID)
	if err != nil || cancelRecord.State != agentruntime.AgentStateCancelled {
		t.Fatalf("cancel Wait record=%+v err=%v", cancelRecord, err)
	}
	waitC25WindowsPIDGone(t, cancelChildPID, 8*time.Second)
	if _, err := os.Stat(cancelMarker); !os.IsNotExist(err) {
		t.Fatalf("cancelled descendant marker exists: %v", err)
	}

	cancelRuntime, ok := rt.openCode.Get(session.Workspace(), opencoderuntime.PermissionProject)
	if !ok || cancelRuntime.PID <= 0 || cancelRuntime.RuntimeSHA != "d5664f38002d8326eca179cf2efb5774c8ce1247" {
		t.Fatalf("runtime after cancel=%+v ok=%v", cancelRuntime, ok)
	}

	timeoutMarker := filepath.Join(workspace, "timeout-marker.txt")
	timeoutPIDFile := filepath.Join(workspace, "timeout-child.pid")
	waitC25OpenCodeRuntimeReady(t, rt, session.Workspace(), opencoderuntime.PermissionProject, 8*time.Second)
	timeoutID := startC25RealOpenCodeAgent(t, rt, session.SessionID,
		c25TaskPrompt(t, "run until timeout", []opencodeworker.Step{{Tool: "bash", Arguments: map[string]any{
			"command": c25TimeoutPowerShellCommand(timeoutMarker, timeoutPIDFile),
		}}}), "project", 8000)
	timeoutChildPID := waitC25PIDFile(t, timeoutPIDFile, 5*time.Second)
	timeoutRecord, err := rt.agents.Wait(context.Background(), timeoutID)
	if err != nil {
		t.Fatalf("timeout Wait: %v", err)
	}
	if timeoutRecord.State != agentruntime.AgentStateTimedOut {
		t.Fatalf("timeout record=%+v", timeoutRecord)
	}
	waitC25WindowsPIDGone(t, timeoutChildPID, 8*time.Second)
	// The timeout fixture writes its orphan marker 10s after child startup. Since
	// the PID gate proves startup within 5s and the Agent deadline is 8s, an
	// orphaned descendant must have written the marker before this check.
	time.Sleep(8 * time.Second)
	if _, err := os.Stat(timeoutMarker); !os.IsNotExist(err) {
		t.Fatalf("timed out descendant marker exists: %v", err)
	}
	postTimeoutRuntime, ok := rt.openCode.Get(session.Workspace(), opencoderuntime.PermissionProject)
	if !ok || postTimeoutRuntime.PID != cancelRuntime.PID {
		t.Fatalf("runtime not preserved across cancel/timeout before Session close: before=%+v after=%+v ok=%v", cancelRuntime, postTimeoutRuntime, ok)
	}
}

func TestC25RealOpenCodeSessionCloseReleasesRuntimeAndDescendants(t *testing.T) {
	rt, root := newC25RealOpenCodeRuntime(t)
	workspace := filepath.Join(root, "session-close-workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "c25-session-close", "repo_root": workspace, "permission_mode": "project"})
	marker := filepath.Join(workspace, "session-close-marker.txt")
	pidFile := filepath.Join(workspace, "session-close-child.pid")
	agentID := startC25RealOpenCodeAgent(t, rt, session.SessionID,
		c25TaskPrompt(t, "keep child active until Session close", []opencodeworker.Step{
			{ID: "close-readiness", Tool: "bash", Arguments: map[string]any{"command": "echo C25_BASH_READY", "timeout": 30000}},
			{ID: "close-step", Tool: "bash", Arguments: map[string]any{"command": c25SlowPowerShellCommand(marker, pidFile)}},
		}), "project", 90000)
	waitC25AgentStepCompleted(t, rt, session.SessionID, agentID, "close-readiness", 75*time.Second)
	waitC25OpenCodeRuntimeReady(t, rt, session.Workspace(), opencoderuntime.PermissionProject, 8*time.Second)
	childPID := waitC25PIDFile(t, pidFile, 8*time.Second)

	bound, err := rt.sessions.Get(session.SessionID)
	if err != nil {
		t.Fatalf("session get before close: %v", err)
	}
	wantScope := d1RuntimeScope(session.SessionID)
	if bound.RuntimeID == nil || *bound.RuntimeID != wantScope {
		t.Fatalf("runtime ownership=%v want=%q", bound.RuntimeID, wantScope)
	}
	runtimeSummary, ok := rt.openCode.Get(session.Workspace(), opencoderuntime.PermissionProject)
	if !ok || runtimeSummary.PID <= 0 || runtimeSummary.RuntimeSHA != "d5664f38002d8326eca179cf2efb5774c8ce1247" {
		t.Fatalf("runtime before Session close=%+v ok=%v", runtimeSummary, ok)
	}

	closed, err := rt.Call(context.Background(), "session_close", map[string]any{"session_id": session.SessionID})
	if err != nil {
		t.Fatalf("session_close: %v", err)
	}
	if closed["ok"] != true {
		t.Fatalf("session_close result=%#v", closed)
	}
	terminal, err := rt.agents.Wait(context.Background(), agentID)
	if err != nil || terminal.State != agentruntime.AgentStateCancelled {
		t.Fatalf("Agent after Session close=%+v err=%v", terminal, err)
	}
	if _, exists := rt.openCode.Get(session.Workspace(), opencoderuntime.PermissionProject); exists {
		t.Fatal("Session close left OpenCode runtime registered")
	}
	waitC25WindowsPIDGone(t, runtimeSummary.PID, 8*time.Second)
	waitC25WindowsPIDGone(t, childPID, 8*time.Second)
	time.Sleep(4200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Session-close descendant survived long enough to write marker: %v", err)
	}
}

func TestC25RealOpenCodeMultiSessionRuntimeIsolation(t *testing.T) {
	rt, root := newC25RealOpenCodeRuntime(t)
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	for _, workspace := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "inside.txt"), []byte(filepath.Base(workspace)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessionA := mustOpenSession(t, rt, map[string]any{"project_id": "c25-isolation-a", "repo_root": workspaceA, "permission_mode": "project"})
	sessionB := mustOpenSession(t, rt, map[string]any{"project_id": "c25-isolation-b", "repo_root": workspaceB, "permission_mode": "project"})

	startAndWait := func(sessionID, file string) agentruntime.Record {
		t.Helper()
		id := startC25RealOpenCodeAgent(t, rt, sessionID,
			c25TaskPrompt(t, "read isolated file", []opencodeworker.Step{{Tool: "read", Arguments: map[string]any{"filePath": file}}}), "project", 30000)
		record, err := rt.agents.Wait(context.Background(), id)
		if err != nil || record.State != agentruntime.AgentStateSucceeded {
			t.Fatalf("isolated agent=%+v err=%v", record, err)
		}
		return record
	}
	startAndWait(sessionA.SessionID, filepath.Join(workspaceA, "inside.txt"))
	startAndWait(sessionB.SessionID, filepath.Join(workspaceB, "inside.txt"))

	runtimeA, okA := rt.openCode.Get(sessionA.Workspace(), opencoderuntime.PermissionProject)
	runtimeB, okB := rt.openCode.Get(sessionB.Workspace(), opencoderuntime.PermissionProject)
	if !okA || !okB || runtimeA.PID <= 0 || runtimeB.PID <= 0 || runtimeA.PID == runtimeB.PID {
		t.Fatalf("isolated runtimes A=%+v okA=%v B=%+v okB=%v", runtimeA, okA, runtimeB, okB)
	}
	if runtimeA.RuntimeSHA != "d5664f38002d8326eca179cf2efb5774c8ce1247" || runtimeB.RuntimeSHA != runtimeA.RuntimeSHA {
		t.Fatalf("runtime SHA isolation mismatch A=%+v B=%+v", runtimeA, runtimeB)
	}

	if _, err := rt.Call(context.Background(), "session_close", map[string]any{"session_id": sessionA.SessionID}); err != nil {
		t.Fatalf("close Session A: %v", err)
	}
	if _, exists := rt.openCode.Get(sessionA.Workspace(), opencoderuntime.PermissionProject); exists {
		t.Fatal("Session A runtime remained registered after close")
	}
	waitC25WindowsPIDGone(t, runtimeA.PID, 8*time.Second)
	if afterB, exists := rt.openCode.Get(sessionB.Workspace(), opencoderuntime.PermissionProject); !exists || afterB.PID != runtimeB.PID || !c25WindowsPIDAlive(runtimeB.PID) {
		t.Fatalf("Session B runtime affected by closing A: before=%+v after=%+v exists=%v", runtimeB, afterB, exists)
	}

	startAndWait(sessionB.SessionID, filepath.Join(workspaceB, "inside.txt"))
	if afterReuse, exists := rt.openCode.Get(sessionB.Workspace(), opencoderuntime.PermissionProject); !exists || afterReuse.PID != runtimeB.PID {
		t.Fatalf("Session B runtime was not reusable after closing A: before=%+v after=%+v exists=%v", runtimeB, afterReuse, exists)
	}
	if _, err := rt.Call(context.Background(), "session_close", map[string]any{"session_id": sessionB.SessionID}); err != nil {
		t.Fatalf("close Session B: %v", err)
	}
	waitC25WindowsPIDGone(t, runtimeB.PID, 8*time.Second)
}

func c25SlowPowerShellCommand(marker, pidFile string) string {
	return fmt.Sprintf(`powershell.exe -NoProfile -NonInteractive -Command "$PID | Set-Content -LiteralPath '%s' -Encoding ascii; Start-Sleep -Seconds 4; Set-Content -LiteralPath '%s' -Value orphan; Start-Sleep -Seconds 6"`, filepath.Base(pidFile), filepath.Base(marker))
}

func c25TimeoutPowerShellCommand(marker, pidFile string) string {
	return fmt.Sprintf(`powershell.exe -NoProfile -NonInteractive -Command "$PID | Set-Content -LiteralPath '%s' -Encoding ascii; Start-Sleep -Seconds 10; Set-Content -LiteralPath '%s' -Value orphan; Start-Sleep -Seconds 6"`, filepath.Base(pidFile), filepath.Base(marker))
}

func waitC25PIDFile(t *testing.T, path string, timeout time.Duration) int {
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
	t.Fatalf("timed out waiting for PID file %s", path)
	return 0
}

func c25WindowsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }", pid))
	return cmd.Run() == nil
}

func waitC25WindowsPIDGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !c25WindowsPIDAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("PID %d remained alive after %s", pid, timeout)
}
