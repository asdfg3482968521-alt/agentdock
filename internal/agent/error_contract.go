package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/uvwt/agentdock/internal/credential"
	"github.com/uvwt/agentdock/internal/redact"
)

func AgentErrorFrom(err error, secret []byte) ErrorEnvelope {
	if err == nil {
		return ErrorEnvelope{}
	}
	message := err.Error()
	if len(secret) > 0 {
		message = strings.ReplaceAll(message, string(secret), "[REDACTED]")
	}
	message = redact.Text(message)

	code := "TOOL_ERROR"
	retryable := false
	var providerCode *string
	var backendFailure *BackendError
	if errors.As(err, &backendFailure) {
		switch backendFailure.Code {
		case BackendErrorAuthFailure, BackendErrorRateLimited, BackendErrorBadRequest,
			BackendErrorUnavailable, BackendErrorTimeout, BackendErrorCancelled,
			BackendErrorMalformedResponse, BackendErrorModelUnavailable,
			BackendErrorBudgetExceeded, BackendErrorTool:
			code = backendFailure.Code
			retryable = backendFailure.Retryable
			providerCode = backendFailure.ProviderCode
		}
	}
	switch {
	case errors.Is(err, ErrAgentNotFound):
		code = "AGENT_NOT_FOUND"
	case errors.Is(err, ErrAgentSessionOwnershipMismatch):
		code = "AGENT_SESSION_OWNERSHIP_MISMATCH"
	case errors.Is(err, ErrAgentSessionClosed):
		code = "AGENT_SESSION_CLOSED"
	case errors.Is(err, ErrAgentWriteDenied):
		code = "AGENT_WRITE_DENIED"
	case errors.Is(err, ErrAgentNotTerminal):
		code = "AGENT_NOT_TERMINAL"
	case errors.Is(err, ErrInvalidAgentTransition):
		code = "INVALID_AGENT_TRANSITION"
	case errors.Is(err, ErrInvalidArguments):
		code = "INVALID_ARGUMENTS"
	case errors.Is(err, credential.ErrInvalidRef):
		code = "INVALID_CREDENTIAL_REF"
	case errors.Is(err, credential.ErrNotFound):
		code = "CREDENTIAL_NOT_FOUND"
	case errors.Is(err, ErrProviderUnsupported):
		code = "PROVIDER_UNSUPPORTED"
	case errors.Is(err, ErrModelUnavailable):
		code = "MODEL_UNAVAILABLE"
	case errors.Is(err, ErrBudgetExceeded):
		code = "BUDGET_EXCEEDED"
	case errors.Is(err, ErrResourceLimit):
		code, retryable = "RESOURCE_LIMIT", true
	case errors.Is(err, ErrIdempotencyConflict):
		code = "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, context.DeadlineExceeded):
		code, retryable = "TIMEOUT", true
	case errors.Is(err, context.Canceled):
		code = "CANCELLED"
	case errors.Is(err, ErrToolError):
		code = "TOOL_ERROR"
	default:
		if backendFailure != nil && code != "TOOL_ERROR" {
			break
		}
		switch providerErrorCode(err) {
		case ProviderErrorModelUnavailable:
			code = "MODEL_UNAVAILABLE"
		case ProviderErrorAuthFailure:
			code = "AUTH_FAILURE"
		case ProviderErrorRateLimited:
			code, retryable = "RATE_LIMITED", true
		case ProviderErrorBadRequest:
			code = "BAD_REQUEST"
		case ProviderErrorUnavailable:
			code, retryable = "PROVIDER_UNAVAILABLE", true
		case ProviderErrorTimeout:
			code, retryable = "TIMEOUT", true
		case ProviderErrorCancelled:
			code = "CANCELLED"
		case ProviderErrorMalformedResponse:
			code = "MALFORMED_RESPONSE"
		}
	}
	if strings.TrimSpace(message) == "" {
		message = strings.ToLower(strings.ReplaceAll(code, "_", " "))
	}
	return ErrorEnvelope{Code: code, Message: message, Retryable: retryable, ProviderCode: providerCode}
}
