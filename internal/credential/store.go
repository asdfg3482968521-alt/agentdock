package credential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
	"github.com/uvwt/agentdock/internal/fs/securepath"
	"github.com/uvwt/agentdock/internal/securedata"
)

const (
	credentialRecordVersion  = 1
	maxCredentialFileBytes   = 256 << 10
	maxCredentialSecretBytes = 64 << 10
	maxCredentialRecords     = 1024
)

var credentialProviderPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

type Metadata struct {
	Ref       string    `json:"credential_ref"`
	Provider  string    `json:"provider"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type record struct {
	Ref           string    `json:"credential_ref"`
	Provider      string    `json:"provider"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ProtectedData string    `json:"protected_data"`
}

type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(agentDockHome string) (*Store, error) {
	if strings.TrimSpace(agentDockHome) == "" {
		return nil, errors.New("agentdock home is required")
	}
	store := &Store{root: filepath.Join(agentDockHome, "credentials")}
	if err := store.ensureRoot(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) Put(ctx context.Context, ref, provider string, secret []byte) (Metadata, error) {
	if err := ValidateRef(ref); err != nil {
		return Metadata{}, err
	}
	provider, err := normalizeProvider(provider)
	if err != nil {
		return Metadata{}, err
	}
	if len(secret) == 0 {
		return Metadata{}, ErrEmptySecret
	}
	if len(secret) > maxCredentialSecretBytes {
		return Metadata{}, fmt.Errorf("credential secret exceeds %d bytes", maxCredentialSecretBytes)
	}

	release, err := s.acquireStoreLock(ctx)
	if err != nil {
		return Metadata{}, err
	}
	defer release()

	now := time.Now().UTC()
	createdAt := now
	existing, err := s.loadRecordLocked(ref)
	switch {
	case err == nil:
		if existing.Provider != provider {
			return Metadata{}, fmt.Errorf("%w: ref %q is bound to provider %q", ErrProviderMismatch, ref, existing.Provider)
		}
		createdAt = existing.CreatedAt
	case errors.Is(err, ErrNotFound):
		// New record.
	default:
		return Metadata{}, err
	}

	protected, err := securedata.Protect(secret, credentialEntropy(ref, provider))
	if err != nil {
		return Metadata{}, fmt.Errorf("protect credential %q: %w", ref, err)
	}
	record := record{
		Ref:           ref,
		Provider:      provider,
		Version:       credentialRecordVersion,
		CreatedAt:     createdAt,
		UpdatedAt:     now,
		ProtectedData: base64.StdEncoding.EncodeToString(protected),
	}
	if err := s.writeRecordLocked(record); err != nil {
		return Metadata{}, err
	}
	return record.metadata(), nil
}

func (s *Store) Resolve(ctx context.Context, ref string) (Secret, error) {
	if err := ValidateRef(ref); err != nil {
		return nil, err
	}
	release, err := s.acquireStoreLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	record, err := s.loadRecordLocked(ref)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(record.ProtectedData)
	if err != nil || len(ciphertext) == 0 {
		return nil, invalidRecord(ref)
	}
	plain, err := securedata.Unprotect(ciphertext, credentialEntropy(record.Ref, record.Provider))
	if err != nil {
		return nil, fmt.Errorf("unprotect credential %q: %w", ref, err)
	}
	return Secret(plain), nil
}

func (s *Store) Describe(ctx context.Context, ref string) (Metadata, error) {
	if err := ValidateRef(ref); err != nil {
		return Metadata{}, err
	}
	release, err := s.acquireStoreLock(ctx)
	if err != nil {
		return Metadata{}, err
	}
	defer release()
	record, err := s.loadRecordLocked(ref)
	if err != nil {
		return Metadata{}, err
	}
	return record.metadata(), nil
}

func (s *Store) List(ctx context.Context) ([]Metadata, error) {
	release, err := s.acquireStoreLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read credential store: %w", err)
	}
	items := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == ".store.lock" || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(items) >= maxCredentialRecords {
			return nil, fmt.Errorf("credential store exceeds %d records", maxCredentialRecords)
		}
		ref := strings.TrimSuffix(entry.Name(), ".json")
		if err := ValidateRef(ref); err != nil {
			return nil, invalidRecord(ref)
		}
		record, err := s.loadRecordLocked(ref)
		if err != nil {
			return nil, err
		}
		items = append(items, record.metadata())
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ref < items[j].Ref })
	return items, nil
}

func (s *Store) Delete(ctx context.Context, ref string) (bool, error) {
	path, err := s.path(ref)
	if err != nil {
		return false, err
	}
	release, err := s.acquireStoreLock(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("remove credential %q: %w", ref, err)
	}
	return true, nil
}

func (s *Store) path(ref string) (string, error) {
	if err := ValidateRef(ref); err != nil {
		return "", err
	}
	return filepath.Join(s.root, ref+".json"), nil
}

func (s *Store) ensureRoot() error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create credential store: %w", err)
	}
	if err := securepath.EnsurePrivate(s.root); err != nil {
		return fmt.Errorf("secure credential store: %w", err)
	}
	return nil
}

func (s *Store) acquireStoreLock(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	releaseFileLock, err := filelock.Acquire(lockCtx, filepath.Join(s.root, ".store.lock"))
	cancel()
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("lock credential store: %w", err)
	}
	return func() {
		releaseFileLock()
		s.mu.Unlock()
	}, nil
}

func (s *Store) loadRecordLocked(ref string) (record, error) {
	if err := s.ensureRoot(); err != nil {
		return record{}, err
	}
	path, err := s.path(ref)
	if err != nil {
		return record{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return record{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	if err != nil {
		return record{}, fmt.Errorf("read credential %q: %w", ref, err)
	}
	if !info.Mode().IsRegular() {
		return record{}, invalidRecord(ref)
	}
	if err := securepath.EnsurePrivate(path); err != nil {
		return record{}, fmt.Errorf("secure credential %q: %w", ref, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return record{}, fmt.Errorf("read credential %q: %w", ref, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	if err != nil {
		return record{}, fmt.Errorf("read credential %q: %w", ref, err)
	}
	if len(data) == 0 || len(data) > maxCredentialFileBytes {
		return record{}, invalidRecord(ref)
	}

	var stored record
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return record{}, invalidRecord(ref)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return record{}, invalidRecord(ref)
	}
	if stored.Ref != ref || stored.Version != credentialRecordVersion || stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() || stored.UpdatedAt.Before(stored.CreatedAt) {
		return record{}, invalidRecord(ref)
	}
	if err := ValidateRef(stored.Ref); err != nil {
		return record{}, invalidRecord(ref)
	}
	provider, err := normalizeProvider(stored.Provider)
	if err != nil || provider != stored.Provider {
		return record{}, invalidRecord(ref)
	}
	if ciphertext, err := base64.StdEncoding.DecodeString(stored.ProtectedData); err != nil || len(ciphertext) == 0 {
		return record{}, invalidRecord(ref)
	}
	return stored, nil
}

func (s *Store) writeRecordLocked(stored record) error {
	if err := s.ensureRoot(); err != nil {
		return err
	}
	path, err := s.path(stored.Ref)
	if err != nil {
		return err
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode credential %q: %w", stored.Ref, err)
	}
	if len(data) > maxCredentialFileBytes {
		return fmt.Errorf("credential record exceeds %d bytes", maxCredentialFileBytes)
	}
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return fmt.Errorf("write credential %q: %w", stored.Ref, err)
	}
	if err := securepath.EnsurePrivate(path); err != nil {
		return fmt.Errorf("secure credential %q: %w", stored.Ref, err)
	}
	return nil
}

func (stored record) metadata() Metadata {
	return Metadata{
		Ref:       stored.Ref,
		Provider:  stored.Provider,
		Version:   stored.Version,
		CreatedAt: stored.CreatedAt,
		UpdatedAt: stored.UpdatedAt,
	}
}

func normalizeProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !credentialProviderPattern.MatchString(provider) {
		return "", fmt.Errorf("%w: %q", ErrInvalidProvider, provider)
	}
	return provider, nil
}

func credentialEntropy(ref, provider string) []byte {
	return []byte(fmt.Sprintf("agentdock.d2.credential.v1\x00%s\x00%s", ref, provider))
}

func invalidRecord(ref string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRecord, ref)
}
