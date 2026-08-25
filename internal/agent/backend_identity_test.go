package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

type credentiallessMetadataBackend struct {
	*capturingAgentBackend
}

func (b *credentiallessMetadataBackend) BackendIdentity() BackendIdentity {
	return BackendIdentityOpenCode
}

func (b *credentiallessMetadataBackend) RequiresCredential() bool { return false }

func TestManagerCredentiallessBackendSkipsSecretResolutionAndPersistsIdentity(t *testing.T) {
	root := t.TempDir()
	calls := &atomic.Int32{}
	backend := &credentiallessMetadataBackend{capturingAgentBackend: &capturingAgentBackend{}}
	manager, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: root, RuntimeScope: "scope-a", PermissionMode: "project"},
		}},
		Credentials: countingCredentialResolver{calls: calls, secret: credential.Secret("must-not-be-used")},
		Backends:    map[string]AgentBackend{"opencode": backend},
		Limits:      DefaultAgentLimits(),
		IDFactory:   func() string { return "agent_opencode_identity" },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	started, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "opencode", Model: "default", CredentialRef: "cred_opencode_local",
		Prompt:         `{"instruction":"read","steps":[{"tool":"read","arguments":{"filePath":"README.md"}}]}`,
		PermissionMode: "project", MaxRequests: 1, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished, err := manager.Wait(context.Background(), started.AgentID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("credential resolver calls=%d want=0", got)
	}
	if finished.Backend != BackendIdentityOpenCode {
		t.Fatalf("backend identity=%q want=%q", finished.Backend, BackendIdentityOpenCode)
	}

	recovered, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: root, RuntimeScope: "scope-a", PermissionMode: "project"},
		}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("unused")},
		Backends:    map[string]AgentBackend{"opencode": backend},
		Limits:      DefaultAgentLimits(),
	})
	if err != nil {
		t.Fatalf("recover NewManager: %v", err)
	}
	defer recovered.Close()
	got, err := recovered.Get("session_a", started.AgentID)
	if err != nil {
		t.Fatalf("recovered Get: %v", err)
	}
	if got.Backend != BackendIdentityOpenCode {
		t.Fatalf("recovered backend identity=%q want=%q", got.Backend, BackendIdentityOpenCode)
	}
}

func TestRecoveredLegacyRecordWithoutBackendIdentityFallsBackToProvider(t *testing.T) {
	root := t.TempDir()
	persistence, err := newAgentPersistence(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := persistence.save(Record{
		AgentID: "agent_legacy_no_backend", SessionID: "session_a", Workspace: root, RuntimeScope: "scope-a",
		PermissionMode: "project", Provider: "anthropic", Model: "legacy-model", ResolvedModel: "legacy-model",
		Role: "implementation", CredentialRef: "cred_legacy", TimeoutMS: 1000,
		Budget: AgentBudget{MaxRequests: 1}, MaxRequests: 1, State: AgentStateSucceeded,
		CreatedAt: now, UpdatedAt: now, FinishedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManager(context.Background(), ManagerOptions{
		StorageRoot: root,
		Sessions: fakeSessionResolver{bindings: map[string]SessionBinding{
			"session_a": {SessionID: "session_a", Workspace: root, RuntimeScope: "scope-a", PermissionMode: "project"},
		}},
		Credentials: fakeCredentialResolver{secret: credential.Secret("legacy")},
		Limits:      DefaultAgentLimits(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()
	got, err := manager.Get("session_a", "agent_legacy_no_backend")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Backend != BackendIdentityProvider {
		t.Fatalf("legacy backend identity=%q want=%q", got.Backend, BackendIdentityProvider)
	}
}
