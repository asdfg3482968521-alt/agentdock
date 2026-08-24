package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type secretCapturingToolBridge struct {
	calls      atomic.Int32
	seenSecret string
}

func (b *secretCapturingToolBridge) Tools(permission string) []ProviderTool {
	return []ProviderTool{{Name: "opencode_read"}}
}

func (b *secretCapturingToolBridge) Execute(ctx context.Context, request ToolExecutionRequest) (ToolExecutionResult, error) {
	b.calls.Add(1)
	if len(request.KnownSecrets) != 1 {
		return ToolExecutionResult{}, ErrToolError
	}
	b.seenSecret = string(request.KnownSecrets[0])
	return ToolExecutionResult{JobID: "job-secret", Text: "tool output"}, nil
}

func TestAgentManagerPassesResolvedCredentialOnlyAsEphemeralToolRedactionContext(t *testing.T) {
	var providerCalls atomic.Int32
	provider := ProviderAdapterFunc(func(ctx context.Context, req ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
		if providerCalls.Add(1) == 1 {
			if err := emit(ProviderEvent{Kind: ProviderEventTool, Tool: &ToolCall{ID: "call-secret", Name: "opencode_read", Arguments: []byte(`{}`)}}); err != nil {
				return ProviderResult{}, err
			}
			return ProviderResult{ResolvedModel: "fake-model", StopReason: "tool_use"}, nil
		}
		return ProviderResult{Summary: "done", ResolvedModel: "fake-model", StopReason: "stop"}, nil
	})
	bridge := &secretCapturingToolBridge{}
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
	if final.State != AgentStateSucceeded || bridge.calls.Load() != 1 {
		t.Fatalf("final=%+v calls=%d", final, bridge.calls.Load())
	}
	if bridge.seenSecret != "D2_TEST_SECRET_manager" {
		t.Fatalf("tool redaction context=%q", bridge.seenSecret)
	}
	bodyBytes, err := os.ReadFile(filepath.Join(manager.persistence.recordsDir, record.AgentID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if strings.Contains(body, bridge.seenSecret) {
		t.Fatal("resolved credential leaked into durable Agent record")
	}
}
