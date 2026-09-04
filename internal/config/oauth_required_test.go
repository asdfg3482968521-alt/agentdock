package config

import "testing"

func TestValidateAuthRejectsStartupWhenOAuthRequiredButDisabled(t *testing.T) {
	cfg := Config{Host: "127.0.0.1", Port: 8765, OAuthRequired: true}
	if err := cfg.ValidateAuth(); err == nil {
		t.Fatal("ValidateAuth() should refuse to start when AGENTDOCK_OAUTH_REQUIRED=true but OAuth is not enabled")
	}
}

func TestValidateAuthAcceptsForcedOAuthWhenFullyConfigured(t *testing.T) {
	t.Setenv("AGENTDOCK_OAUTH_PASSWORD", "0123456789ab")
	t.Setenv("AGENTDOCK_OAUTH_TOKEN_SECRET", "0123456789abcdef0123456789abcdef")
	cfg := Config{
		Host:           "127.0.0.1",
		Port:           8765,
		OAuthRequired:  true,
		OAuthEnabled:   true,
		OAuthServerURL: "https://dock.example.com",
	}
	if err := cfg.ValidateAuth(); err != nil {
		t.Fatalf("ValidateAuth() error = %v", err)
	}
}

func TestValidateAuthKeepsBearerOnlyWhenOAuthNotRequired(t *testing.T) {
	cfg := Config{Host: "127.0.0.1", Port: 8765}
	if err := cfg.ValidateAuth(); err != nil {
		t.Fatalf("ValidateAuth() error = %v", err)
	}
}
