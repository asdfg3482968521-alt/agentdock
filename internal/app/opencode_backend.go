package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
	"github.com/uvwt/agentdock/internal/opencodeworker"
)

const openCodeBackendModel = "opencode-native"

type openCodeWorker interface {
	Execute(context.Context, opencodeworker.Request, opencodeworker.EventSink) (opencodeworker.Result, error)
}

type openCodeRuntimeBinder func(sessionID, runtimeScope string) error

type openCodeBackend struct {
	worker      openCodeWorker
	bindRuntime openCodeRuntimeBinder
}

type openCodeTaskEnvelope struct {
	Instruction string                `json:"instruction"`
	Steps       []opencodeworker.Step `json:"steps"`
}

func newOpenCodeBackend(worker openCodeWorker, bindRuntime ...openCodeRuntimeBinder) *openCodeBackend {
	backend := &openCodeBackend{worker: worker}
	if len(bindRuntime) > 0 {
		backend.bindRuntime = bindRuntime[0]
	}
	return backend
}

func newOpenCodeAgentBackend(executor opencodeworker.Executor, bindRuntime ...openCodeRuntimeBinder) (agentruntime.AgentBackend, error) {
	engine, err := opencodeworker.New(executor, opencodeworker.Options{})
	if err != nil {
		return nil, err
	}
	return newOpenCodeBackend(engine, bindRuntime...), nil
}

func (b *openCodeBackend) BackendIdentity() agentruntime.BackendIdentity {
	return agentruntime.BackendIdentityOpenCode
}

func (b *openCodeBackend) RequiresCredential() bool { return false }

func (b *openCodeBackend) ResolveModel(raw string) (string, error) {
	model := strings.TrimSpace(raw)
	switch model {
	case "", "default", openCodeBackendModel:
		return openCodeBackendModel, nil
	default:
		return "", &agentruntime.BackendError{
			Code: agentruntime.BackendErrorModelUnavailable,
			Err:  fmt.Errorf("OpenCode backend is model-free; model %q is unsupported", model),
		}
	}
}

func (b *openCodeBackend) Run(ctx context.Context, request agentruntime.BackendRequest, emit func(agentruntime.BackendEvent) error) (agentruntime.BackendResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.worker == nil {
		return agentruntime.BackendResult{}, &agentruntime.BackendError{
			Code:      agentruntime.BackendErrorUnavailable,
			Retryable: true,
			Err:       errors.New("OpenCode worker is unavailable"),
		}
	}
	task, err := decodeOpenCodeTask(request.Prompt)
	if err != nil {
		return agentruntime.BackendResult{}, err
	}
	for index := range task.Steps {
		step := &task.Steps[index]
		step.Tool = strings.TrimSpace(step.Tool)
		if step.Arguments == nil {
			step.Arguments = map[string]any{}
		}
		if !isVerifiedOpenCodeTool(step.Tool) {
			return agentruntime.BackendResult{}, badOpenCodeTask(fmt.Errorf("tool %q is not part of the verified OpenCode surface", step.Tool))
		}
		if err := validateAgentToolArguments(step.Tool, step.Arguments); err != nil {
			return agentruntime.BackendResult{}, badOpenCodeTask(err)
		}
		if request.PermissionMode == "read-only" && agentToolMutates(step.Tool, step.Arguments) {
			return agentruntime.BackendResult{}, agentruntime.ErrAgentWriteDenied
		}
	}
	if request.PermissionMode != "read-only" && request.PermissionMode != "project" {
		return agentruntime.BackendResult{}, badOpenCodeTask(fmt.Errorf("invalid Agent permission %q", request.PermissionMode))
	}
	if b.bindRuntime != nil {
		if err := b.bindRuntime(strings.TrimSpace(request.SessionID), strings.TrimSpace(request.RuntimeScope)); err != nil {
			return agentruntime.BackendResult{}, &agentruntime.BackendError{
				Code:      agentruntime.BackendErrorUnavailable,
				Retryable: true,
				Err:       fmt.Errorf("bind OpenCode session runtime ownership: %w", err),
			}
		}
	}

	// Agent read-only is enforced above before execution. The verified OpenCode
	// runtime's project mode supplies the same workspace confinement used by the
	// existing Agent tool bridge without granting full-mode access.
	workerRequest := opencodeworker.Request{
		Scope:          opencodeworker.Scope{ID: request.AgentID, Workspace: request.Workspace},
		PermissionMode: opencoderuntime.PermissionProject,
		Instruction:    task.Instruction,
		Timeout:        request.Timeout,
		Steps:          task.Steps,
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var emitErr error
	result, runErr := b.worker.Execute(runCtx, workerRequest, func(event opencodeworker.Event) {
		if emit == nil || emitErr != nil {
			return
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			emitErr = err
			cancel()
			return
		}
		if err := emit(agentruntime.BackendEvent{Kind: agentruntime.ProviderEventText, Text: "[opencode] " + string(encoded)}); err != nil {
			emitErr = err
			cancel()
		}
	})
	if emitErr != nil {
		return agentruntime.BackendResult{}, emitErr
	}
	if runErr != nil {
		return agentruntime.BackendResult{}, normalizeOpenCodeBackendError(runErr)
	}
	if result.Completion != opencodeworker.CompletionSucceeded {
		return agentruntime.BackendResult{}, &agentruntime.BackendError{
			Code: agentruntime.BackendErrorTool,
			Err:  fmt.Errorf("OpenCode worker completed with unexpected state %q", result.Completion),
		}
	}
	return agentruntime.BackendResult{
		Summary:       fmt.Sprintf("OpenCode worker completed %d step(s)", len(task.Steps)),
		ResolvedModel: openCodeBackendModel,
		StopReason:    "stop",
	}, nil
}

func decodeOpenCodeTask(prompt string) (openCodeTaskEnvelope, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(prompt)))
	decoder.DisallowUnknownFields()
	var task openCodeTaskEnvelope
	if err := decoder.Decode(&task); err != nil {
		return openCodeTaskEnvelope{}, badOpenCodeTask(fmt.Errorf("decode bounded OpenCode task: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return openCodeTaskEnvelope{}, badOpenCodeTask(errors.New("bounded OpenCode task must contain exactly one JSON object"))
	}
	task.Instruction = strings.TrimSpace(task.Instruction)
	if task.Instruction == "" || len(task.Steps) == 0 {
		return openCodeTaskEnvelope{}, badOpenCodeTask(errors.New("bounded OpenCode task requires instruction and at least one step"))
	}
	return task, nil
}

func badOpenCodeTask(err error) error {
	return &agentruntime.BackendError{Code: agentruntime.BackendErrorBadRequest, Err: err}
}

func normalizeOpenCodeBackendError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	workerCode := opencodeworker.ErrorCode(err)
	providerCode := workerCode
	retryable := false
	var runtimeErr *opencoderuntime.Error
	if errors.As(err, &runtimeErr) {
		providerCode = runtimeErr.Code
		retryable = runtimeErr.Retryable
	}
	code := agentruntime.BackendErrorTool
	switch workerCode {
	case "OPENCODE_WORKER_SCOPE_REQUIRED", "OPENCODE_WORKER_INSTRUCTION_REQUIRED", "OPENCODE_WORKER_PERMISSION_INVALID",
		"OPENCODE_WORKER_STEPS_REQUIRED", "OPENCODE_WORKER_STEP_LIMIT", "OPENCODE_WORKER_STEP_INVALID",
		"OPENCODE_WORKER_WORKSPACE_REQUIRED", "OPENCODE_WORKER_WORKSPACE_INVALID":
		code = agentruntime.BackendErrorBadRequest
	case "OPENCODE_WORKER_RUNTIME_FAILED":
		code = agentruntime.BackendErrorUnavailable
		retryable = true
	case "OPENCODE_WORKER_TOOL_FAILED":
		code = agentruntime.BackendErrorTool
	}
	var providerCodePtr *string
	if strings.TrimSpace(providerCode) != "" {
		value := providerCode
		providerCodePtr = &value
	}
	return &agentruntime.BackendError{Code: code, Retryable: retryable, ProviderCode: providerCodePtr, Err: err}
}
