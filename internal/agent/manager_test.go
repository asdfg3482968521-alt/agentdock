package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

type fakeSessionResolver struct {
	bindings map[string]SessionBinding
}

func (f fakeSessionResolver) ResolveAgentSession(ctx context.Context, sessionID string) (SessionBinding, error) {
	binding, ok := f.bindings[sessionID]
	if !ok {
		return SessionBinding{}, ErrAgentSessionClosed
	}
	return binding, nil
}

type fakeCredentialResolver struct {
	secret credential.Secret
}

type countingCredentialResolver struct {
	calls  *atomic.Int32
	secret credential.Secret
}

func (c countingCredentialResolver) Resolve(ctx context.Context, ref string) (credential.Secret, error) {
	c.calls.Add(1)
	return credential.CloneSecret(c.secret), nil
}

func (f fakeCredentialResolver) Resolve(ctx context.Context, ref string) (credential.Secret, error) {
	if ref == "" {
		return nil, credential.ErrInvalidRef
	}
	return credential.CloneSecret(f.secret), nil
}

type blockingProvider struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	max     atomic.Int32
}

func (p *blockingProvider) ResolveModel(raw string) (string, error) {
	model := strings.TrimSpace(raw)
	if model == "" || model == "default" {
		return "fake-model", nil
	}
	return model, nil
}

func (p *blockingProvider) Run(ctx context.Context, req ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		prior := p.max.Load()
		if active <= prior || p.max.CompareAndSwap(prior, active) {
			break
		}
	}
	if p.started != nil {
		p.started <- req.AgentID
	}
	select {
	case <-ctx.Done():
		return ProviderResult{}, ctx.Err()
	case <-p.release:
		if err := emit(ProviderEvent{Kind: ProviderEventText, Text: "done"}); err != nil {
			return ProviderResult{}, err
		}
		return ProviderResult{Summary: "done", ResolvedModel: "fake-model", StopReason: "stop"}, nil
	}
}

func testAgentManager(t *testing.T, provider ProviderAdapter, limits AgentLimits) *Manager {
	t.Helper()
	root := t.TempDir()
	manager, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: filepath.Join(root, "a"), RuntimeScope: "scope-a", PermissionMode: "project"},
			"session_b": {SessionID: "session_b", Workspace: filepath.Join(root, "b"), RuntimeScope: "scope-b", PermissionMode: "read-only"},
		}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("D2_TEST_SECRET_manager")},
		Providers:   map[string]ProviderAdapter{"fake": provider},
		Limits:      limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestAgentManagerRequestedPermissionCannotExceedSession(t *testing.T) {
	root := t.TempDir()
	resolver := fakeSessionResolver{bindings: map[string]SessionBinding{
		"session_ro": {SessionID: "session_ro", Workspace: filepath.Join(root, "ro"), RuntimeScope: "scope-ro", PermissionMode: "read-only"},
		"session_rw": {SessionID: "session_rw", Workspace: filepath.Join(root, "rw"), RuntimeScope: "scope-rw", PermissionMode: "project"},
	}}
	manager, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: filepath.Join(root, "agents"), Sessions: resolver,
		Credentials: fakeCredentialResolver{secret: credential.Secret("synthetic")},
		Providers:   map[string]ProviderAdapter{"fake": &scriptedProvider{}},
		Limits:      DefaultAgentLimits(), IDFactory: func() string { return "agent_permission" },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	base := StartRequest{Provider: "fake", CredentialRef: "cred_permission_test", Prompt: "x", MaxRequests: 1}
	request := base
	request.SessionID = "session_ro"
	request.PermissionMode = "project"
	if _, err := manager.Start(context.Background(), request); !errors.Is(err, ErrAgentWriteDenied) {
		t.Fatalf("project agent in read-only session error=%v", err)
	}
	request.PermissionMode = "read-only"
	if record, err := manager.Start(context.Background(), request); err != nil || record.PermissionMode != "read-only" {
		t.Fatalf("read-only start record=%+v err=%v", record, err)
	}
	request = base
	request.SessionID = "session_rw"
	request.PermissionMode = "read-only"
	request.MaxRequests = 1
	manager.options.IDFactory = func() string { return "agent_permission_rw" }
	if record, err := manager.Start(context.Background(), request); err != nil || record.PermissionMode != "read-only" {
		t.Fatalf("explicit downgrade record=%+v err=%v", record, err)
	}
}

func TestAgentManagerRequiresCanonicalSessionBinding(t *testing.T) {
	provider := &blockingProvider{release: make(chan struct{})}
	manager := testAgentManager(t, provider, DefaultAgentLimits())
	defer manager.Close()

	if _, err := manager.Start(context.Background(), StartRequest{SessionID: "missing", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x", MaxRequests: 1}); !errors.Is(err, ErrAgentSessionClosed) {
		t.Fatalf("Start(missing session) error = %v", err)
	}
	record, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	if record.Workspace == "" || record.RuntimeScope != "scope-a" || record.PermissionMode != "project" {
		t.Fatalf("record did not use canonical session binding: %+v", record)
	}
	close(provider.release)
	if _, err := manager.Wait(context.Background(), record.AgentID); err != nil {
		t.Fatal(err)
	}
}

func TestAgentManagerReplaysSameLogicalStartAdmission(t *testing.T) {
	manager := testAgentManager(t, ProviderAdapterFunc(func(context.Context, ProviderRequest, func(ProviderEvent) error) (ProviderResult, error) {
		return ProviderResult{Summary: "done", ResolvedModel: "fake-model", StopReason: "stop"}, nil
	}), DefaultAgentLimits())
	defer manager.Close()
	request := StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "same logical start",
		MaxRequests: 1, ClientRequestID: "logical-start-001",
	}
	first, err := manager.StartAdmission(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.StartAdmission(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Replayed || second.Created || !second.Replayed {
		t.Fatalf("unexpected admission outcomes: first=%+v second=%+v", first, second)
	}
	if first.Record.AgentID != second.Record.AgentID {
		t.Fatalf("duplicate admission changed agent: first=%s second=%s", first.Record.AgentID, second.Record.AgentID)
	}
}

func TestAgentManagerRejectsChangedRequestForLogicalStartKey(t *testing.T) {
	manager := testAgentManager(t, ProviderAdapterFunc(func(context.Context, ProviderRequest, func(ProviderEvent) error) (ProviderResult, error) {
		return ProviderResult{Summary: "done", ResolvedModel: "fake-model", StopReason: "stop"}, nil
	}), DefaultAgentLimits())
	defer manager.Close()
	request := StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "original", MaxRequests: 1, ClientRequestID: "logical-start-conflict"}
	if _, err := manager.StartAdmission(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Prompt = "changed"
	if _, err := manager.StartAdmission(context.Background(), request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestAgentManagerLogicalStartContractAcrossConcurrencyRestartFailureScopeAndLegacy(t *testing.T) {
	provider := &blockingProvider{started: make(chan string, 8), release: make(chan struct{})}
	manager := testAgentManager(t, provider, DefaultAgentLimits())
	defer manager.Close()
	request := StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "pending", MaxRequests: 1, ClientRequestID: "contract-key"}
	results := make(chan StartOutcome, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			outcome, err := manager.StartAdmission(context.Background(), request)
			results <- outcome
			errs <- err
		}()
	}
	var first, second StartOutcome
	for i := 0; i < 2; i++ {
		outcome, err := <-results, <-errs
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = outcome
		} else {
			second = outcome
		}
	}
	if first.Record.AgentID != second.Record.AgentID || (first.Created == second.Created) || (first.Replayed == second.Replayed) {
		t.Fatalf("concurrent/pending outcomes first=%+v second=%+v", first, second)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("logical start did not begin")
	}
	select {
	case <-provider.started:
		t.Fatal("duplicate started a second execution")
	default:
	}
	other := request
	other.SessionID, other.PermissionMode = "session_b", "read-only"
	if outcome, err := manager.StartAdmission(context.Background(), other); err != nil || !outcome.Created || outcome.Record.AgentID == first.Record.AgentID {
		t.Fatalf("scope isolation outcome=%+v err=%v", outcome, err)
	}
	close(provider.release)
	if _, err := manager.Wait(context.Background(), first.Record.AgentID); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(context.Background(), manager.options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replay, err := restarted.StartAdmission(context.Background(), request)
	if err != nil || !replay.Replayed || replay.Record.AgentID != first.Record.AgentID {
		t.Fatalf("restart replay=%+v err=%v", replay, err)
	}
	legacyA, err := restarted.StartAdmission(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "legacy", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	legacyB, err := restarted.StartAdmission(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "legacy", MaxRequests: 1})
	if err != nil || legacyA.Record.AgentID == legacyB.Record.AgentID || !legacyA.Created || !legacyB.Created {
		t.Fatalf("legacy outcomes a=%+v b=%+v err=%v", legacyA, legacyB, err)
	}
}

func TestAgentManagerDoesNotPersistResolvedSecret(t *testing.T) {
	provider := &blockingProvider{release: make(chan struct{})}
	manager := testAgentManager(t, provider, DefaultAgentLimits())
	root := manager.StorageRoot()
	record, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	if _, err := manager.Wait(context.Background(), record.AgentID); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "D2_TEST_SECRET_manager") {
			t.Fatalf("persisted file %s leaked resolved secret", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentManagerBoundedGlobalAndSessionConcurrency(t *testing.T) {
	provider := &blockingProvider{started: make(chan string, 8), release: make(chan struct{})}
	limits := DefaultAgentLimits()
	limits.MaxConcurrent = 2
	limits.MaxConcurrentPerSession = 1
	limits.MaxConcurrentPerProvider = 2
	limits.MaxQueued = 4
	manager := testAgentManager(t, provider, limits)
	defer manager.Close()

	a1, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "a1", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "a2", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	b1, err := manager.Start(context.Background(), StartRequest{SessionID: "session_b", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "b1", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-provider.started:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only started %v", seen)
		}
	}
	if !seen[a1.AgentID] || !seen[b1.AgentID] || seen[a2.AgentID] {
		t.Fatalf("unexpected initial scheduling: seen=%v a1=%s a2=%s b1=%s", seen, a1.AgentID, a2.AgentID, b1.AgentID)
	}
	if provider.max.Load() > 2 {
		t.Fatalf("global concurrency = %d", provider.max.Load())
	}
	close(provider.release)
	for _, id := range []string{a1.AgentID, a2.AgentID, b1.AgentID} {
		if _, err := manager.Wait(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentManagerRejectsWhenBoundedQueueIsFull(t *testing.T) {
	provider := &blockingProvider{started: make(chan string, 4), release: make(chan struct{})}
	limits := DefaultAgentLimits()
	limits.MaxConcurrent = 1
	limits.MaxConcurrentPerSession = 1
	limits.MaxConcurrentPerProvider = 1
	limits.MaxQueued = 1
	manager := testAgentManager(t, provider, limits)
	defer manager.Close()
	if _, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "a1", MaxRequests: 1}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if _, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "a2", MaxRequests: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "a3", MaxRequests: 1}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("third Start() error=%v want RESOURCE_LIMIT", err)
	}
	close(provider.release)
}

func TestAgentManagerCancelWaitsForTerminalAndDoesNotAffectOtherSession(t *testing.T) {
	provider := &blockingProvider{started: make(chan string, 4), release: make(chan struct{})}
	limits := DefaultAgentLimits()
	limits.MaxConcurrent = 2
	limits.MaxConcurrentPerSession = 1
	manager := testAgentManager(t, provider, limits)
	defer manager.Close()
	a, _ := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "a", MaxRequests: 1})
	b, _ := manager.Start(context.Background(), StartRequest{SessionID: "session_b", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "b", MaxRequests: 1})
	for i := 0; i < 2; i++ {
		<-provider.started
	}
	cancelled, err := manager.Cancel(context.Background(), "session_a", a.AgentID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != AgentStateCancelled {
		t.Fatalf("state=%s", cancelled.State)
	}
	other, err := manager.Get("session_b", b.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if other.State.Terminal() {
		t.Fatalf("session B agent unexpectedly terminal: %s", other.State)
	}
	close(provider.release)
	_, _ = manager.Wait(context.Background(), b.AgentID)
}

func TestAgentManagerOutputIsBoundedCursorBasedAndRedacted(t *testing.T) {
	provider := ProviderAdapterFunc(func(ctx context.Context, req ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
		_ = emit(ProviderEvent{Kind: ProviderEventText, Text: "first D2_TEST_SECRET_manager"})
		_ = emit(ProviderEvent{Kind: ProviderEventText, Text: "second"})
		return ProviderResult{Summary: "ok", ResolvedModel: "fake-model"}, nil
	})
	limits := DefaultAgentLimits()
	limits.MaxOutputBytesPerRead = 16
	manager := testAgentManager(t, provider, limits)
	defer manager.Close()
	record, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.Wait(context.Background(), record.AgentID)
	first, err := manager.Output("session_a", record.AgentID, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor <= 0 || strings.Contains(first.Text, "D2_TEST_SECRET_manager") {
		t.Fatalf("output=%+v", first)
	}
	second, err := manager.Output("session_a", record.AgentID, first.NextCursor, 16)
	if err != nil {
		t.Fatal(err)
	}
	if second.NextCursor < first.NextCursor {
		t.Fatalf("cursor regressed: %d -> %d", first.NextCursor, second.NextCursor)
	}
}

func TestAgentManagerRestartReconcilesNonTerminalWithoutReplay(t *testing.T) {
	root := t.TempDir()
	provider := &blockingProvider{release: make(chan struct{})}
	options := ManagerOptions{
		StorageRoot: root,
		Sessions:    fakeSessionResolver{bindings: map[string]SessionBinding{"session_a": {SessionID: "session_a", Workspace: filepath.Join(root, "a"), RuntimeScope: "scope-a", PermissionMode: "project"}}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("synthetic")},
		Providers:   map[string]ProviderAdapter{"fake": provider},
		Limits:      DefaultAgentLimits(),
	}
	manager, err := NewManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Start(context.Background(), StartRequest{SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default", Prompt: "x", MaxRequests: 1})
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
	if got.State != AgentStateInterrupted {
		t.Fatalf("recovered state=%s", got.State)
	}
}

func TestAgentStartingCancelCannotTransitionBackToRunning(t *testing.T) {
	providerCalls := atomic.Int32{}
	provider := ProviderAdapterFunc(func(context.Context, ProviderRequest, func(ProviderEvent) error) (ProviderResult, error) {
		providerCalls.Add(1)
		return ProviderResult{}, nil
	})
	manager := testAgentManager(t, provider, DefaultAgentLimits())
	defer manager.Close()
	credentialCalls := atomic.Int32{}
	manager.options.Credentials = countingCredentialResolver{calls: &credentialCalls, secret: credential.Secret("synthetic")}
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(manager.rootCtx)
	cancel()
	managed := &managedAgent{
		record:  Record{AgentID: "agent_starting_cancel", SessionID: "session_a", Workspace: "x", RuntimeScope: "scope-a", PermissionMode: "project", Provider: "fake", Model: "fake-model", ResolvedModel: "fake-model", Role: "implementation", CredentialRef: "cred_fake_default", TimeoutMS: 1000, Budget: AgentBudget{MaxRequests: 1}, MaxRequests: 1, State: AgentStateCancelRequested, CreatedAt: now, StartedAt: &now, UpdatedAt: now},
		request: &StartRequest{SessionID: "session_a", Provider: "fake", Model: "fake-model", CredentialRef: "cred_fake_default", Role: "implementation", Prompt: "must not run", PermissionMode: "project", MaxRequests: 1, Timeout: time.Second},
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	manager.mu.Lock()
	manager.agents[managed.record.AgentID] = managed
	manager.activeGlobal++
	manager.activeProvider["fake"]++
	manager.activeSession["session_a"]++
	manager.mu.Unlock()

	manager.runAgent(ctx, managed.record.AgentID)
	got, err := manager.Get("session_a", managed.record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentStateCancelled {
		t.Fatalf("starting cancel transitioned to %s", got.State)
	}
	if credentialCalls.Load() != 0 {
		t.Fatalf("credential resolved after STARTING cancel: calls=%d", credentialCalls.Load())
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider ran after STARTING cancel: calls=%d", providerCalls.Load())
	}
}

// ProviderAdapterFunc makes deterministic manager tests concise.
type ProviderAdapterFunc func(context.Context, ProviderRequest, func(ProviderEvent) error) (ProviderResult, error)

func (f ProviderAdapterFunc) ResolveModel(raw string) (string, error) {
	model := strings.TrimSpace(raw)
	if model == "" || model == "default" {
		return "fake-model", nil
	}
	return model, nil
}

func (f ProviderAdapterFunc) Run(ctx context.Context, req ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
	return f(ctx, req, emit)
}

var _ = sync.Once{}
