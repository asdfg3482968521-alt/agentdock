//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/nativebridge"
	"github.com/uvwt/agentdock/internal/opencodeautonomous"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

const d3G1LiveRuntimeSHA = "d5664f38002d8326eca179cf2efb5774c8ce1247"

func TestD3G1RealWindowsProductionABCPoolGate(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("AGENTDOCK_D3_NATIVE_URL"))
	manifest := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if baseURL == "" || manifest == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL and AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST for D3-G1 live gate")
	}
	manifestBody, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read frozen OpenCode manifest: %v", err)
	}
	if !strings.Contains(string(manifestBody), d3G1LiveRuntimeSHA) {
		t.Fatalf("OpenCode manifest is not frozen runtime %s: %s", d3G1LiveRuntimeSHA, manifest)
	}

	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	workspaces := map[string]string{}
	tokens := map[string]string{}
	for _, label := range []string{"A", "B", "C"} {
		workspace := filepath.Join(root, "workspace-"+strings.ToLower(label))
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		workspaces[label] = workspace
		tokens[label] = writeD3G1LiveFixture(t, workspace, label, d3G1LiveFixtureSleepSeconds(t, label))
	}

	cfg := config.Config{
		AgentDockHome:           stateRoot,
		AgentDockDefaultDir:     root,
		OpenCodeEnabled:         true,
		OpenCodeRuntimeManifest: manifest,
		OpenCodeLogDir:          filepath.Join(root, "opencode-logs"),
		OpenCodeNativeURL:       baseURL,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}

	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime(D3-G1): %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = rt.Close()
		}
	}()
	bridge, ok := rt.nativeSupervisor.(*nativebridge.Manager)
	if !ok || bridge == nil || rt.nativeAutonomous == nil {
		t.Fatalf("production D3-G1 wiring missing supervisor=%T autonomous=%T", rt.nativeSupervisor, rt.nativeAutonomous)
	}

	sessions := map[string]sessionruntime.Record{}
	agents := map[string]string{}
	bindings := map[string]sessionruntime.NativeSessionBinding{}
	for _, label := range []string{"A", "B", "C"} {
		record := mustOpenSession(t, rt, map[string]any{
			"project_id":      "d3-g1-live-" + strings.ToLower(label),
			"repo_root":       workspaces[label],
			"permission_mode": "project",
		})
		sessions[label] = record
		agents[label] = startD3G1LiveNativeAgent(t, rt, record.SessionID, label, tokens[label])
	}

	for _, label := range []string{"A", "B", "C"} {
		bindings[label] = waitD3G1LiveBinding(t, rt, sessions[label].SessionID, agents[label], 90*time.Second)
		defer deleteD3F1LiveNativeSession(t, baseURL, workspaces[label], bindings[label].OpenCodeSessionID)
	}
	if bindings["A"].OpenCodeSessionID == bindings["B"].OpenCodeSessionID || bindings["A"].OpenCodeSessionID == bindings["C"].OpenCodeSessionID || bindings["B"].OpenCodeSessionID == bindings["C"].OpenCodeSessionID {
		t.Fatalf("A/B/C root ids must be distinct: A=%s B=%s C=%s", bindings["A"].OpenCodeSessionID, bindings["B"].OpenCodeSessionID, bindings["C"].OpenCodeSessionID)
	}
	seenWorkspace := map[string]bool{}
	seenRuntimeScope := map[string]bool{}
	for _, label := range []string{"A", "B", "C"} {
		binding := bindings[label]
		record := sessions[label]
		if binding.SubagentID != agents[label] || binding.AgentDockSessionID != record.SessionID || binding.Workspace != record.Workspace() {
			t.Fatalf("%s root authority mismatch binding=%+v record=%+v agent=%s", label, binding, record, agents[label])
		}
		if binding.RuntimeScope != "session:"+record.SessionID || binding.PermissionMode != "project" {
			t.Fatalf("%s runtime authority mismatch binding=%+v", label, binding)
		}
		if binding.Provider != "jiyuan" || binding.Model != "deepseek-flash" || binding.Variant != "max" || binding.ExecutionMode != opencodeautonomous.ExecutionModeAutonomous {
			t.Fatalf("%s native identity mismatch binding=%+v", label, binding)
		}
		if seenWorkspace[binding.Workspace] || seenRuntimeScope[binding.RuntimeScope] {
			t.Fatalf("%s reused workspace/runtime scope", label)
		}
		seenWorkspace[binding.Workspace] = true
		seenRuntimeScope[binding.RuntimeScope] = true
	}

	// Each autonomous root must have reached a real bash tool before we mutate
	// any one root. The scripts remain sleeping at this point, proving overlap.
	for _, label := range []string{"A", "B", "C"} {
		waitD3G1LiveFile(t, filepath.Join(workspaces[label], strings.ToLower(label)+"-ready.txt"), 90*time.Second)
		record, getErr := rt.agents.Get(sessions[label].SessionID, agents[label])
		if getErr != nil || record.State != agentruntime.AgentStateRunning {
			t.Fatalf("%s must still be RUNNING during overlap record=%+v err=%v", label, record, getErr)
		}
	}

	correctionResult, err := rt.Call(context.Background(), "agent_supervisor_correct", map[string]any{
		"session_id":        sessions["A"].SessionID,
		"subagent_id":       agents["A"],
		"client_message_id": "d3-g1-live-a-correction",
		"mode":              "graceful",
		"text":              "Supervisor correction for A only. Do not use tools. After the current turn, reply exactly D3G1_A_CORRECT_OK.",
	})
	if err != nil {
		t.Fatalf("A-only correction: %v", err)
	}
	correction := decodeD3F1AppResult[nativebridge.OrderedCorrectionResult](t, correctionResult)
	if correction.OpenCodeSessionIDBefore != bindings["A"].OpenCodeSessionID || correction.OpenCodeSessionIDAfter != bindings["A"].OpenCodeSessionID || correction.Aborted || correction.PreemptiveClaimed || !correction.Message.Created || !correction.Message.Dispatched {
		t.Fatalf("A-only correction root semantics=%+v", correction)
	}

	cancelResult, err := rt.Call(context.Background(), "agent_cancel", map[string]any{
		"session_id": sessions["B"].SessionID,
		"agent_id":   agents["B"],
		"reason":     "D3-G1 targeted B cancel",
	})
	if err != nil {
		t.Fatalf("B-only cancel: %v", err)
	}
	if cancelResult["state"] != string(agentruntime.AgentStateCancelled) {
		t.Fatalf("B cancel result=%+v", cancelResult)
	}
	bRecord, err := rt.agents.Wait(context.Background(), agents["B"])
	if err != nil || bRecord.State != agentruntime.AgentStateCancelled {
		t.Fatalf("B terminal state record=%+v err=%v", bRecord, err)
	}
	if _, statErr := os.Stat(filepath.Join(workspaces["B"], "b-done.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("B cancelled execution reached terminal marker: %v", statErr)
	}

	// A and C must be unaffected by B cancellation and retain their exact roots.
	for _, label := range []string{"A", "C"} {
		record, getErr := rt.agents.Get(sessions[label].SessionID, agents[label])
		if getErr != nil || record.State != agentruntime.AgentStateRunning {
			t.Fatalf("%s corrupted by B cancel record=%+v err=%v", label, record, getErr)
		}
		current, bindErr := rt.sessions.NativeSessionBinding(sessions[label].SessionID, agents[label])
		if bindErr != nil || current.OpenCodeSessionID != bindings[label].OpenCodeSessionID {
			t.Fatalf("%s root drift after B cancel before=%+v after=%+v err=%v", label, bindings[label], current, bindErr)
		}
	}

	aRecord, err := rt.agents.Wait(context.Background(), agents["A"])
	if err != nil || aRecord.State != agentruntime.AgentStateSucceeded {
		t.Fatalf("A first task record=%+v err=%v", aRecord, err)
	}
	cRecord, err := rt.agents.Wait(context.Background(), agents["C"])
	if err != nil || cRecord.State != agentruntime.AgentStateSucceeded {
		t.Fatalf("C task record=%+v err=%v", cRecord, err)
	}
	waitD3G1LiveFile(t, filepath.Join(workspaces["A"], "a-done.txt"), 10*time.Second)
	waitD3G1LiveFile(t, filepath.Join(workspaces["C"], "c-done.txt"), 10*time.Second)
	waitD3F1AssistantText(t, rt, sessions["A"].SessionID, agents["A"], 90*time.Second, "D3G1_A_CORRECT_OK")
	waitD3F1AppIdle(t, rt, sessions["A"].SessionID, agents["A"], bindings["A"].OpenCodeSessionID, 90*time.Second)

	// Cross-root public supervisor authority must fail before touching the native plane.
	if _, err := rt.Call(context.Background(), "agent_supervisor_observe", map[string]any{
		"session_id": sessions["A"].SessionID, "subagent_id": agents["B"], "after_cursor": 0, "limit": 16,
	}); err == nil {
		t.Fatal("cross-root observe A->B unexpectedly succeeded")
	}
	if _, err := rt.Call(context.Background(), "agent_supervisor_message", map[string]any{
		"session_id": sessions["A"].SessionID, "subagent_id": agents["C"], "client_message_id": "d3-g1-cross-root-message", "kind": "FOLLOWUP", "text": "must be denied",
	}); err == nil {
		t.Fatal("cross-root message A->C unexpectedly succeeded")
	}
	if err := bridge.Abort(context.Background(), sessions["A"].SessionID, agents["B"], sessions["A"].Workspace()); nativebridge.ErrorCode(err) != nativebridge.ErrorOwnership {
		t.Fatalf("cross-root abort code=%s err=%v", nativebridge.ErrorCode(err), err)
	}
	if _, err := rt.Call(context.Background(), "agent_supervisor_observe", map[string]any{
		"session_id": sessions["A"].SessionID, "subagent_id": bindings["B"].OpenCodeSessionID, "after_cursor": 0, "limit": 16,
	}); err == nil {
		t.Fatal("raw OpenCodeSessionID unexpectedly authorized public observe")
	}
	if _, _, err := bridge.Attach(context.Background(), sessions["B"].SessionID, "ses-foreign-descendant-token", sessions["B"].Workspace()); nativebridge.ErrorCode(err) != nativebridge.ErrorOwnership {
		t.Fatalf("foreign descendant token code=%s err=%v", nativebridge.ErrorCode(err), err)
	}

	// A repeated logical native start for the same AgentID must reconcile to the
	// existing root, never create a replacement root.
	duplicateA, err := bridge.Start(context.Background(), nativebridge.StartRequest{
		AgentDockSessionID: sessions["A"].SessionID,
		ClientRequestID:    agents["A"],
		SubagentID:         agents["A"],
		Workspace:          sessions["A"].Workspace(),
		Provider:           "jiyuan",
		Model:              "deepseek-flash",
		Variant:            "max",
		ExecutionMode:      opencodeautonomous.ExecutionModeAutonomous,
	})
	if err != nil || duplicateA.OpenCodeSessionID != bindings["A"].OpenCodeSessionID {
		t.Fatalf("duplicate logical A start created/drifted root before=%+v after=%+v err=%v", bindings["A"], duplicateA, err)
	}

	aFollowRaw, err := rt.Call(context.Background(), "agent_supervisor_message", map[string]any{
		"session_id": sessions["A"].SessionID, "subagent_id": agents["A"], "client_message_id": "d3-g1-live-a-followup", "kind": "FOLLOWUP",
		"text": "Do not use tools. Reply exactly D3G1_A_FOLLOWUP_OK.",
	})
	if err != nil {
		t.Fatalf("A follow-up: %v", err)
	}
	aFollow := decodeD3F1AppResult[nativebridge.SupervisorMessageResult](t, aFollowRaw)
	if aFollow.OpenCodeSessionID != bindings["A"].OpenCodeSessionID || !aFollow.Created || !aFollow.Dispatched {
		t.Fatalf("A follow-up root drift=%+v", aFollow)
	}
	cFollowRaw, err := rt.Call(context.Background(), "agent_supervisor_message", map[string]any{
		"session_id": sessions["C"].SessionID, "subagent_id": agents["C"], "client_message_id": "d3-g1-live-c-followup", "kind": "FOLLOWUP",
		"text": "Do not use tools. Reply exactly D3G1_C_FOLLOWUP_OK.",
	})
	if err != nil {
		t.Fatalf("C follow-up: %v", err)
	}
	cFollow := decodeD3F1AppResult[nativebridge.SupervisorMessageResult](t, cFollowRaw)
	if cFollow.OpenCodeSessionID != bindings["C"].OpenCodeSessionID || !cFollow.Created || !cFollow.Dispatched {
		t.Fatalf("C follow-up root drift=%+v", cFollow)
	}
	waitD3F1AssistantText(t, rt, sessions["A"].SessionID, agents["A"], 90*time.Second, "D3G1_A_FOLLOWUP_OK")
	waitD3F1AssistantText(t, rt, sessions["C"].SessionID, agents["C"], 90*time.Second, "D3G1_C_FOLLOWUP_OK")
	waitD3F1AppIdle(t, rt, sessions["A"].SessionID, agents["A"], bindings["A"].OpenCodeSessionID, 90*time.Second)
	waitD3F1AppIdle(t, rt, sessions["C"].SessionID, agents["C"], bindings["C"].OpenCodeSessionID, 90*time.Second)
	if correction.Message.Message.MessageSeq >= aFollow.Message.MessageSeq || cFollow.Message.MessageSeq != 1 {
		t.Fatalf("per-root supervisor message ordering not independent: A correction=%d A followup=%d C followup=%d", correction.Message.Message.MessageSeq, aFollow.Message.MessageSeq, cFollow.Message.MessageSeq)
	}

	// Public output cursors are root-scoped. Each root must expose only its own
	// autonomous telemetry, and consuming A cannot rewrite B/C cursor values.
	outputs := map[string]Result{}
	for _, label := range []string{"A", "B", "C"} {
		value, outErr := rt.Call(context.Background(), "agent_output", map[string]any{
			"session_id": sessions[label].SessionID, "agent_id": agents[label], "cursor": 0, "limit": 262144,
		})
		if outErr != nil {
			t.Fatalf("agent_output %s: %v", label, outErr)
		}
		outputs[label] = value
		text := fmt.Sprint(value["output"])
		if !strings.Contains(text, "[opencode-native]") {
			t.Fatalf("%s missing autonomous telemetry: %q", label, text)
		}
	}
	bCursorBefore := outputs["B"]["next_cursor"]
	cCursorBefore := outputs["C"]["next_cursor"]
	if _, err := rt.Call(context.Background(), "agent_output", map[string]any{
		"session_id": sessions["A"].SessionID, "agent_id": agents["A"], "cursor": outputs["A"]["next_cursor"], "limit": 65536,
	}); err != nil {
		t.Fatalf("advance A output cursor: %v", err)
	}
	bAgain, _ := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": sessions["B"].SessionID, "agent_id": agents["B"], "cursor": 0, "limit": 262144})
	cAgain, _ := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": sessions["C"].SessionID, "agent_id": agents["C"], "cursor": 0, "limit": 262144})
	if fmt.Sprint(bAgain["next_cursor"]) != fmt.Sprint(bCursorBefore) || fmt.Sprint(cAgain["next_cursor"]) != fmt.Sprint(cCursorBefore) {
		t.Fatalf("A output cursor read altered siblings: B %v->%v C %v->%v", bCursorBefore, bAgain["next_cursor"], cCursorBefore, cAgain["next_cursor"])
	}

	// Focused service re-init: all three durable mappings must point to the exact
	// same native roots. Re-init itself must not create a replacement root.
	if err := rt.Close(); err != nil {
		t.Fatalf("close runtime before re-init: %v", err)
	}
	closed = true
	rt2, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime re-init: %v", err)
	}
	defer rt2.Close()
	bridge2, ok := rt2.nativeSupervisor.(*nativebridge.Manager)
	if !ok || bridge2 == nil {
		t.Fatalf("re-init supervisor=%T", rt2.nativeSupervisor)
	}
	for _, label := range []string{"A", "B", "C"} {
		recovered, bindErr := rt2.sessions.NativeSessionBinding(sessions[label].SessionID, agents[label])
		if bindErr != nil || recovered.OpenCodeSessionID != bindings[label].OpenCodeSessionID || recovered.SubagentID != agents[label] {
			t.Fatalf("re-init root %s drift before=%+v after=%+v err=%v", label, bindings[label], recovered, bindErr)
		}
		duplicate, startErr := bridge2.Start(context.Background(), nativebridge.StartRequest{
			AgentDockSessionID: sessions[label].SessionID,
			ClientRequestID:    agents[label],
			SubagentID:         agents[label],
			Workspace:          sessions[label].Workspace(),
			Provider:           "jiyuan",
			Model:              "deepseek-flash",
			Variant:            "max",
			ExecutionMode:      opencodeautonomous.ExecutionModeAutonomous,
		})
		if startErr != nil || duplicate.OpenCodeSessionID != bindings[label].OpenCodeSessionID {
			t.Fatalf("re-init duplicate %s created replacement before=%s after=%s err=%v", label, bindings[label].OpenCodeSessionID, duplicate.OpenCodeSessionID, startErr)
		}
	}

	t.Logf("D3G1_WINDOWS_GATE PASS A_AGENT=%s A_ROOT=%s B_AGENT=%s B_ROOT=%s C_AGENT=%s C_ROOT=%s A_CORRECTION_SEQ=%d A_FOLLOWUP_SEQ=%d C_FOLLOWUP_SEQ=%d",
		agents["A"], bindings["A"].OpenCodeSessionID,
		agents["B"], bindings["B"].OpenCodeSessionID,
		agents["C"], bindings["C"].OpenCodeSessionID,
		correction.Message.Message.MessageSeq, aFollow.Message.MessageSeq, cFollow.Message.MessageSeq,
	)
}

func d3G1LiveFixtureSleepSeconds(t *testing.T, label string) int {
	t.Helper()
	defaults := map[string]int{"A": 20, "B": 60, "C": 25}
	seconds := defaults[label]
	override := strings.TrimSpace(os.Getenv("AGENTDOCK_D3_G1_FIXTURE_SLEEP_SECONDS"))
	if override == "" {
		return seconds
	}
	parsed, err := strconv.Atoi(override)
	if err != nil || parsed < 1 {
		t.Fatalf("AGENTDOCK_D3_G1_FIXTURE_SLEEP_SECONDS must be a positive integer, got %q", override)
	}
	return parsed
}

func writeD3G1LiveFixture(t *testing.T, workspace, label string, sleepSeconds int) string {
	t.Helper()
	lower := strings.ToLower(label)
	token := fmt.Sprintf("D3G1_%s_TOKEN_%d", label, time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("Set-StrictMode -Version Latest\r\n$ErrorActionPreference = 'Stop'\r\nSet-Content -LiteralPath '%s-ready.txt' -Value 'READY_%s' -NoNewline\r\nStart-Sleep -Seconds %d\r\nSet-Content -LiteralPath '%s-done.txt' -Value 'DONE_%s' -NoNewline\r\n", lower, label, sleepSeconds, lower, label)
	if err := os.WriteFile(filepath.Join(workspace, "run.ps1"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	return token
}

func startD3G1LiveNativeAgent(t *testing.T, rt *Runtime, sessionID, label, token string) string {
	t.Helper()
	prompt := fmt.Sprintf("D3-G1 real Windows root %s. You MUST use the read tool to read input.txt in the current workspace first. Confirm it contains token %s. Then you MUST use the bash tool to run exactly this command from the current workspace: powershell -NoProfile -ExecutionPolicy Bypass -File .\\run.ps1 . Do not edit any file yourself. The script intentionally sleeps; wait for it to finish. After the bash command returns, reply exactly: D3G1_%s_FIRST_OK %s", label, token, label, token)
	result, err := rt.Call(context.Background(), "agent_start", map[string]any{
		"session_id":     sessionID,
		"provider":       d3NativeAutonomousProvider,
		"model":          nil,
		"role":           "implementation",
		"credential_ref": "cred_d3_g1_local_" + strings.ToLower(label),
		"prompt":         prompt,
		"permission":     "project",
		"timeout_ms":     180000,
		"budget":         map[string]any{"max_requests": 1},
	})
	if err != nil {
		t.Fatalf("agent_start %s: %v", label, err)
	}
	agent := publicAgentFromResult(t, result)
	id, _ := agent["agent_id"].(string)
	if strings.TrimSpace(id) == "" {
		t.Fatalf("agent_start %s result=%+v", label, result)
	}
	return id
}

func waitD3G1LiveBinding(t *testing.T, rt *Runtime, sessionID, agentID string, timeout time.Duration) sessionruntime.NativeSessionBinding {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		binding, err := rt.sessions.NativeSessionBinding(sessionID, agentID)
		if err == nil && strings.TrimSpace(binding.OpenCodeSessionID) != "" {
			return binding
		}
		lastErr = err
		if record, getErr := rt.agents.Get(sessionID, agentID); getErr == nil && record.State.Terminal() {
			output, _ := rt.agents.Output(sessionID, agentID, 0, 262144)
			t.Fatalf("agent became terminal before native binding agent=%s state=%s failure=%v output=%q binding_err=%v", agentID, record.State, record.FailureReason, output.Text, lastErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	record, _ := rt.agents.Get(sessionID, agentID)
	output, _ := rt.agents.Output(sessionID, agentID, 0, 262144)
	t.Fatalf("native binding agent=%s not available within %s: state=%s failure=%v output=%q binding_err=%v", agentID, timeout, record.State, record.FailureReason, output.Text, lastErr)
	return sessionruntime.NativeSessionBinding{}
}

func waitD3G1LiveFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("file %s not observed within %s", path, timeout)
}
