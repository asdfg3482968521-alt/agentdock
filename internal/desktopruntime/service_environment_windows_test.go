//go:build windows

package desktopruntime

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveOAuthAccessTokenTTL(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		inherited  string
		want       string
	}{
		{name: "persisted wins", configured: "30d", inherited: "1h", want: "30d"},
		{name: "env fallback", inherited: "24h", want: "24h"},
		{name: "core default", want: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveOAuthAccessTokenTTL(test.configured, test.inherited); got != test.want {
				t.Fatalf("effectiveOAuthAccessTokenTTL(%q, %q) = %q, want %q", test.configured, test.inherited, got, test.want)
			}
		})
	}
}

func TestLoadControlPanelSettingsValidatesOAuthAccessTokenTTL(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "control-panel-settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"port":8765,"log_level":"info","oauth_access_token_ttl":"never"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadControlPanelSettings(root, 8765)
	if err != nil {
		t.Fatalf("loadControlPanelSettings() error = %v", err)
	}
	if settings.OAuthAccessTokenTTL != "never" {
		t.Fatalf("OAuthAccessTokenTTL = %q, want never", settings.OAuthAccessTokenTTL)
	}

	if err := os.WriteFile(settingsPath, []byte(`{"port":8765,"log_level":"info","oauth_access_token_ttl":"59s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadControlPanelSettings(root, 8765); err == nil {
		t.Fatal("loadControlPanelSettings() accepted invalid OAuth access token TTL")
	}
}

func TestProtectedTextRoundTripKeepsCiphertextAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.dpapi")
	secret := "D2_TEST_SECRET_desktopruntime_roundtrip"
	entropy := "agentdock.d2.desktopruntime.regression.v1"

	if err := writeProtectedText(path, secret, entropy); err != nil {
		t.Fatalf("writeProtectedText() error = %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if bytes.Contains(stored, []byte(secret)) {
		t.Fatal("protected file contains plaintext marker")
	}
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(stored))); err != nil {
		t.Fatalf("protected file is not base64 DPAPI data: %v", err)
	}

	got, err := readProtectedText(path, entropy)
	if err != nil {
		t.Fatalf("readProtectedText() error = %v", err)
	}
	if got != secret {
		t.Fatalf("readProtectedText() = %q, want %q", got, secret)
	}
}
