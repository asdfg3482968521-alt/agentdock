package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/uvwt/agentdock/internal/credential"
)

type agentBackendRoute string

const (
	agentBackendProvider agentBackendRoute = "provider"
	agentBackendOpenCode agentBackendRoute = "opencode"
)

func routeAgentBackend(provider string) agentBackendRoute {
	if strings.EqualFold(strings.TrimSpace(provider), string(agentBackendOpenCode)) {
		return agentBackendOpenCode
	}
	return agentBackendProvider
}

func (route agentBackendRoute) requiresStoredCredential() bool {
	return route != agentBackendOpenCode
}

func validateAgentStartCredential(ctx context.Context, store *credential.Store, route agentBackendRoute, provider, credentialRef string) error {
	if err := credential.ValidateRef(credentialRef); err != nil {
		return err
	}
	if !route.requiresStoredCredential() {
		return nil
	}
	if store == nil {
		return credential.ErrNotFound
	}
	metadata, err := store.Describe(ctx, credentialRef)
	if err != nil {
		return err
	}
	if metadata.Provider != provider {
		return fmt.Errorf("%w: credential_ref is bound to a different provider", credential.ErrInvalidRef)
	}
	return nil
}
