package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

type testCredentialItem struct {
	provider string
	secret   credential.Secret
	created  time.Time
	updated  time.Time
}

type testCredentialStore struct {
	mu    sync.RWMutex
	items map[string]testCredentialItem
}

func newTestCredentialStore() *testCredentialStore {
	return &testCredentialStore{items: map[string]testCredentialItem{}}
}

func (s *testCredentialStore) Put(_ context.Context, ref, provider string, secret []byte) (credential.Metadata, error) {
	if err := credential.ValidateRef(ref); err != nil {
		return credential.Metadata{}, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return credential.Metadata{}, credential.ErrInvalidProvider
	}
	if len(secret) == 0 {
		return credential.Metadata{}, credential.ErrEmptySecret
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	created := now
	if existing, ok := s.items[ref]; ok {
		if existing.provider != provider {
			return credential.Metadata{}, credential.ErrProviderMismatch
		}
		created = existing.created
	}
	s.items[ref] = testCredentialItem{
		provider: provider,
		secret:   credential.CloneSecret(secret),
		created:  created,
		updated:  now,
	}
	return credential.Metadata{Ref: ref, Provider: provider, Version: 1, CreatedAt: created, UpdatedAt: now}, nil
}

func (s *testCredentialStore) Resolve(_ context.Context, ref string) (credential.Secret, error) {
	if err := credential.ValidateRef(ref); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[ref]
	if !ok {
		return nil, credential.ErrNotFound
	}
	return credential.CloneSecret(item.secret), nil
}

func (s *testCredentialStore) Describe(_ context.Context, ref string) (credential.Metadata, error) {
	if err := credential.ValidateRef(ref); err != nil {
		return credential.Metadata{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[ref]
	if !ok {
		return credential.Metadata{}, credential.ErrNotFound
	}
	return credential.Metadata{
		Ref: ref, Provider: item.provider, Version: 1,
		CreatedAt: item.created, UpdatedAt: item.updated,
	}, nil
}

var _ agentCredentialStore = (*testCredentialStore)(nil)
