package agent

import (
	"context"
	"errors"
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

type capturingAgentBackend struct {
	started chan BackendRequest
	run     func(context.Context, BackendRequest, func(BackendEvent) error) (BackendResult, error)
}

func (b *capturingAgentBackend) ResolveModel(raw string) (string, error) {
	if raw == "" || raw == "default" {
		return "backend-model", nil
	}
	return raw, nil
}

func (b *capturingAgentBackend) Run(ctx context.Context, request BackendRequest, emit func(BackendEvent) error) (BackendResult, error) {
	if b.started != nil {
		b.started <- request
	}
	if b.run != nil {
		return b.run(ctx, request, emit)
	}
	return BackendResult{Summary: "backend done", ResolvedModel: request.Model, StopReason: "stop"}, nil
}

func TestManagerDispatchesConfiguredAgentBackendWithoutLegacyProvider(t *testing.T) {
	root := t.TempDir()
	backend := &capturingAgentBackend{started: make(chan BackendRequest, 1)}
	manager, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: root, RuntimeScope: "scope-a", PermissionMode: "project"},
		}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("backend-secret")},
		Backends:    map[string]AgentBackend{"backend": backend},
		Limits:      DefaultAgentLimits(),
		IDFactory:   func() string { return "agent_backend_dispatch" },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "backend", Model: "default", CredentialRef: "cred_backend",
		Prompt: "do work", PermissionMode: "project", MaxRequests: 3, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case request := <-backend.started:
		if request.Workspace != root || request.RuntimeScope != "scope-a" || request.PermissionMode != "project" {
			t.Fatalf("scope mapping mismatch: %+v", request)
		}
		if request.Budget.MaxRequests != 3 || request.Timeout != 5*time.Second {
			t.Fatalf("budget/timeout mapping mismatch: %+v", request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not start")
	}
	finished, err := manager.Wait(context.Background(), record.AgentID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if finished.State != AgentStateSucceeded || finished.ResultSummary != "backend done" {
		t.Fatalf("finished=%+v", finished)
	}
}

func TestAgentErrorFromNormalizesBackendFailure(t *testing.T) {
	providerCode := "WORKER_OFFLINE"
	failure := &BackendError{Code: BackendErrorUnavailable, Retryable: true, ProviderCode: &providerCode, Err: errors.New("worker unavailable")}
	envelope := AgentErrorFrom(failure, nil)
	if envelope.Code != "PROVIDER_UNAVAILABLE" || !envelope.Retryable || envelope.Message != "worker unavailable" {
		t.Fatalf("envelope=%+v", envelope)
	}
	if envelope.ProviderCode == nil || *envelope.ProviderCode != providerCode {
		t.Fatalf("provider code=%v want %q", envelope.ProviderCode, providerCode)
	}
}

func newBackendTestManager(t *testing.T, backend AgentBackend, id string) *Manager {
	t.Helper()
	root := t.TempDir()
	manager, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: root, RuntimeScope: "scope-a", PermissionMode: "project"},
		}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("backend-secret")},
		Backends:    map[string]AgentBackend{"backend": backend},
		Limits:      DefaultAgentLimits(),
		IDFactory:   func() string { return id },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

func TestManagerBackendCancellationPublishesCancelledAfterBackendStops(t *testing.T) {
	backend := &capturingAgentBackend{started: make(chan BackendRequest, 1)}
	backend.run = func(ctx context.Context, _ BackendRequest, _ func(BackendEvent) error) (BackendResult, error) {
		<-ctx.Done()
		return BackendResult{}, ctx.Err()
	}
	manager := newBackendTestManager(t, backend, "agent_backend_cancel")
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "backend", Model: "default", CredentialRef: "cred_backend",
		Prompt: "cancel me", MaxRequests: 2, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not start")
	}
	finished, err := manager.Cancel(context.Background(), "session_a", record.AgentID, "stop")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if finished.State != AgentStateCancelled {
		t.Fatalf("state=%s want CANCELLED", finished.State)
	}
}

func TestManagerBackendTimeoutPublishesTimedOut(t *testing.T) {
	backend := &capturingAgentBackend{}
	backend.run = func(ctx context.Context, _ BackendRequest, _ func(BackendEvent) error) (BackendResult, error) {
		<-ctx.Done()
		return BackendResult{}, ctx.Err()
	}
	manager := newBackendTestManager(t, backend, "agent_backend_timeout")
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "backend", Model: "default", CredentialRef: "cred_backend",
		Prompt: "timeout", MaxRequests: 2, Timeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished, err := manager.Wait(context.Background(), record.AgentID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if finished.State != AgentStateTimedOut || finished.FailureReason == nil || finished.FailureReason.Code != "TIMEOUT" {
		t.Fatalf("finished=%+v", finished)
	}
}

func TestManagerBackendFailureUsesNormalizedEnvelope(t *testing.T) {
	backend := &capturingAgentBackend{}
	backend.run = func(context.Context, BackendRequest, func(BackendEvent) error) (BackendResult, error) {
		return BackendResult{}, &BackendError{Code: BackendErrorUnavailable, Retryable: true, Err: errors.New("worker unavailable")}
	}
	manager := newBackendTestManager(t, backend, "agent_backend_failure")
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "backend", Model: "default", CredentialRef: "cred_backend",
		Prompt: "fail", MaxRequests: 2, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished, err := manager.Wait(context.Background(), record.AgentID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if finished.State != AgentStateFailed || finished.FailureReason == nil {
		t.Fatalf("finished=%+v", finished)
	}
	if finished.FailureReason.Code != "PROVIDER_UNAVAILABLE" || !finished.FailureReason.Retryable {
		t.Fatalf("failure=%+v", finished.FailureReason)
	}
}

func TestManagerBackendWaitPreservesTerminalPersistenceOrdering(t *testing.T) {
	release := make(chan struct{})
	backend := &capturingAgentBackend{started: make(chan BackendRequest, 1)}
	backend.run = func(ctx context.Context, request BackendRequest, emit func(BackendEvent) error) (BackendResult, error) {
		select {
		case <-ctx.Done():
			return BackendResult{}, ctx.Err()
		case <-release:
			return BackendResult{Summary: "done", ResolvedModel: request.Model}, nil
		}
	}
	manager := newBackendTestManager(t, backend, "agent_backend_order")
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "backend", Model: "default", CredentialRef: "cred_backend",
		Prompt: "ordering", MaxRequests: 1, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not start")
	}
	gate := &terminalSaveGate{agentID: record.AgentID, entered: make(chan Record, 1), release: make(chan struct{})}
	manager.persistence.beforeSave = gate.hook
	waitDone := make(chan error, 1)
	go func() {
		_, err := manager.Wait(context.Background(), record.AgentID)
		waitDone <- err
	}()
	close(release)
	select {
	case saved := <-gate.entered:
		if saved.State != AgentStateSucceeded {
			t.Fatalf("state=%s want SUCCEEDED", saved.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal save not reached")
	}
	assertDoesNotReturn(t, waitDone, "backend Wait")
	close(gate.release)
	if err := <-waitDone; err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
