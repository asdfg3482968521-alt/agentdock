package agent

import (
	"context"
	"encoding/json"
)

type ToolExecutionRequest struct {
	AgentID        string
	SessionID      string
	Workspace      string
	RuntimeScope   string
	PermissionMode string
	Tool           string
	Arguments      map[string]any
}

type ToolExecutionResult struct {
	JobID string
	Text  string
}

type ToolBridge interface {
	Tools(permission string) []ProviderTool
	Execute(context.Context, ToolExecutionRequest) (ToolExecutionResult, error)
}

func decodeToolArguments(raw json.RawMessage) (map[string]any, error) {
	var arguments map[string]any
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &arguments); err != nil || arguments == nil {
		return nil, ErrInvalidArguments
	}
	return arguments, nil
}
