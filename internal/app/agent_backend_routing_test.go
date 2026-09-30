package app

import (
	"context"
	"errors"
	"testing"

	"github.com/uvwt/agentdock/internal/credential"
)

func TestRouteAgentBackend(t *testing.T) {
	tests := []struct {
		name               string
		provider           string
		want               agentBackendRoute
		requiresCredential bool
	}{
		{name: "opencode", provider: "opencode", want: agentBackendOpenCode, requiresCredential: false},
		{name: "opencode normalized", provider: "  OpenCode  ", want: agentBackendOpenCode, requiresCredential: false},
		{name: "opencode native autonomous", provider: "opencode-native", want: agentBackendOpenCode, requiresCredential: false},
		{name: "anthropic", provider: "anthropic", want: agentBackendProvider, requiresCredential: true},
		{name: "deepseek", provider: "deepseek", want: agentBackendProvider, requiresCredential: true},
		{name: "future provider", provider: "provider-x", want: agentBackendProvider, requiresCredential: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route := routeAgentBackend(tc.provider)
			if route != tc.want {
				t.Fatalf("routeAgentBackend(%q)=%q want=%q", tc.provider, route, tc.want)
			}
			if got := route.requiresStoredCredential(); got != tc.requiresCredential {
				t.Fatalf("route %q requiresStoredCredential=%v want=%v", route, got, tc.requiresCredential)
			}
		})
	}
}

func TestValidateAgentStartCredentialOpenCodeSkipsStoreLookup(t *testing.T) {
	if err := validateAgentStartCredential(context.Background(), nil, routeAgentBackend("opencode"), "opencode", "cred_opencode_local"); err != nil {
		t.Fatalf("OpenCode route must not require a stored provider credential: %v", err)
	}
}

func TestValidateAgentStartCredentialLegacyRequiresStoredBinding(t *testing.T) {
	store := newTestCredentialStore()
	ctx := context.Background()
	if err := validateAgentStartCredential(ctx, store, routeAgentBackend("anthropic"), "anthropic", "cred_missing_legacy"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("missing legacy credential error=%v want ErrNotFound", err)
	}
}

func TestValidateAgentStartCredentialLegacyRejectsProviderMismatch(t *testing.T) {
	store, err := credential.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Put(ctx, "cred_c25_provider_bound", "anthropic", []byte("C25_TEST_SECRET_provider_binding")); err != nil {
		t.Fatal(err)
	}
	if err := validateAgentStartCredential(ctx, store, routeAgentBackend("deepseek"), "deepseek", "cred_c25_provider_bound"); !errors.Is(err, credential.ErrInvalidRef) {
		t.Fatalf("provider mismatch error=%v want ErrInvalidRef", err)
	}
}
