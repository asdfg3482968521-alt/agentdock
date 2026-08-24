package agent

import (
	"context"
	"encoding/json"

	"github.com/uvwt/agentdock/internal/credential"
)

const (
	ProviderEventText = "text"
	ProviderEventTool = "tool"
)

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ProviderRequest struct {
	AgentID    string
	SessionID  string
	Model      string
	Role       string
	Prompt     string
	Credential credential.Secret
	MaxTurns   int
}

type ProviderEvent struct {
	Kind string
	Text string
	Tool *ToolCall
}

type ProviderResult struct {
	Summary       string
	ResolvedModel string
	Usage         Usage
}

type ProviderAdapter interface {
	Run(context.Context, ProviderRequest, func(ProviderEvent) error) (ProviderResult, error)
}
