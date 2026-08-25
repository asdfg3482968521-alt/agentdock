package agent

import (
	"context"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

// BackendRequest is the execution-only contract between Agent Manager and a
// backend. Lifecycle, terminal state, persistence, ownership, and completion
// publication remain Manager responsibilities.
type BackendRequest struct {
	AgentID         string
	SessionID       string
	Workspace       string
	RuntimeScope    string
	PermissionMode  string
	Provider        string
	Model           string
	Role            string
	Prompt          string
	Messages        []ProviderMessage
	Tools           []ProviderTool
	Credential      credential.Secret
	Budget          AgentBudget
	Timeout         time.Duration
	MaxOutputTokens int
}

// BackendEvent and BackendResult deliberately reuse the existing provider
// event/result vocabulary so the provider path remains behavior-equivalent.
type BackendEvent = ProviderEvent
type BackendResult = ProviderResult

// AgentBackend owns only execution. Agent Manager owns the durable Agent state
// machine and all terminal completion ordering.
type AgentBackend interface {
	ResolveModel(string) (string, error)
	Run(context.Context, BackendRequest, func(BackendEvent) error) (BackendResult, error)
}

// ProviderBackend adapts the existing ProviderAdapter contract to AgentBackend
// without changing provider request or result semantics.
type ProviderBackend struct {
	provider ProviderAdapter
}

func NewProviderBackend(provider ProviderAdapter) *ProviderBackend {
	return &ProviderBackend{provider: provider}
}

func (b *ProviderBackend) ResolveModel(raw string) (string, error) {
	return resolveProviderModel(b.provider, raw)
}

func (b *ProviderBackend) Run(ctx context.Context, request BackendRequest, emit func(BackendEvent) error) (BackendResult, error) {
	return b.provider.Run(ctx, ProviderRequest{
		AgentID:         request.AgentID,
		SessionID:       request.SessionID,
		Model:           request.Model,
		Role:            request.Role,
		Prompt:          request.Prompt,
		Messages:        request.Messages,
		Tools:           request.Tools,
		Credential:      request.Credential,
		MaxTurns:        request.Budget.MaxRequests,
		MaxOutputTokens: request.MaxOutputTokens,
	}, emit)
}
