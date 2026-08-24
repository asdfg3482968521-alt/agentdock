package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

type resolvingTestProvider struct {
	runCalls atomic.Int32
	result   ProviderResult
	runErr   error
	block    chan struct{}
}

func (p *resolvingTestProvider) ResolveModel(raw string) (string, error) {
	model := strings.TrimSpace(raw)
	if model == "" || model == "default" {
		return "fake-deterministic-v1", nil
	}
	if model == "fake-deterministic-v1" {
		return model, nil
	}
	return "", &providerError{code: ProviderErrorModelUnavailable, err: errors.New("requested model is unavailable")}
}

func (p *resolvingTestProvider) Run(ctx context.Context, request ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
	p.runCalls.Add(1)
	if p.block != nil {
		select {
		case <-ctx.Done():
			return ProviderResult{}, ctx.Err()
		case <-p.block:
		}
	}
	result := p.result
	if result.ResolvedModel == "" {
		result.ResolvedModel = request.Model
	}
	return result, p.runErr
}

func newAlignmentManager(t *testing.T, provider ProviderAdapter, root string) *Manager {
	t.Helper()
	manager, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: filepath.Join(root, "workspace-a"), RuntimeScope: "scope-a", PermissionMode: "project"},
		}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("D2_TEST_SECRET_resource_alignment")},
		Providers:   map[string]ProviderAdapter{"fake": provider},
		Limits:      DefaultAgentLimits(),
		IDFactory:   func() string { return "agent_resource_alignment" },
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestAgentStartResolvesDeterministicModelAndPersistsD2Fields(t *testing.T) {
	root := t.TempDir()
	provider := &resolvingTestProvider{block: make(chan struct{})}
	manager := newAlignmentManager(t, provider, root)
	defer manager.Close()

	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "inspect",
		Role: "code-review", PermissionMode: "read-only", MaxRequests: 3, Timeout: 1750 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Model != "fake-deterministic-v1" || record.Model == "default" {
		t.Fatalf("resolved model=%q", record.Model)
	}
	if record.Role != "code-review" {
		t.Fatalf("role=%q", record.Role)
	}
	if record.TimeoutMS != 1750 {
		t.Fatalf("timeout_ms=%d", record.TimeoutMS)
	}
	if record.Budget.MaxRequests != 3 {
		t.Fatalf("budget=%+v", record.Budget)
	}
	if record.CredentialRef != "cred_fake_default" {
		t.Fatalf("credential_ref=%q", record.CredentialRef)
	}
	resource := PublicResource(record)
	if resource.Model != record.Model || resource.Role != record.Role || resource.TimeoutMS != record.TimeoutMS || resource.Permission != "read-only" {
		t.Fatalf("public resource drift: %+v", resource)
	}
	if resource.RuntimeScope != "scope-a" || resource.OutputCursor != 0 {
		t.Fatalf("public resource routing/audit fields=%+v", resource)
	}
	close(provider.block)
	_, _ = manager.Wait(context.Background(), record.AgentID)
}

func TestAgentStartExplicitSupportedModelPreservedAndUnsupportedRejectedBeforeRun(t *testing.T) {
	provider := &resolvingTestProvider{block: make(chan struct{})}
	manager := newAlignmentManager(t, provider, t.TempDir())
	defer manager.Close()

	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", Model: "fake-deterministic-v1", CredentialRef: "cred_fake_default",
		Prompt: "x", Role: "implementation", PermissionMode: "project", MaxRequests: 1, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Model != "fake-deterministic-v1" {
		t.Fatalf("model=%q", record.Model)
	}
	close(provider.block)
	_, _ = manager.Wait(context.Background(), record.AgentID)

	before := provider.runCalls.Load()
	manager.options.IDFactory = func() string { return "agent_resource_alignment_bad_model" }
	_, err = manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", Model: "unsupported-model", CredentialRef: "cred_fake_default",
		Prompt: "x", Role: "implementation", PermissionMode: "project", MaxRequests: 1, Timeout: time.Second,
	})
	if AgentErrorFrom(err, nil).Code != "MODEL_UNAVAILABLE" {
		t.Fatalf("unsupported model error=%v envelope=%+v", err, AgentErrorFrom(err, nil))
	}
	if provider.runCalls.Load() != before {
		t.Fatalf("provider Run called for unsupported model: before=%d after=%d", before, provider.runCalls.Load())
	}
}

func TestAgentUsagePersistsRealProviderUsageWithoutFabricatingUnknownTokens(t *testing.T) {
	known := &resolvingTestProvider{result: ProviderResult{
		ResolvedModel: "fake-deterministic-v1",
		Usage:         Usage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18, Known: true},
	}}
	manager := newAlignmentManager(t, known, t.TempDir())
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x",
		Role: "test-analysis", PermissionMode: "read-only", MaxRequests: 1, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.Wait(context.Background(), record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	manager.Close()
	if got.Usage.Requests != 1 || got.Usage.InputTokens == nil || *got.Usage.InputTokens != 11 || got.Usage.OutputTokens == nil || *got.Usage.OutputTokens != 7 {
		t.Fatalf("known usage=%+v", got.Usage)
	}

	unknown := &resolvingTestProvider{result: ProviderResult{ResolvedModel: "fake-deterministic-v1"}}
	manager2 := newAlignmentManager(t, unknown, t.TempDir())
	defer manager2.Close()
	record2, err := manager2.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x",
		Role: "test-analysis", PermissionMode: "read-only", MaxRequests: 1, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got2, err := manager2.Wait(context.Background(), record2.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Usage.Requests != 1 || got2.Usage.InputTokens != nil || got2.Usage.OutputTokens != nil {
		t.Fatalf("unknown usage fabricated token counts: %+v", got2.Usage)
	}
}

func TestAgentRestartPreservesD2ResourceFields(t *testing.T) {
	root := t.TempDir()
	provider := &resolvingTestProvider{block: make(chan struct{})}
	options := ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: filepath.Join(root, "workspace-a"), RuntimeScope: "scope-a", PermissionMode: "project"},
		}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("synthetic")},
		Providers:   map[string]ProviderAdapter{"fake": provider}, Limits: DefaultAgentLimits(),
		IDFactory: func() string { return "agent_restart_resource" },
	}
	manager, err := NewManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x",
		Role: "investigation", PermissionMode: "read-only", MaxRequests: 4, Timeout: 9 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.crashForTest()

	recovered, err := NewManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	got, err := recovered.Get("session_a", record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "fake-deterministic-v1" || got.Role != "investigation" || got.TimeoutMS != 9000 || got.Budget.MaxRequests != 4 {
		t.Fatalf("recovered resource fields=%+v", got)
	}
	if got.State != AgentStateInterrupted {
		t.Fatalf("recovered state=%s", got.State)
	}
}

func TestAgentTerminalFailurePersistsFrozenRedactedErrorEnvelope(t *testing.T) {
	secret := "D2_TEST_SECRET_resource_alignment"
	provider := &resolvingTestProvider{runErr: &providerError{code: ProviderErrorAuthFailure, err: fmt.Errorf("auth rejected %s", secret)}}
	manager := newAlignmentManager(t, provider, t.TempDir())
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x",
		Role: "test-analysis", PermissionMode: "read-only", MaxRequests: 1, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.Wait(context.Background(), record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentStateFailed || got.FailureReason == nil || got.FailureReason.Code != "AUTH_FAILURE" {
		t.Fatalf("failure resource=%+v", got)
	}
	if strings.Contains(got.FailureReason.Message, secret) || strings.Contains(got.Error, secret) {
		t.Fatalf("secret leaked in persisted failure: %+v", got)
	}
	resource := PublicResource(got)
	if resource.FailureReason == nil || resource.FailureReason.Code != "AUTH_FAILURE" {
		t.Fatalf("public failure_reason=%+v", resource.FailureReason)
	}
}

func TestAgentErrorEnvelopeMapsFrozenCodesAndRedactsSecrets(t *testing.T) {
	secret := []byte("D2_TEST_SECRET_error_envelope")
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"not_found", ErrAgentNotFound, "AGENT_NOT_FOUND"},
		{"ownership", ErrAgentSessionOwnershipMismatch, "AGENT_SESSION_OWNERSHIP_MISMATCH"},
		{"session_closed", ErrAgentSessionClosed, "AGENT_SESSION_CLOSED"},
		{"write_denied", ErrAgentWriteDenied, "AGENT_WRITE_DENIED"},
		{"not_terminal", ErrAgentNotTerminal, "AGENT_NOT_TERMINAL"},
		{"transition", ErrInvalidAgentTransition, "INVALID_AGENT_TRANSITION"},
		{"arguments", ErrInvalidArguments, "INVALID_ARGUMENTS"},
		{"credential_ref", credential.ErrInvalidRef, "INVALID_CREDENTIAL_REF"},
		{"credential_missing", credential.ErrNotFound, "CREDENTIAL_NOT_FOUND"},
		{"provider", ErrProviderUnsupported, "PROVIDER_UNSUPPORTED"},
		{"model", &providerError{code: ProviderErrorModelUnavailable, err: errors.New("unavailable")}, "MODEL_UNAVAILABLE"},
		{"auth", &providerError{code: ProviderErrorAuthFailure, err: fmt.Errorf("bad %s", secret)}, "AUTH_FAILURE"},
		{"rate", &providerError{code: ProviderErrorRateLimited, err: errors.New("rate")}, "RATE_LIMITED"},
		{"bad_request", &providerError{code: ProviderErrorBadRequest, err: errors.New("bad")}, "BAD_REQUEST"},
		{"unavailable", &providerError{code: ProviderErrorUnavailable, err: errors.New("down")}, "PROVIDER_UNAVAILABLE"},
		{"timeout", context.DeadlineExceeded, "TIMEOUT"},
		{"cancelled", context.Canceled, "CANCELLED"},
		{"malformed", &providerError{code: ProviderErrorMalformedResponse, err: errors.New("malformed")}, "MALFORMED_RESPONSE"},
		{"tool", ErrToolError, "TOOL_ERROR"},
		{"budget", ErrBudgetExceeded, "BUDGET_EXCEEDED"},
		{"resource", ErrResourceLimit, "RESOURCE_LIMIT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := AgentErrorFrom(tc.err, secret)
			if envelope.Code != tc.code {
				t.Fatalf("code=%q want=%q envelope=%+v", envelope.Code, tc.code, envelope)
			}
			if strings.Contains(envelope.Message, string(secret)) {
				t.Fatalf("secret leaked in envelope: %+v", envelope)
			}
		})
	}
}
