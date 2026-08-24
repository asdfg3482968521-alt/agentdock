package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/uvwt/agentdock/internal/credential"
)

type scriptedProvider struct {
	events []ProviderEvent
	result ProviderResult
}

func (provider scriptedProvider) Run(ctx context.Context, request ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
	for _, event := range provider.events {
		select {
		case <-ctx.Done():
			return ProviderResult{}, ctx.Err()
		default:
		}
		if err := emit(event); err != nil {
			return ProviderResult{}, err
		}
	}
	return provider.result, nil
}

func TestProviderAdapterStreamsEventsInOrder(t *testing.T) {
	provider := scriptedProvider{
		events: []ProviderEvent{
			{Kind: ProviderEventText, Text: "first"},
			{Kind: ProviderEventText, Text: "second"},
		},
		result: ProviderResult{ResolvedModel: "fake-model", Usage: Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
	}
	request := ProviderRequest{AgentID: "agent_test", SessionID: "session_test", Model: "fake-model", Prompt: "test", Credential: credential.Secret("D2_TEST_SECRET_provider_boundary")}
	var got []string
	result, err := provider.Run(context.Background(), request, func(event ProviderEvent) error {
		got = append(got, event.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("events = %v", got)
	}
	if result.ResolvedModel != "fake-model" || result.Usage.TotalTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
}

func TestProviderAdapterCancellationPreventsLaterEvents(t *testing.T) {
	provider := scriptedProvider{events: []ProviderEvent{
		{Kind: ProviderEventText, Text: "first"},
		{Kind: ProviderEventText, Text: "must-not-arrive"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	_, err := provider.Run(ctx, ProviderRequest{AgentID: "agent_test", SessionID: "session_test"}, func(event ProviderEvent) error {
		count++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if count != 1 {
		t.Fatalf("event count = %d, want 1", count)
	}
}

func TestProviderAdapterPropagatesEventSinkFailure(t *testing.T) {
	provider := scriptedProvider{events: []ProviderEvent{{Kind: ProviderEventText, Text: "first"}}}
	want := errors.New("sink stopped")
	_, err := provider.Run(context.Background(), ProviderRequest{}, func(ProviderEvent) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want sink error", err)
	}
}
