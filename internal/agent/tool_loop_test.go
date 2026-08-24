package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeToolBridge struct {
	calls atomic.Int32
	block chan struct{}
}

func (b *fakeToolBridge) Tools(permission string) []ProviderTool {
	return []ProviderTool{{Name: "opencode_read"}}
}

func (b *fakeToolBridge) Execute(ctx context.Context, request ToolExecutionRequest) (ToolExecutionResult, error) {
	b.calls.Add(1)
	if b.block != nil {
		select {
		case <-ctx.Done():
			return ToolExecutionResult{}, ctx.Err()
		case <-b.block:
		}
	}
	return ToolExecutionResult{JobID: "job-1", Text: "file contents"}, nil
}

func TestAgentManagerBoundedToolLoopReturnsToolResultToProvider(t *testing.T) {
	var providerCalls atomic.Int32
	provider := ProviderAdapterFunc(func(ctx context.Context, req ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
		call := providerCalls.Add(1)
		if call == 1 {
			if len(req.Tools) != 1 || req.Tools[0].Name != "opencode_read" {
				t.Fatalf("tools=%+v", req.Tools)
			}
			_ = emit(ProviderEvent{Kind: ProviderEventTool, Tool: &ToolCall{ID: "call-1", Name: "opencode_read", Arguments: []byte(`{"filePath":"README.md"}`)}})
			return ProviderResult{ResolvedModel: "fake-model", StopReason: "tool_use"}, nil
		}
		foundToolResult := false
		for _, message := range req.Messages {
			if message.Role == "tool" && message.ToolCallID == "call-1" && message.Text == "file contents" {
				foundToolResult = true
			}
		}
		if !foundToolResult {
			t.Fatalf("second request missing tool result: %+v", req.Messages)
		}
		_ = emit(ProviderEvent{Kind: ProviderEventText, Text: "finished"})
		return ProviderResult{Summary: "finished", ResolvedModel: "fake-model", StopReason: "stop"}, nil
	})
	bridge := &fakeToolBridge{}
	manager := testAgentManagerWithTools(t, provider, bridge, DefaultAgentLimits())
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "inspect", MaxRequests: 2})
	if err != nil {
		t.Fatal(err)
	}
	final, err := manager.Wait(context.Background(), record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != AgentStateSucceeded || providerCalls.Load() != 2 || bridge.calls.Load() != 1 {
		t.Fatalf("final=%+v providerCalls=%d toolCalls=%d", final, providerCalls.Load(), bridge.calls.Load())
	}
}

func TestAgentManagerBudgetBlocksProviderRequestAfterTool(t *testing.T) {
	var providerCalls atomic.Int32
	provider := ProviderAdapterFunc(func(ctx context.Context, req ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
		providerCalls.Add(1)
		_ = emit(ProviderEvent{Kind: ProviderEventTool, Tool: &ToolCall{ID: "call-1", Name: "opencode_read", Arguments: []byte(`{}`)}})
		return ProviderResult{ResolvedModel: "fake-model", StopReason: "tool_use"}, nil
	})
	manager := testAgentManagerWithTools(t, provider, &fakeToolBridge{}, DefaultAgentLimits())
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "inspect", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	final, err := manager.Wait(context.Background(), record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != AgentStateFailed || final.Error != ErrBudgetExceeded.Error() {
		t.Fatalf("final=%+v", final)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls=%d want 1", providerCalls.Load())
	}
}

func TestAgentCancelStopsBlockedToolAndPreventsFutureProviderCalls(t *testing.T) {
	var providerCalls atomic.Int32
	provider := ProviderAdapterFunc(func(ctx context.Context, req ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
		providerCalls.Add(1)
		_ = emit(ProviderEvent{Kind: ProviderEventTool, Tool: &ToolCall{ID: "call-1", Name: "opencode_read", Arguments: []byte(`{}`)}})
		return ProviderResult{ResolvedModel: "fake-model", StopReason: "tool_use"}, nil
	})
	bridge := &fakeToolBridge{block: make(chan struct{})}
	manager := testAgentManagerWithTools(t, provider, bridge, DefaultAgentLimits())
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "inspect", MaxRequests: 3})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for bridge.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bridge.calls.Load() == 0 {
		t.Fatal("tool did not start")
	}
	cancelled, err := manager.Cancel(context.Background(), "session_a", record.AgentID, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != AgentStateCancelled {
		t.Fatalf("state=%s", cancelled.State)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls=%d want 1", providerCalls.Load())
	}
}

func testAgentManagerWithTools(t *testing.T, provider ProviderAdapter, tools ToolBridge, limits AgentLimits) *Manager {
	t.Helper()
	manager := testAgentManager(t, provider, limits)
	manager.options.Tools = tools
	return manager
}

var _ = errors.Is
