package opencodeautonomous

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/nativebridge"
	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

const ExecutionModeAutonomous = "native-autonomous"

const defaultAbortTimeout = 5 * time.Second

type NativeBridge interface {
	Start(context.Context, nativebridge.StartRequest) (session.NativeSessionBinding, error)
	SubmitMessage(context.Context, string, string, string, opencodenative.PromptRequest) (opencodenative.Message, error)
	ObserveEvents(context.Context, string, string, string, func(opencodenative.NativeEvent, int64) error) error
	Abort(context.Context, string, string, string) error
}

type Request struct {
	AgentID            string
	AgentDockSessionID string
	Workspace          string
	PermissionMode     string
	Provider           string
	Model              string
	NativeAgent        string
	Task               string
	Constraints        []string
	Timeout            time.Duration
}

type TelemetryEvent struct {
	EventID    string
	Cursor     int64
	SessionID  string
	Tool       string
	CallID     string
	Status     string
	Input      json.RawMessage
	Output     json.RawMessage
	Descendant bool
}

type Result struct {
	SubagentID            string
	RootOpenCodeSessionID string
	Provider              string
	Model                 string
	FinalText             string
	AutonomousToolSteps   int
	Telemetry             []TelemetryEvent
}

type OpenCodeAutonomousBackend struct {
	bridge       NativeBridge
	abortTimeout time.Duration
}

func New(bridge NativeBridge) (*OpenCodeAutonomousBackend, error) {
	if bridge == nil {
		return nil, errors.New("OpenCode autonomous backend requires native bridge")
	}
	return &OpenCodeAutonomousBackend{bridge: bridge, abortTimeout: defaultAbortTimeout}, nil
}

func (b *OpenCodeAutonomousBackend) Run(ctx context.Context, request Request, sink func(TelemetryEvent) error) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.AgentDockSessionID = strings.TrimSpace(request.AgentDockSessionID)
	request.Workspace = strings.TrimSpace(request.Workspace)
	request.PermissionMode = strings.TrimSpace(request.PermissionMode)
	request.Provider = strings.TrimSpace(request.Provider)
	request.Model = strings.TrimSpace(request.Model)
	request.NativeAgent = strings.TrimSpace(request.NativeAgent)
	request.Task = strings.TrimSpace(request.Task)
	if request.AgentID == "" || request.AgentDockSessionID == "" || request.Workspace == "" || request.Task == "" {
		return Result{}, errors.New("agent_id, agentdock_session_id, workspace, and high-level task are required")
	}
	if (request.Provider == "") != (request.Model == "") {
		return Result{}, errors.New("selected provider and model must be supplied together")
	}
	if request.NativeAgent == "" {
		request.NativeAgent = "build"
	}

	runCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	binding, err := b.bridge.Start(runCtx, nativebridge.StartRequest{
		AgentDockSessionID: request.AgentDockSessionID,
		ClientRequestID:    request.AgentID,
		Workspace:          request.Workspace,
		Provider:           request.Provider,
		Model:              request.Model,
		ExecutionMode:      ExecutionModeAutonomous,
	})
	if err != nil {
		return Result{}, fmt.Errorf("start exact OpenCode native root: %w", err)
	}
	if binding.OpenCodeSessionID == "" || binding.SubagentID == "" {
		return Result{}, errors.New("native bridge returned incomplete root binding")
	}

	prompt := opencodenative.TextPrompt(highLevelPrompt(request.Task, request.Constraints))
	prompt.Agent = request.NativeAgent
	permissionMode := strings.TrimSpace(binding.PermissionMode)
	if permissionMode == "" {
		permissionMode = request.PermissionMode
	}
	prompt.System = executionContextSystemPrompt(binding.Workspace, permissionMode)
	if request.Provider != "" {
		prompt.Model = &opencodenative.ModelRef{ProviderID: request.Provider, ModelID: request.Model}
	}

	observeCtx, stopObserve := context.WithCancel(runCtx)
	defer stopObserve()
	var telemetryMu sync.Mutex
	telemetry := make([]TelemetryEvent, 0, 8)
	completedCalls := map[string]struct{}{}
	observeDone := make(chan error, 1)
	go func() {
		observeDone <- b.bridge.ObserveEvents(observeCtx, binding.AgentDockSessionID, binding.SubagentID, binding.Workspace, func(event opencodenative.NativeEvent, cursor int64) error {
			item, ok := telemetryFromNativeEvent(event, cursor, binding.OpenCodeSessionID)
			if !ok {
				return nil
			}
			key := item.SessionID + "\x00" + item.CallID
			telemetryMu.Lock()
			if _, exists := completedCalls[key]; exists {
				telemetryMu.Unlock()
				return nil
			}
			completedCalls[key] = struct{}{}
			telemetry = append(telemetry, item)
			telemetryMu.Unlock()
			if sink != nil {
				return sink(item)
			}
			return nil
		})
	}()

	type submitResult struct {
		message opencodenative.Message
		err     error
	}
	submitDone := make(chan submitResult, 1)
	go func() {
		message, submitErr := b.bridge.SubmitMessage(runCtx, binding.AgentDockSessionID, binding.SubagentID, binding.Workspace, prompt)
		submitDone <- submitResult{message: message, err: submitErr}
	}()

	var submitted submitResult
	select {
	case <-runCtx.Done():
		b.abort(binding)
		stopObserve()
		return Result{}, runCtx.Err()
	case submitted = <-submitDone:
		if runCtx.Err() != nil {
			b.abort(binding)
			stopObserve()
			return Result{}, runCtx.Err()
		}
	}
	if submitted.err != nil {
		stopObserve()
		return Result{}, fmt.Errorf("submit autonomous native task: %w", submitted.err)
	}
	if submitted.message.Info.SessionID != "" && submitted.message.Info.SessionID != binding.OpenCodeSessionID {
		stopObserve()
		return Result{}, fmt.Errorf("native result session mismatch: got %q want root %q", submitted.message.Info.SessionID, binding.OpenCodeSessionID)
	}

	// The synchronous native message returns only after OpenCode finishes the turn.
	// Cancel SSE observation then wait for it to unwind so telemetry is stable.
	stopObserve()
	observeErr := <-observeDone
	if observeErr != nil && !errors.Is(observeErr, context.Canceled) && !errors.Is(observeErr, context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("observe autonomous native events: %w", observeErr)
	}

	// Root message parts are also a lossless fallback for root tool completions if
	// the SSE connection raced the first event. Descendant telemetry still comes
	// from D3-B's ownership-filtered ObserveEvents stream.
	telemetryMu.Lock()
	fallbackTelemetry := make([]TelemetryEvent, 0, len(submitted.message.Parts))
	for _, raw := range submitted.message.Parts {
		item, ok := telemetryFromMessagePart(raw, binding.OpenCodeSessionID, binding.OpenCodeSessionID)
		if !ok {
			continue
		}
		key := item.SessionID + "\x00" + item.CallID
		if _, exists := completedCalls[key]; exists {
			continue
		}
		completedCalls[key] = struct{}{}
		telemetry = append(telemetry, item)
		fallbackTelemetry = append(fallbackTelemetry, item)
	}
	stableTelemetry := append([]TelemetryEvent(nil), telemetry...)
	toolSteps := len(completedCalls)
	telemetryMu.Unlock()
	if sink != nil {
		for _, item := range fallbackTelemetry {
			if err := sink(item); err != nil {
				return Result{}, err
			}
		}
	}

	provider, model := request.Provider, request.Model
	if strings.TrimSpace(binding.Provider) != "" {
		provider = binding.Provider
	}
	if strings.TrimSpace(binding.Model) != "" {
		model = binding.Model
	}
	return Result{
		SubagentID:            binding.SubagentID,
		RootOpenCodeSessionID: binding.OpenCodeSessionID,
		Provider:              provider,
		Model:                 model,
		FinalText:             finalText(submitted.message),
		AutonomousToolSteps:   toolSteps,
		Telemetry:             stableTelemetry,
	}, nil
}

func (b *OpenCodeAutonomousBackend) abort(binding session.NativeSessionBinding) {
	ctx, cancel := context.WithTimeout(context.Background(), b.abortTimeout)
	defer cancel()
	_ = b.bridge.Abort(ctx, binding.AgentDockSessionID, binding.SubagentID, binding.Workspace)
}

func highLevelPrompt(task string, constraints []string) string {
	var builder strings.Builder
	builder.WriteString("Task:\n")
	builder.WriteString(strings.TrimSpace(task))
	clean := make([]string, 0, len(constraints))
	for _, constraint := range constraints {
		if value := strings.TrimSpace(constraint); value != "" {
			clean = append(clean, value)
		}
	}
	if len(clean) > 0 {
		builder.WriteString("\n\nConstraints:\n")
		for _, constraint := range clean {
			builder.WriteString("- ")
			builder.WriteString(constraint)
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("\nWork autonomously: analyze the task, choose and run the tools you need, inspect their results, adjust as needed, and return the final result.\n")
	return builder.String()
}

func executionContextSystemPrompt(workspace, permissionMode string) string {
	return fmt.Sprintf(
		"AgentDock owns the execution boundary. Use exact workspace \"%s\" as cwd. Permission mode is \"%s\". Workspace reads/writes are allowed only within the granted mode. Ordinary external paths are read-only unless an explicit bounded writable mount exists; protected, secret, and system paths must not be modified. Do not widen permissions or replace the AgentDock sandbox contract.",
		workspace, permissionMode,
	)
}

type nativeToolPart struct {
	Type   string `json:"type"`
	Tool   string `json:"tool"`
	CallID string `json:"callID"`
	State  struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	} `json:"state"`
}

type nativeEventProperties struct {
	SessionID string          `json:"sessionID"`
	Part      json.RawMessage `json:"part"`
}

func telemetryFromNativeEvent(event opencodenative.NativeEvent, cursor int64, rootSessionID string) (TelemetryEvent, bool) {
	if event.Type != "message.part.updated" {
		return TelemetryEvent{}, false
	}
	var properties nativeEventProperties
	if err := json.Unmarshal(event.Properties, &properties); err != nil {
		return TelemetryEvent{}, false
	}
	item, ok := telemetryFromMessagePart(properties.Part, properties.SessionID, rootSessionID)
	if !ok {
		return TelemetryEvent{}, false
	}
	item.EventID = event.ID
	item.Cursor = cursor
	return item, true
}

func telemetryFromMessagePart(raw json.RawMessage, sessionID, rootSessionID string) (TelemetryEvent, bool) {
	var part nativeToolPart
	if err := json.Unmarshal(raw, &part); err != nil || part.Type != "tool" || part.State.Status != "completed" || strings.TrimSpace(part.Tool) == "" {
		return TelemetryEvent{}, false
	}
	callID := strings.TrimSpace(part.CallID)
	if callID == "" {
		return TelemetryEvent{}, false
	}
	return TelemetryEvent{SessionID: sessionID, Tool: part.Tool, CallID: callID, Status: part.State.Status, Input: part.State.Input, Output: part.State.Output, Descendant: sessionID != rootSessionID}, true
}

func finalText(message opencodenative.Message) string {
	var values []string
	for _, raw := range message.Parts {
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &part); err == nil && part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			values = append(values, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(values, "\n")
}
