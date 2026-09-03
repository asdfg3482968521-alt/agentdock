package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/auth"
)

func TestOAuthNotionClientSecretPostRegistrationAndTokenFlow(t *testing.T) {
	t.Setenv("AGENTDOCK_OAUTH_PASSWORD", "")
	t.Setenv("AGENTDOCK_OAUTH_TOKEN_SECRET", "token-signing-secret")
	cfg := oauthTestConfig(t)
	store := auth.NewOAuthStore()

	registrationRequest := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{
		"client_name":"Notion",
		"client_uri":"https://www.notion.so",
		"logo_uri":"https://app.notion.com/images/notion-logo-block-main.svg",
		"redirect_uris":["https://app.notion.com/workflows/mcp/oauth/callback"],
		"grant_types":["authorization_code","refresh_token"],
		"response_types":["code"],
		"token_endpoint_auth_method":"client_secret_post",
		"application_type":"web",
		"software_id":"notion-mcp-client-v1",
		"software_version":"1.0.0",
		"contacts":["team@makenotion.com"]
	}`))
	registrationRequest.Header.Set("Content-Type", "application/json")
	registrationResponse := httptest.NewRecorder()
	handleRegister(registrationResponse, registrationRequest, cfg, store)
	if registrationResponse.Code != http.StatusCreated {
		t.Fatalf("registration status = %d; body=%s", registrationResponse.Code, registrationResponse.Body.String())
	}
	var registration struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		ClientSecretExpiresAt   int64  `json:"client_secret_expires_at"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(registrationResponse.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	if registration.ClientID == "" || registration.ClientSecret == "" ||
		registration.ClientSecretExpiresAt != 0 ||
		registration.TokenEndpointAuthMethod != auth.OAuthTokenEndpointAuthClientSecretPost {
		t.Fatalf("registration = %#v", registration)
	}
	stored, ok := store.ClientRegistration(registration.ClientID)
	if !ok || stored.TokenEndpointAuthMethod != auth.OAuthTokenEndpointAuthClientSecretPost ||
		stored.ClientSecretHash == "" || stored.ClientSecretHash == registration.ClientSecret {
		t.Fatalf("stored registration = %#v, ok=%v", stored, ok)
	}

	redirectURI := "https://app.notion.com/workflows/mcp/oauth/callback"
	authorizeValues := url.Values{
		"response_type":         {"code"},
		"client_id":             {registration.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {oauthTestChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"notion-state"},
	}
	authorizeResponse := httptest.NewRecorder()
	handleAuthorizeForTest(
		authorizeResponse,
		httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+authorizeValues.Encode(), nil),
		cfg,
		store,
	)
	if authorizeResponse.Code != http.StatusFound {
		t.Fatalf("authorize status = %d; body=%s", authorizeResponse.Code, authorizeResponse.Body.String())
	}
	location, err := url.Parse(authorizeResponse.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatalf("authorization redirect = %s", location)
	}

	tokenValues := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {registration.ClientID},
		"code_verifier": {oauthTestVerifier},
	}
	missingSecret := postTokenRequest(t, cfg, store, tokenValues)
	assertOAuthError(t, missingSecret, http.StatusBadRequest, "invalid_client")

	wrongSecretValues := cloneValues(tokenValues)
	wrongSecretValues.Set("client_secret", "wrong-secret")
	wrongSecret := postTokenRequest(t, cfg, store, wrongSecretValues)
	assertOAuthError(t, wrongSecret, http.StatusBadRequest, "invalid_client")

	tokenValues.Set("client_secret", registration.ClientSecret)
	tokenResponse := postTokenRequest(t, cfg, store, tokenValues)
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("token status = %d; body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var tokenPayload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenPayload.AccessToken == "" || tokenPayload.RefreshToken == "" {
		t.Fatalf("token payload = %#v", tokenPayload)
	}
	assertAuthorizedAccessToken(t, cfg, store, tokenPayload.AccessToken)

	refreshValues := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokenPayload.RefreshToken},
		"client_id":     {registration.ClientID},
		"client_secret": {registration.ClientSecret},
	}
	refreshResponse := postTokenRequest(t, cfg, store, refreshValues)
	if refreshResponse.Code != http.StatusOK {
		t.Fatalf("refresh status = %d; body=%s", refreshResponse.Code, refreshResponse.Body.String())
	}
}
