//go:build windows

package credential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStorePutResolveKeepsSecretEncryptedAtRest(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	ctx := context.Background()
	ref := "cred_anthropic_default"
	provider := "anthropic"
	secret := []byte("D2_TEST_SECRET_credential_store_roundtrip")

	metadata, err := store.Put(ctx, ref, provider, secret)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if metadata.Ref != ref || metadata.Provider != provider || metadata.Version != 1 {
		t.Fatalf("Put() metadata = %+v", metadata)
	}

	path, err := store.path(ref)
	if err != nil {
		t.Fatalf("path() error = %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if bytes.Contains(stored, secret) {
		t.Fatal("credential record contains plaintext marker")
	}
	var fields map[string]any
	if err := json.Unmarshal(stored, &fields); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, forbidden := range []string{"api_key", "token", "password", "authorization", "secret"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("credential record persisted forbidden plaintext field %q", forbidden)
		}
	}
	protected, ok := fields["protected_data"].(string)
	if !ok || protected == "" {
		t.Fatal("credential record missing protected_data")
	}
	if decoded, err := base64.StdEncoding.DecodeString(protected); err != nil || len(decoded) == 0 {
		t.Fatalf("protected_data is not non-empty base64: len=%d err=%v", len(decoded), err)
	}

	resolved, err := store.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	defer Zero(resolved)
	if !bytes.Equal(resolved, secret) {
		t.Fatalf("Resolve() = %q, want %q", resolved, secret)
	}
}

func TestStoreRejectsProviderChangeForExistingRef(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Put(ctx, "cred_provider_bound", "anthropic", []byte("D2_TEST_SECRET_provider_a")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "cred_provider_bound", "deepseek", []byte("D2_TEST_SECRET_provider_b")); !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("Put() provider change error = %v, want ErrProviderMismatch", err)
	}
}

func TestStoreRejectsMalformedRecordWithoutLeakingSecret(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref := "cred_malformed_record"
	marker := "D2_TEST_SECRET_malformed_record"
	if _, err := store.Put(ctx, ref, "anthropic", []byte(marker)); err != nil {
		t.Fatal(err)
	}
	path, err := store.path(ref)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(stored, &fields); err != nil {
		t.Fatal(err)
	}
	fields["protected_data"] = base64.StdEncoding.EncodeToString([]byte("not-a-dpapi-blob"))
	fields["api_key"] = marker
	corrupted, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupted, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = store.Resolve(ctx, ref)
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Resolve() error = %v, want ErrInvalidRecord", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("Resolve() error leaked plaintext marker")
	}
}

func TestStoreListDescribeDeleteExposeMetadataOnly(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Put(ctx, "cred_deepseek_default", "deepseek", []byte("D2_TEST_SECRET_list_one")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "cred_anthropic_default", "anthropic", []byte("D2_TEST_SECRET_list_two")); err != nil {
		t.Fatal(err)
	}

	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 2 || items[0].Ref != "cred_anthropic_default" || items[1].Ref != "cred_deepseek_default" {
		t.Fatalf("List() = %+v", items)
	}
	described, err := store.Describe(ctx, "cred_deepseek_default")
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	if described.Provider != "deepseek" {
		t.Fatalf("Describe().Provider = %q", described.Provider)
	}

	removed, err := store.Delete(ctx, "cred_deepseek_default")
	if err != nil || !removed {
		t.Fatalf("Delete() = %v, %v", removed, err)
	}
	if _, err := store.Resolve(ctx, "cred_deepseek_default"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve() after delete error = %v, want ErrNotFound", err)
	}
}
