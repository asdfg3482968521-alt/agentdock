package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentOAuthClientSecretPostSurvivesReloadWithoutPlaintextSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth-state.json")
	store, err := NewPersistentOAuthStore(path, "test-signing-key-32-bytes-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	clientID, clientSecret, err := store.RegisterClientWithAuth(
		"Notion",
		[]string{"https://app.notion.com/workflows/mcp/oauth/callback"},
		[]string{"authorization_code", "refresh_token"},
		OAuthTokenEndpointAuthClientSecretPost,
	)
	if err != nil {
		t.Fatal(err)
	}
	if clientID == "" || clientSecret == "" {
		t.Fatalf("registration returned client_id=%q secret_empty=%v", clientID, clientSecret == "")
	}
	if !store.ValidateClientSecret(clientID, clientSecret) {
		t.Fatal("fresh confidential client secret was rejected")
	}
	if store.ValidateClientSecret(clientID, "wrong-secret") {
		t.Fatal("wrong confidential client secret was accepted")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(clientSecret)) {
		t.Fatal("OAuth state persisted a plaintext dynamic client secret")
	}

	reloaded, err := NewPersistentOAuthStore(path, "test-signing-key-32-bytes-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	registration, ok := reloaded.ClientRegistration(clientID)
	if !ok || registration.TokenEndpointAuthMethod != OAuthTokenEndpointAuthClientSecretPost || registration.ClientSecretHash == "" {
		t.Fatalf("reloaded registration = %#v, ok=%v", registration, ok)
	}
	if !reloaded.ValidateClientSecret(clientID, clientSecret) {
		t.Fatal("reloaded confidential client secret was rejected")
	}
}
