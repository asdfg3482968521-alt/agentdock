package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/uvwt/agentdock/internal/nativebridge"
	"github.com/uvwt/agentdock/internal/opencodenative"
)

type d3Supervisor interface {
	SubmitSupervisorMessage(context.Context, nativebridge.SupervisorMessageRequest) (nativebridge.SupervisorMessageResult, error)
	SubmitSupervisorCorrection(context.Context, nativebridge.SupervisorMessageRequest, nativebridge.CorrectionMode) (nativebridge.OrderedCorrectionResult, error)
	ObserveSupervisor(context.Context, string, string, string, int64, int) (nativebridge.SupervisorObservation, error)
}

func (r *Runtime) d3SupervisorMessage(ctx context.Context, args map[string]any) (Result, error) {
	if r.nativeSupervisor == nil {
		return nil, toolError("NATIVE_BRIDGE_UNAVAILABLE", "D3 native supervisor is unavailable", "native_supervisor")
	}
	sessionID, err := d3SupervisorRequiredString(args, "session_id")
	if err != nil {
		return nil, err
	}
	subagentID, err := d3SupervisorRequiredString(args, "subagent_id")
	if err != nil {
		return nil, err
	}
	clientMessageID, err := d3SupervisorRequiredString(args, "client_message_id")
	if err != nil {
		return nil, err
	}
	kind, err := d3SupervisorRequiredString(args, "kind")
	if err != nil {
		return nil, err
	}
	kind = strings.ToUpper(kind)
	if kind != "TASK" && kind != "FOLLOWUP" {
		return nil, d3SupervisorInvalid("kind must be TASK or FOLLOWUP", "kind")
	}
	text, err := d3SupervisorRequiredString(args, "text")
	if err != nil {
		return nil, err
	}
	record, err := r.sessions.Resolve(sessionID, "")
	if err != nil {
		return nil, sessionToolError(err)
	}
	prompt, err := r.d3SupervisorPrompt(record.SessionID, subagentID, text)
	if err != nil {
		return nil, err
	}
	result, err := r.nativeSupervisor.SubmitSupervisorMessage(ctx, nativebridge.SupervisorMessageRequest{
		AgentDockSessionID: record.SessionID,
		SubagentID:         subagentID,
		Workspace:          record.Workspace(),
		ClientMessageID:    clientMessageID,
		Kind:               kind,
		Prompt:             prompt,
	})
	if err != nil {
		return nil, d3SupervisorToolError(err)
	}
	return d3SupervisorResult(result)
}

func (r *Runtime) d3SupervisorCorrect(ctx context.Context, args map[string]any) (Result, error) {
	if r.nativeSupervisor == nil {
		return nil, toolError("NATIVE_BRIDGE_UNAVAILABLE", "D3 native supervisor is unavailable", "native_supervisor")
	}
	sessionID, err := d3SupervisorRequiredString(args, "session_id")
	if err != nil {
		return nil, err
	}
	subagentID, err := d3SupervisorRequiredString(args, "subagent_id")
	if err != nil {
		return nil, err
	}
	clientMessageID, err := d3SupervisorRequiredString(args, "client_message_id")
	if err != nil {
		return nil, err
	}
	modeValue, err := d3SupervisorRequiredString(args, "mode")
	if err != nil {
		return nil, err
	}
	mode := nativebridge.CorrectionMode(strings.ToLower(modeValue))
	if mode != nativebridge.CorrectionGraceful && mode != nativebridge.CorrectionForce {
		return nil, d3SupervisorInvalid("mode must be graceful or force", "mode")
	}
	text, err := d3SupervisorRequiredString(args, "text")
	if err != nil {
		return nil, err
	}
	record, err := r.sessions.Resolve(sessionID, "")
	if err != nil {
		return nil, sessionToolError(err)
	}
	prompt, err := r.d3SupervisorPrompt(record.SessionID, subagentID, text)
	if err != nil {
		return nil, err
	}
	result, err := r.nativeSupervisor.SubmitSupervisorCorrection(ctx, nativebridge.SupervisorMessageRequest{
		AgentDockSessionID: record.SessionID,
		SubagentID:         subagentID,
		Workspace:          record.Workspace(),
		ClientMessageID:    clientMessageID,
		Kind:               "CORRECTION",
		Prompt:             prompt,
	}, mode)
	if err != nil {
		return nil, d3SupervisorToolError(err)
	}
	return d3SupervisorResult(result)
}

func (r *Runtime) d3SupervisorObserve(ctx context.Context, args map[string]any) (Result, error) {
	if r.nativeSupervisor == nil {
		return nil, toolError("NATIVE_BRIDGE_UNAVAILABLE", "D3 native supervisor is unavailable", "native_supervisor")
	}
	sessionID, err := d3SupervisorRequiredString(args, "session_id")
	if err != nil {
		return nil, err
	}
	subagentID, err := d3SupervisorRequiredString(args, "subagent_id")
	if err != nil {
		return nil, err
	}
	after, err := d3SupervisorInt64(args, "after_cursor", 0, 0, 1<<62)
	if err != nil {
		return nil, err
	}
	limit64, err := d3SupervisorInt64(args, "limit", 64, 1, 128)
	if err != nil {
		return nil, err
	}
	record, err := r.sessions.Resolve(sessionID, "")
	if err != nil {
		return nil, sessionToolError(err)
	}
	result, err := r.nativeSupervisor.ObserveSupervisor(ctx, record.SessionID, subagentID, record.Workspace(), after, int(limit64))
	if err != nil {
		return nil, d3SupervisorToolError(err)
	}
	return d3SupervisorResult(result)
}

func (r *Runtime) d3SupervisorPrompt(sessionID, subagentID, text string) (opencodenative.PromptRequest, error) {
	binding, err := r.sessions.NativeSessionBinding(sessionID, subagentID)
	if err != nil {
		return opencodenative.PromptRequest{}, sessionToolError(err)
	}
	prompt := opencodenative.TextPrompt(text)
	if strings.TrimSpace(binding.Provider) != "" && strings.TrimSpace(binding.Model) != "" {
		prompt.Model = &opencodenative.ModelRef{ProviderID: binding.Provider, ModelID: binding.Model}
	}
	return prompt, nil
}

func d3SupervisorRequiredString(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok {
		return "", d3SupervisorInvalid(key+" is required", key)
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", d3SupervisorInvalid(key+" must be a non-empty string", key)
	}
	return strings.TrimSpace(value), nil
}

func d3SupervisorInt64(args map[string]any, key string, fallback, minimum, maximum int64) (int64, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback, nil
	}
	var value int64
	switch typed := raw.(type) {
	case int:
		value = int64(typed)
	case int64:
		value = typed
	case float64:
		value = int64(typed)
		if float64(value) != typed {
			return 0, d3SupervisorInvalid(key+" must be an integer", key)
		}
	default:
		return 0, d3SupervisorInvalid(key+" must be an integer", key)
	}
	if value < minimum || value > maximum {
		return 0, d3SupervisorInvalid(fmt.Sprintf("%s must be between %d and %d", key, minimum, maximum), key)
	}
	return value, nil
}

func d3SupervisorInvalid(message, field string) error {
	return toolErrorDetails("INVALID_ARGUMENTS", message, "validation", map[string]any{"field": field})
}

func d3SupervisorToolError(err error) error {
	if err == nil {
		return nil
	}
	code := nativebridge.ErrorCode(err)
	if code == "" {
		return toolErrorCause("NATIVE_BRIDGE_FAILED", "D3 native supervisor request failed", "native_supervisor", nil, err)
	}
	message := err.Error()
	var bridgeErr *nativebridge.Error
	if errors.As(err, &bridgeErr) && strings.TrimSpace(bridgeErr.Message) != "" {
		message = bridgeErr.Message
	}
	return toolErrorCause(code, message, "native_supervisor", nil, err)
}

func d3SupervisorResult(value any) (Result, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode D3 supervisor result: %w", err)
	}
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode D3 supervisor result: %w", err)
	}
	return result, nil
}
