package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/opencodeautonomous"
)

type fakeD3G1AutonomousRunner struct {
	request   opencodeautonomous.Request
	telemetry []opencodeautonomous.TelemetryEvent
	result    opencodeautonomous.Result
	err       error
	run       func(context.Context, opencodeautonomous.Request, func(opencodeautonomous.TelemetryEvent) error) (opencodeautonomous.Result, error)
}

func (f *fakeD3G1AutonomousRunner) Run(ctx context.Context, request opencodeautonomous.Request, sink func(opencodeautonomous.TelemetryEvent) error) (opencodeautonomous.Result, error) {
	f.request = request
	if f.run != nil {
		return f.run(ctx, request, sink)
	}
	for _, event := range f.telemetry {
		if sink != nil {
			if err := sink(event); err != nil {
				return opencodeautonomous.Result{}, err
			}
		}
	}
	return f.result, f.err
}

func TestOpenCodeNativeAutonomousBackendMetadataAndModel(t *testing.T) {
	backend := newOpenCodeNativeAutonomousAgentBackend(&fakeD3G1AutonomousRunner{})
	metadata, ok := backend.(agentruntime.AgentBackendMetadata)
	if !ok {
		t.Fatal("native autonomous backend must expose AgentBackendMetadata")
	}
	if metadata.BackendIdentity() != agentruntime.BackendIdentityOpenCode || metadata.RequiresCredential() {
		t.Fatalf("metadata identity=%q requires_credential=%v", metadata.BackendIdentity(), metadata.RequiresCredential())
	}
	for raw, want := range map[string]string{"": d3NativeAutonomousDefaultModel, "default": d3NativeAutonomousDefaultModel, "custom-model": "custom-model", "jiyuan/deepseek-flash#low": "jiyuan/deepseek-flash#low"} {
		got, err := backend.ResolveModel(raw)
		if err != nil || got != want {
			t.Fatalf("ResolveModel(%q)=%q err=%v want=%q", raw, got, err, want)
		}
	}
}

func TestParseD3NativeModelRouteSplitsProviderModelAndVariant(t *testing.T) {
	route, err := parseD3NativeModelRoute("jiyuan/deepseek-flash#max")
	if err != nil {
		t.Fatal(err)
	}
	if route.Provider != "jiyuan" || route.Model != "deepseek-flash" || route.Variant != "max" {
		t.Fatalf("route=%+v", route)
	}
	if got := canonicalD3NativeModel(route); got != "jiyuan/deepseek-flash#max" {
		t.Fatalf("canonical=%q", got)
	}
}

func TestOpenCodeNativeAutonomousBackendMapsCompositeModelRoute(t *testing.T) {
	runner := &fakeD3G1AutonomousRunner{
		result: opencodeautonomous.Result{
			SubagentID: "agent-route", RootOpenCodeSessionID: "native-root-route",
			Provider: "jiyuan", Model: "deepseek-flash", Variant: "high", FinalText: "ok",
		},
	}
	backend := newOpenCodeNativeAutonomousAgentBackend(runner)
	result, err := backend.Run(context.Background(), agentruntime.BackendRequest{
		AgentID: "agent-route", SessionID: "session-route", Workspace: `C:\work\route`,
		PermissionMode: "project", Provider: d3NativeAutonomousProvider,
		Model: "jiyuan/deepseek-flash#high", Prompt: "route model correctly", Timeout: time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runner.request.Provider != "jiyuan" || runner.request.Model != "deepseek-flash" || runner.request.Variant != "high" {
		t.Fatalf("runner request=%+v", runner.request)
	}
	if result.ResolvedModel != "jiyuan/deepseek-flash#high" {
		t.Fatalf("resolved model=%q", result.ResolvedModel)
	}
}

func TestOpenCodeNativeAutonomousBackendMapsAgentRequestAndTelemetry(t *testing.T) {
	runner := &fakeD3G1AutonomousRunner{
		telemetry: []opencodeautonomous.TelemetryEvent{{EventID: "evt-1", Cursor: 3, SessionID: "native-root-a", Tool: "read", CallID: "call-1", Status: "completed"}},
		result: opencodeautonomous.Result{
			SubagentID: "agent-a", RootOpenCodeSessionID: "native-root-a", Provider: "opencode", Model: d3NativeAutonomousDefaultModel,
			FinalText: "D3 G1 autonomous result", AutonomousToolSteps: 1,
		},
	}
	backend := newOpenCodeNativeAutonomousAgentBackend(runner)
	var emitted []agentruntime.BackendEvent
	result, err := backend.Run(context.Background(), agentruntime.BackendRequest{
		AgentID: "agent-a", SessionID: "session-a", Workspace: `C:\work\A`, RuntimeScope: "session:session-a",
		PermissionMode: "project", Provider: d3NativeAutonomousProvider, Model: d3NativeAutonomousDefaultModel,
		Role: "implementation", Prompt: "Solve the high-level task autonomously.", Timeout: 45 * time.Second,
		Budget: agentruntime.AgentBudget{MaxRequests: 1},
	}, func(event agentruntime.BackendEvent) error {
		emitted = append(emitted, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runner.request.AgentID != "agent-a" || runner.request.AgentDockSessionID != "session-a" || runner.request.Workspace != `C:\work\A` {
		t.Fatalf("identity mapping=%+v", runner.request)
	}
	if runner.request.PermissionMode != "project" || runner.request.Provider != "opencode" || runner.request.Model != d3NativeAutonomousDefaultModel {
		t.Fatalf("authority/model mapping=%+v", runner.request)
	}
	if runner.request.Task != "Solve the high-level task autonomously." || runner.request.Timeout != 45*time.Second {
		t.Fatalf("task mapping=%+v", runner.request)
	}
	if result.Summary != "D3 G1 autonomous result" || result.ResolvedModel != d3NativeAutonomousDefaultModel || result.StopReason != "stop" {
		t.Fatalf("backend result=%+v", result)
	}
	if len(emitted) != 2 || emitted[0].Kind != agentruntime.ProviderEventText || emitted[0].Tool != nil || emitted[1].Kind != agentruntime.ProviderEventText || emitted[1].Tool != nil {
		t.Fatalf("telemetry and final text must be projected as text-only Agent output: %+v", emitted)
	}
	if !strings.Contains(emitted[0].Text, `"tool":"read"`) || !strings.Contains(emitted[0].Text, `"call_id":"call-1"`) {
		t.Fatalf("telemetry output=%q", emitted[0].Text)
	}
	if emitted[1].Text != "D3 G1 autonomous result" {
		t.Fatalf("final output=%q want autonomous result", emitted[1].Text)
	}
}

func TestOpenCodeNativeAutonomousBackendPropagatesCancellation(t *testing.T) {
	runner := &fakeD3G1AutonomousRunner{run: func(ctx context.Context, _ opencodeautonomous.Request, _ func(opencodeautonomous.TelemetryEvent) error) (opencodeautonomous.Result, error) {
		<-ctx.Done()
		return opencodeautonomous.Result{}, ctx.Err()
	}}
	backend := newOpenCodeNativeAutonomousAgentBackend(runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := backend.Run(ctx, agentruntime.BackendRequest{
		AgentID: "agent-cancel", SessionID: "session-cancel", Workspace: `C:\work`, PermissionMode: "project",
		Provider: d3NativeAutonomousProvider, Model: d3NativeAutonomousDefaultModel, Prompt: "long task", Timeout: time.Minute,
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestD3G1RuntimeBackendRegistrationAndConcurrency(t *testing.T) {
	runner := &fakeD3G1AutonomousRunner{}
	backends := map[string]agentruntime.AgentBackend{"opencode": newOpenCodeBackend(&fakeC25OpenCodeWorker{})}
	addD3NativeAutonomousAgentBackend(backends, runner)
	if backends["opencode"] == nil {
		t.Fatal("legacy opencode backend was replaced")
	}
	if backends[d3NativeAutonomousProvider] == nil {
		t.Fatal("native autonomous backend was not registered")
	}
	limits := d3G1AgentLimits(true)
	if limits.MaxConcurrent < 3 || limits.MaxConcurrentPerProvider < 3 || limits.MaxConcurrentPerSession != 1 {
		t.Fatalf("D3-G1 concurrency limits=%+v", limits)
	}
	baseline := d3G1AgentLimits(false)
	if baseline != agentruntime.DefaultAgentLimits() {
		t.Fatalf("non-native runtime limits changed: got=%+v want=%+v", baseline, agentruntime.DefaultAgentLimits())
	}
}
