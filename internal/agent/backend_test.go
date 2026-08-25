package agent

import (
	"context"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

type capturingProviderAdapter struct {
	request ProviderRequest
}

func (p *capturingProviderAdapter) Run(_ context.Context, request ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
	p.request = request
	if err := emit(ProviderEvent{Kind: ProviderEventText, Text: "hello"}); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{Summary: "done", ResolvedModel: request.Model, StopReason: "stop", Usage: Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5, Known: true}}, nil
}

func (p *capturingProviderAdapter) ResolveModel(raw string) (string, error) { return raw, nil }

func TestProviderBackendMapsAgentExecutionContract(t *testing.T) {
	provider := &capturingProviderAdapter{}
	backend := NewProviderBackend(provider)
	secret := credential.Secret("secret")
	var events []BackendEvent

	result, err := backend.Run(context.Background(), BackendRequest{
		AgentID: "agent_1", SessionID: "session_1", Workspace: `C:\repo`, RuntimeScope: "runtime_1",
		PermissionMode: "project", Provider: "fake", Model: "model_1", Role: "implementation", Prompt: "prompt",
		Credential: secret, Budget: AgentBudget{MaxRequests: 7}, Timeout: 5 * time.Second,
	}, func(event BackendEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.request.AgentID != "agent_1" || provider.request.SessionID != "session_1" || provider.request.Model != "model_1" {
		t.Fatalf("provider request mapping mismatch: %+v", provider.request)
	}
	if provider.request.MaxTurns != 7 || string(provider.request.Credential) != "secret" {
		t.Fatalf("provider budget/credential mapping mismatch: %+v", provider.request)
	}
	if result.Summary != "done" || result.ResolvedModel != "model_1" || result.Usage.TotalTokens != 5 {
		t.Fatalf("backend result mapping mismatch: %+v", result)
	}
	if len(events) != 1 || events[0].Text != "hello" {
		t.Fatalf("backend events mismatch: %+v", events)
	}
}
