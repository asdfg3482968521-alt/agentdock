package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/opencodeautonomous"
)

const (
	d3NativeAutonomousProvider     = "opencode-native"
	d3NativeAutonomousDefaultModel = "mimo-v2.5-free"
)

type d3NativeAutonomousRunner interface {
	Run(context.Context, opencodeautonomous.Request, func(opencodeautonomous.TelemetryEvent) error) (opencodeautonomous.Result, error)
}

type openCodeNativeAutonomousAgentBackend struct {
	runner d3NativeAutonomousRunner
}

func newOpenCodeNativeAutonomousAgentBackend(runner d3NativeAutonomousRunner) agentruntime.AgentBackend {
	return &openCodeNativeAutonomousAgentBackend{runner: runner}
}

func (b *openCodeNativeAutonomousAgentBackend) BackendIdentity() agentruntime.BackendIdentity {
	return agentruntime.BackendIdentityOpenCode
}

func (b *openCodeNativeAutonomousAgentBackend) RequiresCredential() bool { return false }

type d3NativeModelRoute struct {
	Provider string
	Model    string
	Variant  string
}

func parseD3NativeModelRoute(raw string) (d3NativeModelRoute, error) {
	value := strings.TrimSpace(raw)
	if value == "" || value == "default" {
		value = d3NativeAutonomousDefaultModel
	}
	route := d3NativeModelRoute{Provider: "opencode"}
	if before, after, ok := strings.Cut(value, "#"); ok {
		value = strings.TrimSpace(before)
		route.Variant = strings.TrimSpace(after)
		if route.Variant == "" {
			return d3NativeModelRoute{}, fmt.Errorf("native model variant is empty")
		}
	}
	if provider, model, ok := strings.Cut(value, "/"); ok {
		route.Provider = strings.TrimSpace(provider)
		route.Model = strings.TrimSpace(model)
	} else {
		route.Model = strings.TrimSpace(value)
	}
	if route.Provider == "" || route.Model == "" {
		return d3NativeModelRoute{}, fmt.Errorf("native model must be model or provider/model with optional #variant")
	}
	return route, nil
}

func canonicalD3NativeModel(route d3NativeModelRoute) string {
	value := route.Model
	if route.Provider != "opencode" {
		value = route.Provider + "/" + value
	}
	if route.Variant != "" {
		value += "#" + route.Variant
	}
	return value
}

func (b *openCodeNativeAutonomousAgentBackend) ResolveModel(raw string) (string, error) {
	route, err := parseD3NativeModelRoute(raw)
	if err != nil {
		return "", err
	}
	return canonicalD3NativeModel(route), nil
}

func (b *openCodeNativeAutonomousAgentBackend) Run(ctx context.Context, request agentruntime.BackendRequest, emit func(agentruntime.BackendEvent) error) (agentruntime.BackendResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.runner == nil {
		return agentruntime.BackendResult{}, &agentruntime.BackendError{
			Code:      agentruntime.BackendErrorUnavailable,
			Retryable: true,
			Err:       errors.New("OpenCode native autonomous backend is unavailable"),
		}
	}

	route, routeErr := parseD3NativeModelRoute(request.Model)
	if routeErr != nil {
		return agentruntime.BackendResult{}, &agentruntime.BackendError{
			Code: agentruntime.BackendErrorModelUnavailable,
			Err:  routeErr,
		}
	}

	result, err := b.runner.Run(ctx, opencodeautonomous.Request{
		AgentID:            request.AgentID,
		AgentDockSessionID: request.SessionID,
		Workspace:          request.Workspace,
		PermissionMode:     request.PermissionMode,
		Provider:           route.Provider,
		Model:              route.Model,
		Variant:            route.Variant,
		Task:               request.Prompt,
		Timeout:            request.Timeout,
	}, func(event opencodeautonomous.TelemetryEvent) error {
		if emit == nil {
			return nil
		}
		payload, marshalErr := json.Marshal(struct {
			EventID    string          `json:"event_id,omitempty"`
			Cursor     int64           `json:"cursor"`
			SessionID  string          `json:"session_id,omitempty"`
			Tool       string          `json:"tool,omitempty"`
			CallID     string          `json:"call_id,omitempty"`
			Status     string          `json:"status,omitempty"`
			Input      json.RawMessage `json:"input,omitempty"`
			Output     json.RawMessage `json:"output,omitempty"`
			Descendant bool            `json:"descendant"`
		}{
			EventID: event.EventID, Cursor: event.Cursor, SessionID: event.SessionID,
			Tool: event.Tool, CallID: event.CallID, Status: event.Status,
			Input: event.Input, Output: event.Output, Descendant: event.Descendant,
		})
		if marshalErr != nil {
			return marshalErr
		}
		return emit(agentruntime.BackendEvent{Kind: agentruntime.ProviderEventText, Text: "[opencode-native] " + string(payload)})
	})
	if err != nil {
		return agentruntime.BackendResult{}, err
	}
	if strings.TrimSpace(result.SubagentID) != strings.TrimSpace(request.AgentID) {
		return agentruntime.BackendResult{}, &agentruntime.BackendError{
			Code: agentruntime.BackendErrorTool,
			Err:  fmt.Errorf("native autonomous subagent identity mismatch: got %q want %q", result.SubagentID, request.AgentID),
		}
	}
	if strings.TrimSpace(result.RootOpenCodeSessionID) == "" {
		return agentruntime.BackendResult{}, &agentruntime.BackendError{
			Code: agentruntime.BackendErrorTool,
			Err:  errors.New("native autonomous backend returned empty root OpenCode session id"),
		}
	}
	resolvedModel := strings.TrimSpace(request.Model)
	if resolvedModel == "" {
		resolvedModel = canonicalD3NativeModel(d3NativeModelRoute{Provider: result.Provider, Model: result.Model, Variant: result.Variant})
	}
	return agentruntime.BackendResult{
		Summary:       result.FinalText,
		ResolvedModel: resolvedModel,
		StopReason:    "stop",
	}, nil
}

func addD3NativeAutonomousAgentBackend(backends map[string]agentruntime.AgentBackend, runner d3NativeAutonomousRunner) {
	if backends == nil || runner == nil {
		return
	}
	backends[d3NativeAutonomousProvider] = newOpenCodeNativeAutonomousAgentBackend(runner)
}

func d3G1AgentLimits(nativeEnabled bool) agentruntime.AgentLimits {
	limits := agentruntime.DefaultAgentLimits()
	if nativeEnabled && limits.MaxConcurrentPerProvider < 3 {
		limits.MaxConcurrentPerProvider = 3
	}
	return limits
}
