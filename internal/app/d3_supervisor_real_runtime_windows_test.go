package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/nativebridge"
	"github.com/uvwt/agentdock/internal/opencodenative"
)

func TestRealRuntimeWindowsD3F1AppMultiTurnGate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows D3-F1 App/Tool production-routing gate")
	}
	baseURL := strings.TrimSpace(os.Getenv("AGENTDOCK_D3_NATIVE_URL"))
	manifest := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if baseURL == "" || manifest == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL and AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST for live D3-F1 App gate")
	}
	provider := strings.TrimSpace(os.Getenv("AGENTDOCK_D3D_PROVIDER"))
	modelID := strings.TrimSpace(os.Getenv("AGENTDOCK_D3D_MODEL"))
	if provider == "" {
		provider = "opencode"
	}
	if modelID == "" {
		modelID = "mimo-v2.5-free"
	}

	workspace := t.TempDir()
	token := fmt.Sprintf("D3F1_TOKEN_%d", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(workspace, "gate_input.txt"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	cfg := config.Config{
		AgentDockHome:           filepath.Join(stateRoot, "state"),
		AgentDockDefaultDir:     workspace,
		OpenCodeEnabled:         true,
		OpenCodeRuntimeManifest: manifest,
		OpenCodeLogDir:          filepath.Join(stateRoot, "opencode-logs"),
		OpenCodeNativeURL:       baseURL,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	record := mustOpenSession(t, rt, map[string]any{"project_id": "d3-f1-live", "repo_root": workspace})
	bridge, ok := rt.nativeSupervisor.(*nativebridge.Manager)
	if !ok || bridge == nil {
		t.Fatalf("production runtime did not instantiate durable nativebridge: %T", rt.nativeSupervisor)
	}
	binding, err := bridge.Start(context.Background(), nativebridge.StartRequest{
		AgentDockSessionID: record.SessionID,
		ClientRequestID:    "d3-f1-live-root",
		SubagentID:         "subagent-d3-f1-live",
		Workspace:          record.Workspace(),
		Provider:           provider,
		Model:              modelID,
		ExecutionMode:      "native-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	rootID := binding.OpenCodeSessionID
	defer deleteD3F1LiveNativeSession(t, baseURL, workspace, rootID)

	eventCtx, cancelEvents := context.WithCancel(context.Background())
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- bridge.ObserveEvents(eventCtx, record.SessionID, binding.SubagentID, record.Workspace(), func(_ opencodenative.NativeEvent, _ int64) error { return nil })
	}()
	defer func() {
		cancelEvents()
		select {
		case <-eventDone:
		case <-time.After(5 * time.Second):
		}
	}()

	taskAArgs := map[string]any{
		"session_id": record.SessionID, "subagent_id": binding.SubagentID, "client_message_id": "d3-f1-task-a", "kind": "TASK",
		"text": "You MUST use a file-reading tool to read gate_input.txt in the current workspace. Do not guess its contents. After reading it, reply with exactly two tokens separated by one space: D3F1_A_TOOL_OK followed by the file token. Do not use any other tool.",
	}
	taskAResult, err := rt.Call(context.Background(), "agent_supervisor_message", taskAArgs)
	if err != nil {
		t.Fatal(err)
	}
	taskA := decodeD3F1AppResult[nativebridge.SupervisorMessageResult](t, taskAResult)
	if taskA.OpenCodeSessionID != rootID || !taskA.Created || !taskA.Dispatched {
		t.Fatalf("task A routing mismatch: %+v", taskA)
	}
	waitD3F1AssistantText(t, rt, record.SessionID, binding.SubagentID, 120*time.Second, "D3F1_A_TOOL_OK", token)

	followupBArgs := map[string]any{
		"session_id": record.SessionID, "subagent_id": binding.SubagentID, "client_message_id": "d3-f1-followup-b", "kind": "FOLLOWUP",
		"text": "Without rereading gate_input.txt, recall the token from the previous turn and reply with exactly two tokens separated by one space: D3F1_B_MEMORY_OK followed by that remembered token. Do not use tools.",
	}
	followupBResult, err := rt.Call(context.Background(), "agent_supervisor_message", followupBArgs)
	if err != nil {
		t.Fatal(err)
	}
	followupB := decodeD3F1AppResult[nativebridge.SupervisorMessageResult](t, followupBResult)
	if followupB.OpenCodeSessionID != rootID || !followupB.Created || !followupB.Dispatched {
		t.Fatalf("follow-up B routing mismatch: %+v", followupB)
	}
	waitD3F1AssistantText(t, rt, record.SessionID, binding.SubagentID, 120*time.Second, "D3F1_B_MEMORY_OK", token)

	duplicateResult, err := rt.Call(context.Background(), "agent_supervisor_message", followupBArgs)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := decodeD3F1AppResult[nativebridge.SupervisorMessageResult](t, duplicateResult)
	if duplicate.Created || duplicate.Dispatched || duplicate.Message.SupervisorMessageID != followupB.Message.SupervisorMessageID || duplicate.Message.MessageSeq != followupB.Message.MessageSeq {
		t.Fatalf("duplicate client_message_id was not exactly-once: first=%+v duplicate=%+v", followupB, duplicate)
	}

	wrongResult, err := rt.Call(context.Background(), "agent_supervisor_message", map[string]any{
		"session_id": record.SessionID, "subagent_id": binding.SubagentID, "client_message_id": "d3-f1-wrong-direction", "kind": "FOLLOWUP",
		"text": "Enter a deliberately wrong direction. Do not use tools. Produce 700 numbered lines. Every line must begin D3F1_WRONG_DIRECTION_ACTIVE_ followed by its number. Do not summarize and do not stop early.",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := decodeD3F1AppResult[nativebridge.SupervisorMessageResult](t, wrongResult)
	if wrong.OpenCodeSessionID != rootID || !wrong.Created || !wrong.Dispatched {
		t.Fatalf("wrong-direction dispatch mismatch: %+v", wrong)
	}
	waitD3F1AppBusy(t, rt, record.SessionID, binding.SubagentID, rootID, 30*time.Second)

	correctionResult, err := rt.Call(context.Background(), "agent_supervisor_correct", map[string]any{
		"session_id": record.SessionID, "subagent_id": binding.SubagentID, "client_message_id": "d3-f1-correction-c", "mode": "graceful",
		"text": "Supervisor correction: stop the wrong direction. Do not use tools. Reply with exactly two tokens separated by one space: D3F1_C_GRACEFUL_OK followed by the token you remember from the first turn.",
	})
	if err != nil {
		t.Fatal(err)
	}
	correction := decodeD3F1AppResult[nativebridge.OrderedCorrectionResult](t, correctionResult)
	if correction.OpenCodeSessionIDBefore != rootID || correction.OpenCodeSessionIDAfter != rootID || correction.Aborted || correction.PreemptiveClaimed || !correction.Message.Created || !correction.Message.Dispatched {
		t.Fatalf("graceful correction hard gate mismatch: %+v", correction)
	}
	waitD3F1AssistantText(t, rt, record.SessionID, binding.SubagentID, 150*time.Second, "D3F1_C_GRACEFUL_OK", token)
	waitD3F1AppIdle(t, rt, record.SessionID, binding.SubagentID, rootID, 90*time.Second)

	finalResult, err := rt.Call(context.Background(), "agent_supervisor_observe", map[string]any{"session_id": record.SessionID, "subagent_id": binding.SubagentID, "after_cursor": 0, "limit": 128})
	if err != nil {
		t.Fatal(err)
	}
	finalObservation := decodeD3F1AppResult[nativebridge.SupervisorObservation](t, finalResult)
	persisted, err := rt.sessions.NativeSessionBinding(record.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if finalObservation.RootOpenCodeSessionID != rootID || persisted.OpenCodeSessionID != rootID {
		t.Fatalf("final root drifted: observation=%s persisted=%s want=%s", finalObservation.RootOpenCodeSessionID, persisted.OpenCodeSessionID, rootID)
	}
	if !(taskA.Message.MessageSeq < followupB.Message.MessageSeq && followupB.Message.MessageSeq < wrong.Message.MessageSeq && wrong.Message.MessageSeq < correction.Message.Message.MessageSeq) {
		t.Fatalf("durable message order mismatch A=%d B=%d wrong=%d C=%d", taskA.Message.MessageSeq, followupB.Message.MessageSeq, wrong.Message.MessageSeq, correction.Message.Message.MessageSeq)
	}
	if finalObservation.PublicEvents.LatestCursor == 0 {
		t.Fatalf("live event cursor did not advance: %+v", finalObservation.PublicEvents)
	}
	t.Logf("D3F1_LIVE_APP_ROOT=%s A_ID=%s A_SEQ=%d B_ID=%s B_SEQ=%d DUP_ID=%s WRONG_ID=%s WRONG_SEQ=%d C_ID=%s C_SEQ=%d EVENT_CURSOR=%d EXECUTION_STATE=%s", rootID, taskA.Message.SupervisorMessageID, taskA.Message.MessageSeq, followupB.Message.SupervisorMessageID, followupB.Message.MessageSeq, duplicate.Message.SupervisorMessageID, wrong.Message.SupervisorMessageID, wrong.Message.MessageSeq, correction.Message.Message.SupervisorMessageID, correction.Message.Message.MessageSeq, finalObservation.PublicEvents.LatestCursor, persisted.ExecutionState)
}

func decodeD3F1AppResult[T any](t *testing.T, value Result) T {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func observeD3F1App(t *testing.T, rt *Runtime, sessionID, subagentID string) nativebridge.SupervisorObservation {
	t.Helper()
	result, err := rt.Call(context.Background(), "agent_supervisor_observe", map[string]any{"session_id": sessionID, "subagent_id": subagentID, "after_cursor": 0, "limit": 128})
	if err != nil {
		t.Fatal(err)
	}
	return decodeD3F1AppResult[nativebridge.SupervisorObservation](t, result)
}

func waitD3F1AssistantText(t *testing.T, rt *Runtime, sessionID, subagentID string, timeout time.Duration, required ...string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		observation := observeD3F1App(t, rt, sessionID, subagentID)
		for _, owned := range observation.Messages {
			if !strings.EqualFold(owned.Message.Info.Role, "assistant") {
				continue
			}
			for _, raw := range owned.Message.Parts {
				var part struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(raw, &part) != nil || part.Text == "" {
					continue
				}
				matched := true
				for _, needle := range required {
					if !strings.Contains(part.Text, needle) {
						matched = false
						break
					}
				}
				if matched {
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("assistant response containing %q not observed within %s", required, timeout)
}

func waitD3F1AppBusy(t *testing.T, rt *Runtime, sessionID, subagentID, rootID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		observation := observeD3F1App(t, rt, sessionID, subagentID)
		if _, ok := observation.Statuses[rootID]; ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("root %s did not become busy within %s", rootID, timeout)
}

func waitD3F1AppIdle(t *testing.T, rt *Runtime, sessionID, subagentID, rootID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		observation := observeD3F1App(t, rt, sessionID, subagentID)
		if _, ok := observation.Statuses[rootID]; !ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("root %s did not become idle within %s", rootID, timeout)
}

func deleteD3F1LiveNativeSession(t *testing.T, baseURL, workspace, sessionID string) {
	t.Helper()
	endpoint := fmt.Sprintf("%s/session/%s?directory=%s", strings.TrimRight(baseURL, "/"), url.PathEscape(sessionID), url.QueryEscape(workspace))
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		t.Logf("D3-F1 live cleanup request: %v", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("D3-F1 live cleanup failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Logf("D3-F1 live cleanup returned HTTP %d", resp.StatusCode)
	}
}
