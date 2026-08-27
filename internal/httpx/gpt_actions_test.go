package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/mcp"
)

func TestGPTActionsOpenAPISchemaUsesPublicBaseURL(t *testing.T) {
	cfg := testConfig(t)
	cfg.OAuthServerURL = "https://dock.example"
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)

	schema := gptActionsOpenAPISchema(cfg, req)
	servers, ok := schema["servers"].([]map[string]any)
	if !ok || len(servers) != 1 || servers[0]["url"] != "https://dock.example" {
		t.Fatalf("servers = %#v", schema["servers"])
	}
	paths, ok := schema["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths type = %T", schema["paths"])
	}
	for _, path := range []string{"/gpt/status", "/gpt/tools", "/gpt/call"} {
		if _, exists := paths[path]; !exists {
			t.Fatalf("OpenAPI schema missing %s", path)
		}
	}
}

func TestGPTStatusUsesExistingBearerAuthentication(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthToken = "shared-agentdock-token"
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer runtime.Close()

	server := mcp.NewServer(runtime, cfg)
	handler := gptStatusHandler(server, cfg, auth.NewOAuthStore())

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/gpt/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	request := httptest.NewRequest(http.MethodGet, "/gpt/status", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("response = %#v", payload)
	}
	if !strings.Contains(recorder.Body.String(), "server_info") && !strings.Contains(recorder.Body.String(), "AgentDock") {
		t.Fatalf("status response does not contain server information: %s", recorder.Body.String())
	}
}

func TestGPTCallDispatchesDirectlyToRuntime(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthToken = "shared-agentdock-token"
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer runtime.Close()

	server := mcp.NewServer(runtime, cfg)
	handler := gptCallHandler(server, cfg, auth.NewOAuthStore())
	request := httptest.NewRequest(http.MethodPost, "/gpt/call", strings.NewReader(`{"tool":"server_info","arguments":{}}`))
	request.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("response = %#v", payload)
	}
	if payload["tool"] != "server_info" {
		t.Fatalf("tool = %#v", payload["tool"])
	}
}

func TestGPTCallReturnsToolErrorsAsApplicationPayload(t *testing.T) {
	cfg := testConfig(t)
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer runtime.Close()

	server := mcp.NewServer(runtime, cfg)
	handler := gptCallHandler(server, cfg, auth.NewOAuthStore())
	request := httptest.NewRequest(http.MethodPost, "/gpt/call", strings.NewReader(`{"tool":"definitely_missing_tool","arguments":{}}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"ok":false`) || !strings.Contains(recorder.Body.String(), "UNKNOWN_TOOL") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}
