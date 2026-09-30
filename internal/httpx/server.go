package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gooauth2 "github.com/go-oauth2/oauth2/v4"
	oautherrors "github.com/go-oauth2/oauth2/v4/errors"
	oauthserver "github.com/go-oauth2/oauth2/v4/server"
	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/httpx/requestmeta"
	"github.com/uvwt/agentdock/internal/mcp"
	"github.com/uvwt/agentdock/internal/publicartifacts"
)

const (
	defaultOAuthAccessTokenTTLSeconds = int64(time.Hour / time.Second)
	neverOAuthClientExpiresInSeconds  = int64(999999 * 24 * 60 * 60)
	oauthRefreshTokenTTL              = 90 * 24 * time.Hour
	oauthFormBodyLimit                = 64 << 10
)

func Serve(ctx context.Context, server *mcp.Server, cfg config.Config) error {
	authRequired := cfg.AuthRequired()
	oauthStore := auth.NewOAuthStore()
	if cfg.OAuthEnabled {
		persistentStore, err := auth.NewPersistentOAuthStore(filepath.Join(cfg.AgentDockHome, "oauth", "state-v1.json"), oauthSigningKey())
		if err != nil {
			return fmt.Errorf("initialize OAuth token store: %w", err)
		}
		oauthStore = persistentStore
	}
	mux := http.NewServeMux()
	publicArtifactStore := publicartifacts.New(cfg.AgentDockHome, cfg.OAuthServerURL, cfg.Port)
	if err := publicArtifactStore.EnsureSecret(); err != nil {
		return fmt.Errorf("ensure public artifact secret: %w", err)
	}
	if err := publicArtifactStore.Cleanup(time.Now().UTC()); err != nil {
		return fmt.Errorf("clean public artifacts: %w", err)
	}
	slog.Info("http server configured", "host", cfg.Host, "port", cfg.Port, "auth_required", authRequired, "endpoint", "/mcp")
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		writeJSON(w, map[string]any{"ok": true, "version": buildinfo.Version})
	})
	mux.HandleFunc("/.well-known/mcp.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, serverCard(cfg, r))
	})
	mux.HandleFunc("/.well-known/mcp/server-card.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, serverCard(cfg, r))
	})
	mux.HandleFunc("/artifacts/public/", func(w http.ResponseWriter, r *http.Request) {
		publicArtifactStore.ServeHTTP(w, r, "/artifacts/public/")
	})
	registerOAuthRoutes(mux, cfg, oauthStore)
	mux.HandleFunc("/context", agentDockContextHandler(server, cfg, oauthStore))
	registerRuntimeAPI(mux, server, cfg, oauthStore)
	registerGPTActionsRoutes(mux, server, cfg, oauthStore)
	mux.HandleFunc("/mcp", mcpEndpointHandler(server, cfg, oauthStore))

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	httpServer := newHTTPServer(addr, loggingMiddleware(mux))
	httpServer.BaseContext = func(net.Listener) context.Context { return ctx }
	slog.Info("http server listening", "addr", addr)
	return serveHTTP(ctx, httpServer)
}

func serveHTTP(ctx context.Context, server *http.Server) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func registerOAuthRoutes(mux *http.ServeMux, cfg config.Config, store *auth.OAuthStore) {
	if !cfg.OAuthEnabled {
		return
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeOAuthMetadata(w, r, oauthMetadata(cfg, r))
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeOAuthMetadata(w, r, protectedResourceMetadata(cfg, r))
	})
	registrationLimiter := newFixedWindowLimiter(30, time.Minute)
	passwordLimiter := newFixedWindowLimiter(10, 5*time.Minute)
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !registrationLimiter.Allow(requestRemoteIP(r, cfg), time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeJSONStatus(w, http.StatusTooManyRequests, map[string]any{"error": "temporarily_unavailable"})
			return
		}
		handleRegister(w, r, cfg, store)
	})
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !passwordLimiter.Allow(requestRemoteIP(r, cfg), time.Now()) {
			w.Header().Set("Retry-After", "300")
			http.Error(w, "too many authorization attempts", http.StatusTooManyRequests)
			return
		}
		handleAuthorize(w, r, cfg, store)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		handleToken(w, r, cfg, store)
	})
}

func writeOAuthMetadata(w http.ResponseWriter, r *http.Request, metadata map[string]any) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, metadata)
}

type fixedWindowLimiter struct {
	mu      sync.Mutex
	maximum int
	window  time.Duration
	entries map[string]fixedWindowEntry
}

type fixedWindowEntry struct {
	started time.Time
	count   int
}

func newFixedWindowLimiter(maximum int, window time.Duration) *fixedWindowLimiter {
	return &fixedWindowLimiter{maximum: maximum, window: window, entries: map[string]fixedWindowEntry{}}
}

func (l *fixedWindowLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, exists := l.entries[key]
	if !exists && len(l.entries) >= 4096 {
		for candidate, value := range l.entries {
			if now.Sub(value.started) >= l.window {
				delete(l.entries, candidate)
			}
		}
		if len(l.entries) >= 4096 {
			return false
		}
	}
	if entry.started.IsZero() || now.Sub(entry.started) >= l.window {
		l.entries[key] = fixedWindowEntry{started: now, count: 1}
		return true
	}
	if entry.count >= l.maximum {
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}

func requestRemoteIP(r *http.Request, cfg config.Config) string {
	remote := parseRemoteIP(r.RemoteAddr)
	if remote == nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	networks := trustedProxyNetworks(cfg.TrustedProxyCIDRs)
	if !ipInNetworks(remote, networks) {
		return remote.String()
	}

	rawForwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if rawForwarded == "" {
		return remote.String()
	}
	parts := strings.Split(rawForwarded, ",")
	chain := make([]net.IP, 0, len(parts)+1)
	for _, part := range parts {
		ip := net.ParseIP(strings.TrimSpace(part))
		if ip == nil {
			// 可信代理传来的链必须完全可解析；异常链回退到直接对端，不能部分信任。
			return remote.String()
		}
		chain = append(chain, ip)
	}
	chain = append(chain, remote)
	for index := len(chain) - 1; index >= 0; index-- {
		if !ipInNetworks(chain[index], networks) {
			return chain[index].String()
		}
	}
	return chain[0].String()
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.Trim(strings.TrimSpace(remoteAddr), "[]"))
}

func trustedProxyNetworks(values []string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err == nil {
			networks = append(networks, network)
		}
	}
	return networks
}

func ipInNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func agentDockContextHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	authorizer := auth.Bearer{Token: cfg.AuthToken}
	authRequired := cfg.AuthRequired()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		staticOK := cfg.AuthToken != "" && authorizer.Authorized(r)
		oauthOK := authorizedOAuth(r, cfg, oauthStore)
		if authRequired && !staticOK && !oauthOK {
			setBearerChallenge(w, cfg, r, strings.TrimSpace(r.Header.Get("Authorization")) != "")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		result, err := server.AgentDockContext(ctx)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, result)
	}
}

func mcpEndpointHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	authorizer := auth.Bearer{Token: cfg.AuthToken}
	authRequired := cfg.AuthRequired()
	transport := server.HTTPHandler()
	return func(w http.ResponseWriter, r *http.Request) {
		staticOK := cfg.AuthToken != "" && authorizer.Authorized(r)
		oauthOK := authorizedOAuth(r, cfg, oauthStore)
		if authRequired && !staticOK && !oauthOK {
			setBearerChallenge(w, cfg, r, strings.TrimSpace(r.Header.Get("Authorization")) != "")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && acceptsEventStream(r.Header.Values("Accept")) {
			// Notion 的 MCP 客户端会在会话外发起 GET SSE 流（不携带 Mcp-Session-Id）。
			// 统一用独立的保活 SSE 流应答，不进入 SDK 的会话校验（与验证可连通的
			// fix/notion-mcp-compat-20260903 行为一致）。
			serveMCPStandaloneSSE(w, r)
			return
		}
		if r.Method == http.MethodPost && !prepareMCPRequestBody(w, r) {
			return
		}
		ctx := requestmeta.WithBaseURL(r.Context(), requestPublicBaseURL(cfg, r))
		transport.ServeHTTP(w, r.WithContext(ctx))
	}
}

func acceptsEventStream(values []string) bool {
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(candidate))
			if err == nil && strings.EqualFold(mediaType, "text/event-stream") {
				return true
			}
		}
	}
	return false
}

func serveMCPStandaloneSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func prepareMCPRequestBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body exceeds 1048576 bytes", http.StatusRequestEntityTooLarge)
			return false
		}
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      nil,
			"error":   map[string]any{"code": -32700, "message": "Parse error", "data": err.Error()},
		})
		return false
	}
	var payload json.RawMessage
	if err := decodeSingleJSON(bytes.NewReader(body), &payload); err != nil {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      nil,
			"error":   map[string]any{"code": -32700, "message": "Parse error", "data": err.Error()},
		})
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return true
}

func decodeSingleJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("request body must contain exactly one JSON value")
}

func requestPublicBaseURL(cfg config.Config, r *http.Request) string {
	configured := strings.TrimRight(strings.TrimSpace(cfg.OAuthServerURL), "/")
	if configured != "" {
		return configured
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	scheme := "http"
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		candidate := strings.ToLower(strings.TrimSpace(parts[0]))
		if candidate == "http" || candidate == "https" {
			scheme = candidate
		}
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host
}

func handleRegister(w http.ResponseWriter, r *http.Request, cfg config.Config, store *auth.OAuthStore) {
	if !cfg.OAuthEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "invalid_client_metadata", "error_description": "content-type must be application/json"})
		return
	}
	metadata, err := decodeClientRegistration(w, r)
	if err != nil {
		errorCode := "invalid_client_metadata"
		description := "client metadata is invalid"
		if errors.Is(err, errInvalidOAuthRedirectURI) {
			errorCode = "invalid_redirect_uri"
			description = "one or more redirect_uris are invalid"
		}
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": errorCode, "error_description": description})
		return
	}
	clientID, clientSecret, err := store.RegisterClientWithAuth(
		metadata.ClientName,
		metadata.RedirectURIs,
		metadata.GrantTypes,
		metadata.TokenEndpointAuthMethod,
	)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "server_error"})
		return
	}
	response := map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"redirect_uris":              metadata.RedirectURIs,
		"token_endpoint_auth_method": metadata.TokenEndpointAuthMethod,
		"grant_types":                metadata.GrantTypes,
		"response_types":             metadata.ResponseTypes,
	}
	if clientSecret != "" {
		response["client_secret"] = clientSecret
		response["client_secret_expires_at"] = 0
	}
	if metadata.ClientName != "" {
		response["client_name"] = metadata.ClientName
	}
	writeJSONStatus(w, http.StatusCreated, response)
}

var errInvalidOAuthRedirectURI = errors.New("invalid OAuth redirect URI")

type clientRegistrationMetadata struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

func decodeClientRegistration(w http.ResponseWriter, r *http.Request) (clientRegistrationMetadata, error) {
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(body)
	if err != nil {
		return clientRegistrationMetadata{}, fmt.Errorf("read client metadata: %w", err)
	}
	if err := rejectDuplicateTopLevelJSONKeys(data); err != nil {
		return clientRegistrationMetadata{}, err
	}
	var metadata clientRegistrationMetadata
	if err := decodeSingleJSON(strings.NewReader(string(data)), &metadata); err != nil {
		return metadata, fmt.Errorf("decode client metadata: %w", err)
	}
	metadata.ClientName = strings.TrimSpace(metadata.ClientName)
	if len(metadata.ClientName) > 200 {
		return metadata, errors.New("client_name exceeds 200 characters")
	}
	method := strings.TrimSpace(metadata.TokenEndpointAuthMethod)
	if method != auth.OAuthTokenEndpointAuthNone &&
		method != auth.OAuthTokenEndpointAuthClientSecretPost {
		return metadata, fmt.Errorf("token_endpoint_auth_method %q is not supported", method)
	}
	if len(metadata.RedirectURIs) == 0 || len(metadata.RedirectURIs) > 10 {
		return metadata, errors.New("redirect_uris must contain between 1 and 10 entries")
	}
	seenRedirects := make(map[string]struct{}, len(metadata.RedirectURIs))
	redirectURIs := make([]string, 0, len(metadata.RedirectURIs))
	for _, raw := range metadata.RedirectURIs {
		redirectURI := strings.TrimSpace(raw)
		if !validOAuthRedirectURI(redirectURI) {
			return metadata, fmt.Errorf("%w: %q", errInvalidOAuthRedirectURI, redirectURI)
		}
		if _, exists := seenRedirects[redirectURI]; exists {
			continue
		}
		seenRedirects[redirectURI] = struct{}{}
		redirectURIs = append(redirectURIs, redirectURI)
	}
	grantTypes, err := normalizeClientMetadataValues(
		metadata.GrantTypes,
		[]string{"authorization_code"},
		map[string]struct{}{"authorization_code": {}, "refresh_token": {}},
		"grant_type",
	)
	if err != nil {
		return metadata, err
	}
	if containsString(grantTypes, "refresh_token") && !containsString(grantTypes, "authorization_code") {
		return metadata, errors.New("grant_type \"refresh_token\" requires \"authorization_code\"")
	}
	responseTypes, err := normalizeClientMetadataValues(
		metadata.ResponseTypes,
		[]string{"code"},
		map[string]struct{}{"code": {}},
		"response_type",
	)
	if err != nil {
		return metadata, err
	}
	metadata.RedirectURIs = redirectURIs
	metadata.TokenEndpointAuthMethod = method
	metadata.GrantTypes = grantTypes
	metadata.ResponseTypes = responseTypes
	return metadata, nil
}

func rejectDuplicateTopLevelJSONKeys(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	start, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode client metadata: %w", err)
	}
	delim, ok := start.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("client metadata must be a JSON object")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode client metadata key: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("client metadata key must be a string")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate client metadata field %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode client metadata value: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode client metadata: %w", err)
	}
	return nil
}

func normalizeClientMetadataValues(values, defaults []string, supported map[string]struct{}, label string) ([]string, error) {
	if len(values) == 0 {
		values = defaults
	}
	seen := make(map[string]struct{}, len(values))
	clean := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if _, ok := supported[value]; !ok {
			return nil, fmt.Errorf("%s %q is not supported", label, value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		clean = append(clean, value)
	}
	return clean, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func protectedResourceMetadata(cfg config.Config, r *http.Request) map[string]any {
	issuer := issuerFor(cfg, r)
	return map[string]any{
		"resource":                 issuer + "/mcp",
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
	}
}

func setBearerChallenge(w http.ResponseWriter, cfg config.Config, r *http.Request, invalidToken bool) {
	if cfg.OAuthEnabled {
		metadataURL := issuerFor(cfg, r) + "/.well-known/oauth-protected-resource/mcp"
		challenge := `Bearer resource_metadata="` + metadataURL + `"`
		if invalidToken {
			challenge += `, error="invalid_token"`
		}
		w.Header().Set("WWW-Authenticate", challenge)
		return
	}
	challenge := "Bearer"
	if invalidToken {
		challenge += ` error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
}

func oauthMetadata(cfg config.Config, r *http.Request) map[string]any {
	issuer := issuerFor(cfg, r)
	return map[string]any{
		"issuer":                           issuer,
		"authorization_endpoint":           issuer + "/oauth/authorize",
		"token_endpoint":                   issuer + "/oauth/token",
		"registration_endpoint":            issuer + "/register",
		"response_types_supported":         []string{"code"},
		"grant_types_supported":            []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported": []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{
			auth.OAuthTokenEndpointAuthNone,
			auth.OAuthTokenEndpointAuthClientSecretPost,
		},
		"resource_indicators_supported": true,
	}
}

func issuerFor(cfg config.Config, r *http.Request) string {
	issuer := strings.TrimRight(cfg.OAuthServerURL, "/")
	if issuer != "" {
		return issuer
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func handleAuthorize(w http.ResponseWriter, r *http.Request, cfg config.Config, codes *auth.OAuthStore) {
	if !cfg.OAuthEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method == http.MethodPost {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			http.Error(w, "content-type must be application/x-www-form-urlencoded", http.StatusBadRequest)
			return
		}
		if len(r.URL.Query()["password"]) != 0 {
			http.Error(w, "password must be supplied in the request body", http.StatusBadRequest)
			return
		}
		for _, name := range authorizationParameterNames {
			if len(r.URL.Query()[name]) != 0 {
				http.Error(w, "OAuth parameters must be supplied in the request body", http.StatusBadRequest)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, oauthFormBodyLimit)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
	}
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		values = r.PostForm
		if len(values["password"]) > 1 {
			http.Error(w, "password must not be repeated", http.StatusBadRequest)
			return
		}
	}
	if duplicated := repeatedOAuthParameter(values, []string{"client_id", "redirect_uri"}); duplicated != "" {
		http.Error(w, "OAuth parameter must not be repeated: "+duplicated, http.StatusBadRequest)
		return
	}
	clientID := values.Get("client_id")
	redirectURI := values.Get("redirect_uri")
	challenge := values.Get("code_challenge")
	method := values.Get("code_challenge_method")
	state := values.Get("state")
	if !codes.ValidateClientRedirect(clientID, redirectURI) ||
		!codes.ClientAllowsGrant(clientID, "authorization_code") {
		http.Error(w, "invalid client_id or redirect_uri", http.StatusBadRequest)
		return
	}
	if duplicated := repeatedOAuthParameter(values, []string{
		"response_type", "code_challenge", "code_challenge_method", "state",
	}); duplicated != "" {
		redirectOAuthError(w, r, redirectURI, state, "invalid_request")
		return
	}
	responseType := values.Get("response_type")
	if responseType != "code" {
		redirectOAuthError(w, r, redirectURI, state, "unsupported_response_type")
		return
	}
	if !auth.ValidPKCEChallenge(challenge) || method != "S256" {
		redirectOAuthError(w, r, redirectURI, state, "invalid_request")
		return
	}
	expectedResource := issuerFor(cfg, r) + "/mcp"
	resourceValues := values["resource"]
	if len(resourceValues) > 1 {
		redirectOAuthError(w, r, redirectURI, state, "invalid_target")
		return
	}
	resource := expectedResource
	if len(resourceValues) == 1 {
		resource = strings.TrimSpace(resourceValues[0])
	}
	if resource == "" || !auth.EquivalentResourceURI(resource, expectedResource) {
		redirectOAuthError(w, r, redirectURI, state, "invalid_target")
		return
	}
	// AgentDock 只暴露当前 issuer 下唯一的 MCP protected resource。客户端省略
	// RFC 8707 resource 时直接绑定到这个固定资源；显式提供时仍必须严格匹配。
	// 同时把请求规范化，确保授权码内始终保存实际绑定的 protected resource。
	resource = expectedResource
	values.Set("resource", resource)
	if r.Method == http.MethodGet {
		r.URL.RawQuery = values.Encode()
	} else {
		r.PostForm.Set("resource", resource)
		r.Form.Set("resource", resource)
	}
	loginPassword := auth.ConfiguredLoginValue()
	registration, _ := codes.ClientRegistration(clientID)
	if loginPassword != "" && r.Method == http.MethodGet {
		writeAuthorizeForm(w, values, "", registration.ClientName)
		return
	}
	if loginPassword != "" && !auth.ConstantTimeEqual(r.PostForm.Get("password"), loginPassword) {
		writeAuthorizeForm(w, values, "invalid password", registration.ClientName)
		return
	}
	protocol := newOAuthProtocolServer(cfg, codes)
	if err := protocol.HandleAuthorizeRequest(w, r); err != nil {
		slog.Warn("OAuth authorize request failed", "error", err)
	}
}

var authorizationParameterNames = []string{
	"response_type", "client_id", "redirect_uri", "code_challenge",
	"code_challenge_method", "resource", "state",
}

func repeatedOAuthParameter(values url.Values, names []string) string {
	for _, name := range names {
		if len(values[name]) > 1 {
			return name
		}
	}
	return ""
}

func redirectOAuthError(w http.ResponseWriter, r *http.Request, redirectURI, state, code string) {
	values := url.Values{"error": []string{code}}
	if state != "" {
		values.Set("state", state)
	}
	http.Redirect(w, r, auth.AppendQuery(redirectURI, values), http.StatusFound)
}

func handleToken(w http.ResponseWriter, r *http.Request, cfg config.Config, store *auth.OAuthStore) {
	if !cfg.OAuthEnabled {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, oauthFormBodyLimit)
	if err := parseOAuthTokenForm(r); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": err.Error()})
		return
	}
	grantType := strings.TrimSpace(r.FormValue("grant_type"))
	if grantType != "authorization_code" && grantType != "refresh_token" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
		return
	}
	if reason := clientAuthenticationFailureReason(r, grantType, store); reason != "" {
		// 这里只记录固定枚举原因，避免把 client secret、授权码、令牌或回调地址写入日志。
		slog.Warn("OAuth token client rejected", "reason", reason, "grant_type", grantType)
		status := http.StatusBadRequest
		if _, _, ok := r.BasicAuth(); ok {
			status = http.StatusUnauthorized
			w.Header().Set("WWW-Authenticate", `Basic realm="oauth-token"`)
		}
		writeJSONStatus(w, status, map[string]any{"error": "invalid_client"})
		return
	}
	// resource 已在授权码或 Refresh Grant 中绑定；客户端在 Token 请求中省略时沿用原绑定，
	// 显式提供时仍由 TokenStore 校验是否与原绑定一致。
	resource := strings.TrimSpace(r.PostForm.Get("resource"))
	if len(r.PostForm["resource"]) == 1 && resource == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "invalid_target"})
		return
	}
	issuer := issuerFor(cfg, r)
	r = r.WithContext(auth.WithOAuthRequest(r.Context(), issuer, resource, strings.TrimSpace(r.PostForm.Get("client_id"))))
	protocol := newOAuthProtocolServer(cfg, store)
	if err := protocol.HandleTokenRequest(w, r); err != nil {
		slog.Warn("OAuth token request failed", "error", err)
	}
}

func parseOAuthTokenForm(r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return errors.New("content-type must be application/x-www-form-urlencoded")
	}
	if err := r.ParseForm(); err != nil {
		return errors.New("invalid form body")
	}
	if len(r.URL.Query()["resource"]) != 0 {
		return errors.New("parameter resource must be supplied in the request body")
	}
	singleValueFields := []string{"grant_type", "code", "redirect_uri", "client_id", "code_verifier", "refresh_token", "client_secret", "resource"}
	for _, name := range singleValueFields {
		if len(r.URL.Query()[name]) != 0 {
			return fmt.Errorf("parameter %s must be supplied in the request body", name)
		}
		if len(r.PostForm[name]) > 1 {
			return fmt.Errorf("parameter %s must not be repeated", name)
		}
	}
	return nil
}

func authorizedOAuth(r *http.Request, cfg config.Config, store *auth.OAuthStore) bool {
	if !cfg.OAuthEnabled {
		return false
	}
	issuer := issuerFor(cfg, r)
	resource := issuer + "/mcp"
	ctx := auth.WithOAuthRequest(r.Context(), issuer, resource, "")
	_, err := newOAuthProtocolServer(cfg, store).ValidationBearerToken(r.WithContext(ctx))
	return err == nil
}

func newOAuthProtocolServer(cfg config.Config, store *auth.OAuthStore) *oauthserver.Server {
	accessTokenTTLSeconds := cfg.OAuthAccessTokenTTLSeconds
	if cfg.OAuthAccessTokenNeverExpires {
		accessTokenTTLSeconds = 0
	} else if accessTokenTTLSeconds <= 0 {
		accessTokenTTLSeconds = defaultOAuthAccessTokenTTLSeconds
	}
	manager := auth.NewOAuthManager(
		store,
		oauthSigningKey(),
		accessTokenTTLSeconds,
		oauthRefreshTokenTTL,
	)
	protocolConfig := oauthserver.NewConfig()
	protocolConfig.AllowedResponseTypes = []gooauth2.ResponseType{gooauth2.Code}
	protocolConfig.AllowedGrantTypes = []gooauth2.GrantType{gooauth2.AuthorizationCode, gooauth2.Refreshing}
	protocolConfig.AllowedCodeChallengeMethods = []gooauth2.CodeChallengeMethod{gooauth2.CodeChallengeS256}
	protocolConfig.ForcePKCE = true

	protocol := oauthserver.NewServer(protocolConfig, manager)
	protocol.SetClientInfoHandler(oauthserver.ClientFormHandler)
	protocol.SetAccessTokenResolveHandler(func(request *http.Request) (string, bool) {
		return auth.ParseBearerToken(request.Header.Get("Authorization"))
	})
	protocol.SetResponseTokenHandler(func(w http.ResponseWriter, data map[string]interface{}, header http.Header, statusCode ...int) error {
		if _, issued := data["access_token"]; issued {
			if cfg.OAuthAccessTokenNeverExpires {
				// ChatGPT 当前不会持久保存缺少 expires_in 的 OAuth 令牌。
				// 服务端仍按永久令牌校验，这个极远的有限值只用于客户端兼容。
				data["expires_in"] = neverOAuthClientExpiresInSeconds
			} else {
				data["expires_in"] = accessTokenTTLSeconds
			}
		}
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		for key := range header {
			w.Header().Set(key, header.Get(key))
		}
		status := http.StatusOK
		if len(statusCode) > 0 && statusCode[0] > 0 {
			status = statusCode[0]
		}
		w.WriteHeader(status)
		return json.NewEncoder(w).Encode(data)
	})
	protocol.SetClientAuthorizedHandler(func(clientID string, grant gooauth2.GrantType) (bool, error) {
		return store.ClientAllowsGrant(clientID, grant.String()), nil
	})
	protocol.SetUserAuthorizationHandler(func(http.ResponseWriter, *http.Request) (string, error) {
		return "agentdock-user", nil
	})
	protocol.SetResponseErrorHandler(func(response *oautherrors.Response) {
		switch response.Error {
		case oautherrors.ErrInvalidGrant, oautherrors.ErrInvalidAuthorizeCode,
			oautherrors.ErrInvalidRefreshToken, oautherrors.ErrExpiredRefreshToken:
			response.Error = oautherrors.ErrInvalidGrant
			response.StatusCode = http.StatusBadRequest
		}
	})
	return protocol
}

func oauthSigningKey() string { return os.Getenv("AGENTDOCK_OAUTH_TOKEN_SECRET") }

func validOAuthRedirectURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return true
	case "http":
		hostname := strings.ToLower(parsed.Hostname())
		if hostname == "localhost" {
			return true
		}
		ip := net.ParseIP(hostname)
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}

func validClientAuthentication(r *http.Request, grantType string, store *auth.OAuthStore) bool {
	return clientAuthenticationFailureReason(r, grantType, store) == ""
}

func clientAuthenticationFailureReason(r *http.Request, grantType string, store *auth.OAuthStore) string {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return "authorization_header_present"
	}
	if err := r.ParseForm(); err != nil {
		return "form_parse_failed"
	}
	clientID := strings.TrimSpace(r.PostForm.Get("client_id"))
	registration, ok := store.ClientRegistration(clientID)
	if !ok {
		return "client_id_unregistered"
	}
	switch registration.TokenEndpointAuthMethod {
	case auth.OAuthTokenEndpointAuthNone:
		if len(r.PostForm["client_secret"]) != 0 {
			return "client_secret_present"
		}
	case auth.OAuthTokenEndpointAuthClientSecretPost:
		if len(r.PostForm["client_secret"]) != 1 || r.PostForm.Get("client_secret") == "" {
			return "client_secret_missing"
		}
		if !store.ValidateClientSecret(clientID, r.PostForm.Get("client_secret")) {
			return "client_secret_invalid"
		}
	default:
		return "client_auth_method_unsupported"
	}
	if !store.ClientAllowsGrant(clientID, grantType) {
		return "grant_not_allowed"
	}
	if grantType == "authorization_code" && !store.ValidateClientRedirect(clientID, strings.TrimSpace(r.PostForm.Get("redirect_uri"))) {
		return "redirect_uri_mismatch"
	}
	if grantType != "authorization_code" && grantType != "refresh_token" {
		return "unsupported_grant_type"
	}
	return ""
}

func serverCard(cfg config.Config, r *http.Request) map[string]any {
	issuer := issuerFor(cfg, r)
	authInfo := map[string]any{"type": "none"}
	if cfg.AuthToken != "" {
		authInfo = map[string]any{"type": "bearer", "scheme": "Bearer", "header": "Authorization"}
	}
	if cfg.OAuthEnabled {
		authInfo = map[string]any{"type": "oauth2", "scheme": "Bearer", "header": "Authorization", "authorizationUrl": issuer + "/oauth/authorize", "tokenUrl": issuer + "/oauth/token"}
	}
	return map[string]any{"name": config.ServerName, "title": "AgentDock", "version": buildinfo.Version, "description": "Local coding tools MCP server", "transport": map[string]any{"type": "streamable-http", "url": issuer + "/mcp"}, "auth": authInfo}
}

func writeJSON(w http.ResponseWriter, value any) {
	writeJSONStatus(w, http.StatusOK, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("write JSON response failed", "status", status, "error", err)
	}
}
