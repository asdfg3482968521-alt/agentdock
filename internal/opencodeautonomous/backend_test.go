package opencodeautonomous

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/nativebridge"
	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

type fakeNativeBridge struct {
	mu      sync.Mutex
	starts  []nativebridge.StartRequest
	prompts []opencodenative.PromptRequest
	aborts  int
	binding session.NativeSessionBinding
	message opencodenative.Message
	submit  func(context.Context, opencodenative.PromptRequest) (opencodenative.Message, error)
	observe func(context.Context, func(opencodenative.NativeEvent, int64) error) error
}

func (f *fakeNativeBridge) Start(_ context.Context, req nativebridge.StartRequest) (session.NativeSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, req)
	binding := f.binding
	if binding.AgentDockSessionID == "" {
		binding.AgentDockSessionID = req.AgentDockSessionID
	}
	if binding.SubagentID == "" {
		binding.SubagentID = "subagent-d3c"
	}
	if binding.OpenCodeSessionID == "" {
		binding.OpenCodeSessionID = "native-root"
	}
	if binding.Workspace == "" {
		binding.Workspace = req.Workspace
	}
	if binding.Provider == "" {
		binding.Provider = req.Provider
	}
	if binding.Model == "" {
		binding.Model = req.Model
	}
	return binding, nil
}

func (f *fakeNativeBridge) SubmitMessage(ctx context.Context, _, _, _ string, prompt opencodenative.PromptRequest) (opencodenative.Message, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	if f.submit != nil {
		return f.submit(ctx, prompt)
	}
	return f.message, nil
}

func (f *fakeNativeBridge) ObserveEvents(ctx context.Context, _, _, _ string, fn func(opencodenative.NativeEvent, int64) error) error {
	if f.observe != nil {
		return f.observe(ctx, fn)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeNativeBridge) Abort(_ context.Context, _, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts++
	return nil
}

func textMessage(sessionID, text string) opencodenative.Message {
	part, _ := json.Marshal(map[string]any{"type": "text", "text": text})
	return opencodenative.Message{Info: opencodenative.MessageInfo{ID: "msg-final", SessionID: sessionID, Role: "assistant"}, Parts: []json.RawMessage{part}}
}

func toolEvent(sessionID, callID, tool, status string) opencodenative.NativeEvent {
	props, _ := json.Marshal(map[string]any{
		"sessionID": sessionID,
		"part":      map[string]any{"type": "tool", "tool": tool, "callID": callID, "state": map[string]any{"status": status}},
	})
	return opencodenative.NativeEvent{ID: "evt-" + callID + "-" + status, Type: "message.part.updated", Properties: props}
}

func TestRunUsesHighLevelTaskAndExactRootBinding(t *testing.T) {
	bridge := &fakeNativeBridge{message: textMessage("native-root", "done from OpenCode")}
	backend, err := New(bridge)
	if err != nil {
		t.Fatal(err)
	}

	result, err := backend.Run(context.Background(), Request{
		AgentID: "agent-1", AgentDockSessionID: "session-1", Workspace: `C:\work\project`,
		PermissionMode: "project", Provider: "opencode", Model: "mimo-v2.5-free",
		NativeAgent: "build", Task: "Fix the failing tests autonomously.",
		Constraints: []string{"Do not change the public API.", "Run tests before finishing."},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bridge.starts) != 1 {
		t.Fatalf("Start calls=%d want=1", len(bridge.starts))
	}
	start := bridge.starts[0]
	if start.AgentDockSessionID != "session-1" || start.ClientRequestID != "agent-1" || start.Workspace != `C:\work\project` {
		t.Fatalf("Start=%+v", start)
	}
	if start.Provider != "opencode" || start.Model != "mimo-v2.5-free" || start.ExecutionMode != ExecutionModeAutonomous {
		t.Fatalf("Start provider/model/mode=%+v", start)
	}
	if len(bridge.prompts) != 1 {
		t.Fatalf("prompts=%d want=1", len(bridge.prompts))
	}
	prompt := bridge.prompts[0]
	if prompt.Model == nil || prompt.Model.ProviderID != "opencode" || prompt.Model.ModelID != "mimo-v2.5-free" {
		t.Fatalf("prompt model=%+v", prompt.Model)
	}
	if prompt.Agent != "build" {
		t.Fatalf("prompt agent=%q", prompt.Agent)
	}
	if len(prompt.Parts) != 1 || !strings.Contains(prompt.Parts[0].Text, "Fix the failing tests autonomously.") || !strings.Contains(prompt.Parts[0].Text, "Do not change the public API.") {
		t.Fatalf("prompt parts=%+v", prompt.Parts)
	}
	if strings.Contains(strings.ToLower(prompt.Parts[0].Text), `"steps"`) {
		t.Fatalf("high-level prompt leaked precomputed steps: %q", prompt.Parts[0].Text)
	}
	if !strings.Contains(prompt.System, `C:\work\project`) || !strings.Contains(prompt.System, "project") {
		t.Fatalf("system context missing workspace/permission: %q", prompt.System)
	}
	if result.RootOpenCodeSessionID != "native-root" || result.SubagentID != "subagent-d3c" || result.FinalText != "done from OpenCode" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRunProjectsOwnedRootAndDescendantToolTelemetry(t *testing.T) {
	observed := make(chan struct{})
	bridge := &fakeNativeBridge{}
	bridge.observe = func(ctx context.Context, fn func(opencodenative.NativeEvent, int64) error) error {
		events := []opencodenative.NativeEvent{
			toolEvent("native-root", "call-read", "read", "completed"),
			toolEvent("native-child", "call-test", "bash", "completed"),
			toolEvent("native-child", "call-test", "bash", "completed"),
		}
		for i, event := range events {
			if err := fn(event, int64(i+1)); err != nil {
				return err
			}
		}
		close(observed)
		<-ctx.Done()
		return ctx.Err()
	}
	bridge.submit = func(ctx context.Context, _ opencodenative.PromptRequest) (opencodenative.Message, error) {
		select {
		case <-observed:
		case <-time.After(time.Second):
			t.Fatal("event observer did not start")
		}
		return textMessage("native-root", "telemetry complete"), nil
	}
	backend, _ := New(bridge)
	var emitted []TelemetryEvent
	result, err := backend.Run(context.Background(), Request{
		AgentID: "agent-telemetry", AgentDockSessionID: "session-telemetry", Workspace: `C:\work`,
		PermissionMode: "project", Provider: "opencode", Model: "mimo-v2.5-free", Task: "Inspect and test the project.",
	}, func(event TelemetryEvent) error { emitted = append(emitted, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.AutonomousToolSteps != 2 {
		t.Fatalf("tool steps=%d want=2 telemetry=%+v", result.AutonomousToolSteps, result.Telemetry)
	}
	if len(emitted) != 2 || emitted[0].Tool != "read" || emitted[0].Descendant || emitted[1].Tool != "bash" || !emitted[1].Descendant {
		t.Fatalf("emitted=%+v", emitted)
	}
}

func TestRunCancellationAbortsExactNativeRoot(t *testing.T) {
	bridge := &fakeNativeBridge{}
	bridge.submit = func(ctx context.Context, _ opencodenative.PromptRequest) (opencodenative.Message, error) {
		<-ctx.Done()
		return opencodenative.Message{}, ctx.Err()
	}
	backend, _ := New(bridge)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := backend.Run(ctx, Request{
		AgentID: "agent-cancel", AgentDockSessionID: "session-cancel", Workspace: `C:\work`,
		PermissionMode: "project", Provider: "opencode", Model: "mimo-v2.5-free", Task: "Long task.",
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	bridge.mu.Lock()
	aborts := bridge.aborts
	bridge.mu.Unlock()
	if aborts != 1 {
		t.Fatalf("aborts=%d want=1", aborts)
	}
}

func TestRunRejectsEmptyHighLevelTask(t *testing.T) {
	backend, _ := New(&fakeNativeBridge{})
	_, err := backend.Run(context.Background(), Request{AgentID: "a", AgentDockSessionID: "s", Workspace: `C:\work`}, nil)
	if err == nil {
		t.Fatal("empty task accepted")
	}
}

func TestRunTimeoutAbortsExactNativeRoot(t *testing.T) {
	bridge := &fakeNativeBridge{}
	bridge.submit = func(ctx context.Context, _ opencodenative.PromptRequest) (opencodenative.Message, error) {
		<-ctx.Done()
		return opencodenative.Message{}, ctx.Err()
	}
	backend, _ := New(bridge)
	_, err := backend.Run(context.Background(), Request{
		AgentID: "agent-timeout", AgentDockSessionID: "session-timeout", Workspace: `C:\work`,
		PermissionMode: "project", Provider: "opencode", Model: "mimo-v2.5-free", Task: "Long task.", Timeout: 20 * time.Millisecond,
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want context.DeadlineExceeded", err)
	}
	bridge.mu.Lock()
	aborts := bridge.aborts
	bridge.mu.Unlock()
	if aborts != 1 {
		t.Fatalf("aborts=%d want=1", aborts)
	}
}

func TestRunUsesAuthoritativeBindingPermissionContext(t *testing.T) {
	bridge := &fakeNativeBridge{
		binding: session.NativeSessionBinding{PermissionMode: "project"},
		message: textMessage("native-root", "done"),
	}
	backend, err := New(bridge)
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Run(context.Background(), Request{
		AgentID: "agent-permission", AgentDockSessionID: "session-permission", Workspace: `C:\work`,
		PermissionMode: "full", Provider: "opencode", Model: "mimo-v2.5-free", Task: "Inspect the project.",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if len(bridge.prompts) != 1 {
		t.Fatalf("prompts=%d want=1", len(bridge.prompts))
	}
	system := bridge.prompts[0].System
	if !strings.Contains(system, `Permission mode is "project"`) || strings.Contains(system, `Permission mode is "full"`) {
		t.Fatalf("system permission context=%q want authoritative binding permission project", system)
	}
}

func TestRunPropagatesFallbackTelemetrySinkError(t *testing.T) {
	sinkErr := errors.New("sink failed")
	toolPart, err := json.Marshal(map[string]any{
		"type": "tool", "tool": "read", "callID": "call-fallback",
		"state": map[string]any{"status": "completed", "input": map[string]any{"filePath": "README.md"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge := &fakeNativeBridge{
		message: opencodenative.Message{Info: opencodenative.MessageInfo{SessionID: "native-root"}, Parts: []json.RawMessage{toolPart}},
		observe: func(ctx context.Context, _ func(opencodenative.NativeEvent, int64) error) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	backend, err := New(bridge)
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Run(context.Background(), Request{
		AgentID: "agent-sink", AgentDockSessionID: "session-sink", Workspace: `C:\work`,
		PermissionMode: "project", Provider: "opencode", Model: "mimo-v2.5-free", Task: "Inspect README.",
	}, func(TelemetryEvent) error { return sinkErr })
	if !errors.Is(err, sinkErr) {
		t.Fatalf("err=%v want sink error", err)
	}
}
