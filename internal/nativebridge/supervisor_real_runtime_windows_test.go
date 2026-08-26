package nativebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

func TestRealRuntimeWindowsSupervisorCorrectionGate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows supervisor correction gate")
	}
	baseURL := strings.TrimSpace(os.Getenv("AGENTDOCK_D3_NATIVE_URL"))
	workspace := strings.TrimSpace(os.Getenv("AGENTDOCK_D3_NATIVE_WORKSPACE"))
	if baseURL == "" || workspace == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL and AGENTDOCK_D3_NATIVE_WORKSPACE for live supervisor gate")
	}
	provider := strings.TrimSpace(os.Getenv("AGENTDOCK_D3D_PROVIDER"))
	modelID := strings.TrimSpace(os.Getenv("AGENTDOCK_D3D_MODEL"))
	if provider == "" {
		provider = "opencode"
	}
	if modelID == "" {
		modelID = "mimo-v2.5-free"
	}

	stateRoot := t.TempDir()
	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: stateRoot + `\sessions`, WorktreeRoot: stateRoot + `\worktrees`, Git: testGit{}, IDFactory: func() string { return "agentdock-d3-d-live" },
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3-d-live", RepoRoot: workspace, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := opencodenative.NewClient(
		opencodenative.Config{BaseURL: baseURL, ReadinessTimeout: 30 * time.Second, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 500 * time.Millisecond},
		&http.Client{Timeout: 180 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := New(Options{Sessions: manager, Native: client, SubagentIDFactory: func() string { return "subagent-d3-d-live" }, NativeRuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", NativeServerIdentity: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	startRequest := StartRequest{AgentDockSessionID: record.SessionID, ClientRequestID: "d3-d-live-start", Workspace: record.Workspace(), Provider: provider, Model: modelID, ExecutionMode: "native-session"}
	binding, err := bridge.Start(context.Background(), startRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteLiveNativeSession(t, baseURL, workspace, binding.OpenCodeSessionID)
	t.Logf("D3D_LIVE_ROOT=%s AGENTDOCK_SESSION=%s SUBAGENT=%s", binding.OpenCodeSessionID, record.SessionID, binding.SubagentID)

	model := &opencodenative.ModelRef{ProviderID: provider, ModelID: modelID}
	eventCtx, cancelEvents := context.WithCancel(context.Background())
	var eventCount atomic.Int64
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- bridge.ObserveEvents(eventCtx, record.SessionID, binding.SubagentID, workspace, func(_ opencodenative.NativeEvent, _ int64) error {
			eventCount.Add(1)
			return nil
		})
	}()
	defer func() {
		cancelEvents()
		select {
		case <-eventDone:
		case <-time.After(5 * time.Second):
		}
	}()

	wrong := opencodenative.TextPrompt("This is deliberately the WRONG DIRECTION for a correction gate. Do not use tools. Produce 240 numbered lines. Every line must begin WRONG_DIRECTION_ACTIVE_ and then its number. Do not summarize and do not stop early.")
	wrong.Model = model
	if err := bridge.SubmitPromptAsync(context.Background(), record.SessionID, binding.SubagentID, workspace, wrong); err != nil {
		t.Fatal(err)
	}
	if err := waitSupervisorBusy(context.Background(), bridge, record.SessionID, binding.SubagentID, workspace, binding.OpenCodeSessionID, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	activeObservation, err := bridge.ObserveSupervisor(context.Background(), record.SessionID, binding.SubagentID, workspace, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := activeObservation.Statuses[binding.OpenCodeSessionID]; !ok {
		t.Fatalf("root not visible in current activity while busy: %+v", activeObservation.Statuses)
	}

	gracefulPrompt := opencodenative.TextPrompt("Supervisor correction: stop following the wrong direction. Reply exactly D3D_GRACEFUL_CORRECTED and nothing else. Do not use tools.")
	gracefulPrompt.Model = model
	graceful, err := bridge.Correct(context.Background(), record.SessionID, binding.SubagentID, workspace, CorrectionRequest{Mode: CorrectionGraceful, Prompt: gracefulPrompt})
	if err != nil {
		t.Fatal(err)
	}
	if graceful.OpenCodeSessionIDBefore != binding.OpenCodeSessionID || graceful.OpenCodeSessionIDAfter != binding.OpenCodeSessionID || graceful.Aborted || graceful.PreemptiveClaimed {
		t.Fatalf("graceful hard gate mismatch: %+v", graceful)
	}
	if err := waitSupervisorMarker(context.Background(), bridge, record.SessionID, binding.SubagentID, workspace, "D3D_GRACEFUL_CORRECTED", 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitSupervisorIdle(context.Background(), bridge, record.SessionID, binding.SubagentID, workspace, 60*time.Second); err != nil {
		t.Fatal(err)
	}

	forceWrong := opencodenative.TextPrompt("This is deliberately another WRONG DIRECTION. Do not use tools. Produce 400 numbered lines. Every line must begin FORCE_WRONG_DIRECTION_ACTIVE_ and then its number. Do not summarize and do not stop early.")
	forceWrong.Model = model
	if err := bridge.SubmitPromptAsync(context.Background(), record.SessionID, binding.SubagentID, workspace, forceWrong); err != nil {
		t.Fatal(err)
	}
	if err := waitSupervisorBusy(context.Background(), bridge, record.SessionID, binding.SubagentID, workspace, binding.OpenCodeSessionID, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	forcePrompt := opencodenative.TextPrompt("Force supervisor correction after abort: reply exactly D3D_FORCE_CORRECTED and nothing else. Do not use tools.")
	forcePrompt.Model = model
	force, err := bridge.Correct(context.Background(), record.SessionID, binding.SubagentID, workspace, CorrectionRequest{Mode: CorrectionForce, Prompt: forcePrompt})
	if err != nil {
		t.Fatal(err)
	}
	if force.OpenCodeSessionIDBefore != binding.OpenCodeSessionID || force.OpenCodeSessionIDAfter != binding.OpenCodeSessionID || !force.Aborted || force.PreemptiveClaimed {
		t.Fatalf("force hard gate mismatch: %+v", force)
	}
	if err := waitSupervisorMarker(context.Background(), bridge, record.SessionID, binding.SubagentID, workspace, "D3D_FORCE_CORRECTED", 120*time.Second); err != nil {
		t.Fatal(err)
	}

	items, err := client.ListNativeSessions(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	rootMatches := 0
	for _, item := range items {
		if item.ParentID == "" && metadataString(item.Metadata, metadataSessionID) == record.SessionID && metadataString(item.Metadata, metadataClientRequestID) == startRequest.ClientRequestID {
			rootMatches++
			if item.ID != binding.OpenCodeSessionID {
				t.Fatalf("replacement native root observed: got=%s want=%s", item.ID, binding.OpenCodeSessionID)
			}
		}
	}
	if rootMatches != 1 {
		t.Fatalf("same logical supervisor root count=%d want=1", rootMatches)
	}

	deadline := time.Now().Add(10 * time.Second)
	for eventCount.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	observation, err := bridge.ObserveSupervisor(context.Background(), record.SessionID, binding.SubagentID, workspace, 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	if observation.RootOpenCodeSessionID != binding.OpenCodeSessionID || len(observation.Messages) == 0 || len(observation.PublicEvents.Events) == 0 || eventCount.Load() == 0 {
		t.Fatalf("live supervisor observability incomplete messages=%d events=%d stream=%d root=%s", len(observation.Messages), len(observation.PublicEvents.Events), eventCount.Load(), observation.RootOpenCodeSessionID)
	}
	persisted, err := manager.NativeSessionBinding(record.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.OpenCodeSessionID != binding.OpenCodeSessionID || persisted.PendingSupervisorMessageID != "" || persisted.LastEventCursor == 0 {
		t.Fatalf("live durable supervisor state mismatch: %+v", persisted)
	}
	t.Logf("D3D_GRACEFUL_SAME_ID=%s D3D_FORCE_SAME_ID=%s D3D_PUBLIC_CURSOR=%d D3D_STREAM_EVENTS=%d", graceful.OpenCodeSessionIDAfter, force.OpenCodeSessionIDAfter, persisted.LastEventCursor, eventCount.Load())
}

func waitSupervisorBusy(ctx context.Context, bridge *Manager, agentDockSessionID, subagentID, workspace, rootID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := bridge.ObserveStatus(ctx, agentDockSessionID, subagentID, workspace)
		if err == nil && status.Present && status.OpenCodeSessionID == rootID {
			return nil
		}
		if err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("native root %s did not become busy within %s", rootID, timeout)
}

func waitSupervisorIdle(ctx context.Context, bridge *Manager, agentDockSessionID, subagentID, workspace string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := bridge.ObserveStatus(ctx, agentDockSessionID, subagentID, workspace)
		if err != nil {
			return err
		}
		if !status.Present {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("native root did not become idle within %s", timeout)
}

func waitSupervisorMarker(ctx context.Context, bridge *Manager, agentDockSessionID, subagentID, workspace, marker string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		messages, err := bridge.ListMessages(ctx, agentDockSessionID, subagentID, workspace)
		if err != nil {
			return err
		}
		if nativeMessagesContain(messages, marker) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("marker %q not observed within %s", marker, timeout)
}

func nativeMessagesContain(messages []opencodenative.Message, marker string) bool {
	for _, message := range messages {
		for _, raw := range message.Parts {
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &part) == nil && strings.Contains(part.Text, marker) {
				return true
			}
		}
	}
	return false
}
