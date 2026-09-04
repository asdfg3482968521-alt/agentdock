package mcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
)

func TestToolDescriptorsDoNotExposePermissionAnnotations(t *testing.T) {
	descriptors := toolDescriptorsForNames([]string{"read_file", "skill_package"})
	byName := map[string]map[string]any{}
	for _, descriptor := range descriptors {
		name, _ := descriptor["name"].(string)
		byName[name] = descriptor
	}
	for _, tool := range []string{"read_file", "skill_package"} {
		if _, ok := byName[tool]["annotations"]; ok {
			t.Fatalf("%s should not expose permission annotations", tool)
		}
	}
}

func TestFilePublishDescriptorExposesFileRewritePath(t *testing.T) {
	descriptors := toolDescriptorsForNames([]string{"file_publish"})
	byName := map[string]map[string]any{}
	for _, descriptor := range descriptors {
		name, _ := descriptor["name"].(string)
		byName[name] = descriptor
	}
	args, ok := byName["file_publish"]["file_arg_rewrite_paths"].([]string)
	if !ok || len(args) != 1 || args[0] != "file" {
		t.Fatalf("file_publish file_arg_rewrite_paths = %#v", byName["file_publish"]["file_arg_rewrite_paths"])
	}
	meta, ok := byName["file_publish"]["_meta"].(map[string]any)
	if !ok || meta["file_arg_rewrite_paths"] == nil || meta["openai/fileParams"] == nil {
		t.Fatalf("file_publish _meta missing: %#v", meta)
	}
}

func TestToolEnvelopeMCPImageStripsInternalBase64FromStructuredContent(t *testing.T) {
	response := toolEnvelope("view_image", map[string]any{
		"ok":                   true,
		"source":               map[string]any{"type": "artifact", "artifact_id": "artifact-1"},
		"_mcp_image_base64":    "abc123",
		"_mcp_image_mime_type": "image/png",
	}, nil)
	content := response["content"].([]map[string]any)
	if content[0]["type"] != "image" || content[0]["data"] != "abc123" || content[0]["mimeType"] != "image/png" {
		t.Fatalf("content = %#v", content)
	}
	structured := response["structuredContent"].(map[string]any)
	if _, ok := structured["_mcp_image_base64"]; ok {
		t.Fatalf("structuredContent leaked internal base64: %#v", structured)
	}
	if _, ok := structured["_mcp_image_mime_type"]; ok {
		t.Fatalf("structuredContent leaked internal mime type: %#v", structured)
	}
}

func TestToolEnvelopePassesThroughDynamicMCPContent(t *testing.T) {
	response := toolEnvelope("mcp_tool_call", map[string]any{
		"ok":   true,
		"name": "figma:get_screenshot",
		"result": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "done"},
				map[string]any{"type": "image", "data": "abc123", "mimeType": "image/png"},
			},
			"structuredContent": map[string]any{"node_id": "1:2"},
		},
	}, nil)
	content, ok := response["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("dynamic MCP content = %#v", response["content"])
	}
	image, _ := content[1].(map[string]any)
	if image["type"] != "image" || image["data"] != "abc123" || image["mimeType"] != "image/png" {
		t.Fatalf("dynamic MCP image content = %#v", image)
	}
	structured, _ := response["structuredContent"].(map[string]any)
	if structured["name"] != "figma:get_screenshot" {
		t.Fatalf("structuredContent = %#v", structured)
	}
}

func TestOfficialSDKServerListsAndCallsAgentDockTools(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{AgentDockDefaultDir: root, AgentDockHome: filepath.Join(root, ".agentdock")}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := NewServer(runtime, cfg)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.sdk.Run(t.Context(), serverTransport) }()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "agentdock-test", Version: "1.0.0"},
		&mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}},
	)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	foundServerInfo := false
	foundFilePublishMetadata := false
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("Tools() error = %v", err)
		}
		switch tool.Name {
		case "server_info":
			foundServerInfo = true
		case "file_publish":
			paths, _ := tool.Meta["openai/fileParams"].([]any)
			foundFilePublishMetadata = len(paths) == 1 && paths[0] == "file"
		}
	}
	if !foundServerInfo || !foundFilePublishMetadata {
		t.Fatalf("tool discovery incomplete: server_info=%v file_publish_meta=%v", foundServerInfo, foundFilePublishMetadata)
	}

	result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["server"] != config.ServerName || result.IsError {
		t.Fatalf("CallTool() result = %#v", result)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("Server.Run() error = %v", err)
	}
}

func TestOfficialSDKToolsListPreservesFrozenD2AgentSchemas(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{AgentDockDefaultDir: root, AgentDockHome: filepath.Join(root, ".agentdock")}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := NewServer(runtime, cfg)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.sdk.Run(t.Context(), serverTransport) }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agentdock-d2-schema-test", Version: "1.0.0"}, &mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}})
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	actual := map[string]*mcpsdk.Tool{}
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("Tools() error = %v", err)
		}
		if strings.HasPrefix(tool.Name, "agent_") {
			actual[tool.Name] = tool
		}
	}
	for _, name := range []string{"agent_start", "agent_status", "agent_output", "agent_cancel", "agent_result", "agent_list"} {
		tool := actual[name]
		if tool == nil {
			t.Fatalf("raw tools/list missing %s", name)
		}
		file := "agent-" + strings.TrimPrefix(name, "agent_") + ".schema.json"
		body, err := os.ReadFile(filepath.Join("..", "app", "d2contract", file))
		if err != nil {
			t.Fatalf("read frozen %s: %v", file, err)
		}
		var frozen map[string]any
		if err := json.Unmarshal(body, &frozen); err != nil {
			t.Fatalf("parse frozen %s: %v", file, err)
		}
		expectedOutput, ok := frozen["x-success-output"].(map[string]any)
		if !ok {
			t.Fatalf("frozen %s missing x-success-output", file)
		}
		if !reflect.DeepEqual(tool.InputSchema, frozen) {
			t.Fatalf("raw tools/list %s input schema drifted from B FINAL: got=%#v want=%#v", name, tool.InputSchema, frozen)
		}
		if !reflect.DeepEqual(tool.OutputSchema, expectedOutput) {
			t.Fatalf("raw tools/list %s output schema drifted from B FINAL: got=%#v want=%#v", name, tool.OutputSchema, expectedOutput)
		}
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("Server.Run() error = %v", err)
	}
}

func TestAdaptiveStreamableHTTPKeepsNewProtocolStateless(t *testing.T) {
	cfg := config.Config{}
	server := NewServer(nil, cfg)

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"new-client","version":"1.0"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	server.HTTPHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if sessionID := response.Header().Get("Mcp-Session-Id"); sessionID != "" {
		t.Fatalf("Mcp-Session-Id = %q, want empty for sessionless protocol", sessionID)
	}
}

func TestStreamableHTTPHandlerAllowsAuthenticatedPublicHost(t *testing.T) {
	for name, cfg := range map[string]config.Config{
		"static token": {
			AuthToken:      "configured-token",
			OAuthServerURL: "https://dockmini.example",
		},
		"OAuth": {
			OAuthEnabled:   true,
			OAuthServerURL: "https://dockmini.example",
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveStreamableHTTPRequest(t, cfg)
			if response.Code == http.StatusForbidden {
				t.Fatalf("authenticated public Host was rejected: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestStreamableHTTPHandlerKeepsLocalhostProtection(t *testing.T) {
	for name, cfg := range map[string]config.Config{
		"no public URL": {
			AuthToken: "configured-token",
		},
		"public URL without authentication": {
			OAuthServerURL: "https://dockmini.example",
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveStreamableHTTPRequest(t, cfg)
			if response.Code != http.StatusForbidden {
				t.Fatalf("untrusted public Host status=%d, want %d; body=%s", response.Code, http.StatusForbidden, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "invalid Host header") {
				t.Fatalf("unexpected rejection body: %s", response.Body.String())
			}
		})
	}
}

func serveStreamableHTTPRequest(t *testing.T, cfg config.Config) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://dockmini.example/mcp", nil)
	request.Host = "dockmini.example"
	localAddress := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 18766}
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, localAddress))
	response := httptest.NewRecorder()
	NewServer(nil, cfg).HTTPHandler().ServeHTTP(response, request)
	return response
}