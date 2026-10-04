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

func TestMCPClientContextReadsOpenAISessionMeta(t *testing.T) {
	request := &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{
			Meta: mcpsdk.Meta{"openai/session": " chatgpt-session-123 "},
			Name: "server_info",
		},
	}
	info := mcpClientContextFromRequest(request)
	if info.SessionID != "chatgpt-session-123" {
		t.Fatalf("SessionID = %q", info.SessionID)
	}
}

func TestOfficialSDKPropagatesOpenAISessionMetaAndBinding(t *testing.T) {
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
		&mcpsdk.Implementation{Name: "chatgpt-session-test", Version: "1.0.0"},
		&mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}},
	)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	const rawSession = "openai-session-e2e-secret-123"
	meta := mcpsdk.Meta{"openai/session": rawSession}
	if _, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Meta:      meta,
		Name:      "mcp_session_bind",
		Arguments: map[string]any{"conversation_url": "https://chatgpt.com/c/e2e-1", "conversation_title": "E2E", "watchdog_task_id": "wd-e2e"},
	}); err != nil {
		t.Fatalf("mcp_session_bind error = %v", err)
	}

	current, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Meta: meta,
		Name: "mcp_session_current",
	})
	if err != nil {
		t.Fatalf("mcp_session_current error = %v", err)
	}
	if current.IsError {
		t.Fatalf("mcp_session_current result = %#v", current)
	}
	structured, ok := current.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent = %#v", current.StructuredContent)
	}
	sessionInfo, ok := structured["session"].(map[string]any)
	if !ok {
		t.Fatalf("session = %#v", structured["session"])
	}
	if sessionInfo["conversation_url"] != "https://chatgpt.com/c/e2e-1" || sessionInfo["watchdog_task_id"] != "wd-e2e" {
		t.Fatalf("session binding = %#v", sessionInfo)
	}
	encoded, _ := json.Marshal(structured)
	if strings.Contains(string(encoded), rawSession) {
		t.Fatalf("public MCP response leaked raw session identity: %s", encoded)
	}

	statePath := filepath.Join(cfg.AgentDockHome, "mcp-client-sessions.json")
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read MCP session state: %v", err)
	}
	if strings.Contains(string(stateBytes), rawSession) {
		t.Fatalf("persisted MCP session state leaked raw session identity: %s", stateBytes)
	}
	var state map[string]any
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatalf("decode MCP session state: %v", err)
	}
	sessions, _ := state["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("persisted sessions = %#v", sessions)
	}
	persisted, _ := sessions[0].(map[string]any)
	if active, _ := persisted["active_calls"].(float64); active != 0 {
		t.Fatalf("active_calls after completed tool = %#v", persisted["active_calls"])
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("Server.Run() error = %v", err)
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

func TestNotionToolFilterBuildsSeparateCatalogs(t *testing.T) {
	t.Setenv("AGENTDOCK_MCP_TOOLS_INCLUDE", "server_info")
	t.Setenv("AGENTDOCK_MCP_TOOLS_EXCLUDE", "")
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

	server := NewServer(runtime, cfg)
	if server.sdkNotion == nil {
		t.Fatal("expected a Notion-filtered SDK server when the tool filter is configured")
	}

	notionTools := collectSDKToolNames(t, server.sdkNotion)
	if len(notionTools) != 1 || notionTools[0] != "server_info" {
		t.Fatalf("Notion catalog = %#v, want only server_info", notionTools)
	}
	fullTools := collectSDKToolNames(t, server.sdk)
	if len(fullTools) != len(runtime.ToolNames()) {
		t.Fatalf("full catalog size = %d, want %d (filter must not affect non-Notion clients)", len(fullTools), len(runtime.ToolNames()))
	}
	keptExecCommand := false
	for _, name := range fullTools {
		if name == "exec_command" {
			keptExecCommand = true
		}
	}
	if !keptExecCommand {
		t.Fatal("full catalog should keep exec_command for non-Notion clients")
	}
}

func TestSDKForRequestRoutesOnlyNotionClientsToFilteredCatalog(t *testing.T) {
	t.Setenv("AGENTDOCK_MCP_TOOLS_INCLUDE", "server_info")
	t.Setenv("AGENTDOCK_MCP_TOOLS_EXCLUDE", "")
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

	server := NewServer(runtime, cfg)

	notionUA := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil)
	notionUA.Header.Set("User-Agent", "Notion MCP")
	if server.sdkForRequest(notionUA) != server.sdkNotion {
		t.Fatal("Notion User-Agent should be routed to the filtered catalog")
	}
	notionQuery := httptest.NewRequest(http.MethodPost, "http://localhost/mcp?client=notion", nil)
	if server.sdkForRequest(notionQuery) != server.sdkNotion {
		t.Fatal("?client=notion should be routed to the filtered catalog")
	}
	otherClient := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil)
	otherClient.Header.Set("User-Agent", "cursor/1.0")
	if server.sdkForRequest(otherClient) != server.sdk {
		t.Fatal("non-Notion clients must keep the full catalog")
	}

	// 未配置过滤时保持旧行为：即使是 Notion 客户端也使用完整目录。
	t.Setenv("AGENTDOCK_MCP_TOOLS_INCLUDE", "")
	unfiltered := NewServer(runtime, cfg)
	if unfiltered.sdkNotion != nil {
		t.Fatal("no filter configured should not build a Notion catalog")
	}
	if unfiltered.sdkForRequest(notionUA) != unfiltered.sdk {
		t.Fatal("without a configured filter, even Notion clients get the full catalog")
	}
}

func collectSDKToolNames(t *testing.T, sdkServer *mcpsdk.Server) []string {
	t.Helper()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- sdkServer.Run(t.Context(), serverTransport) }()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "agentdock-filter-test", Version: "1.0.0"},
		&mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}},
	)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	names := []string{}
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("Tools() error = %v", err)
		}
		names = append(names, tool.Name)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("Server.Run() error = %v", err)
	}
	return names
}

func TestStreamableHTTPStaysStatelessAcrossProtocolVersions(t *testing.T) {
	// 回归保护：无论客户端协商哪个协议版本，/mcp 都必须保持无会话传输。
	// Notion 的 MCP 客户端不会稳定携带 Mcp-Session-Id（initialize 之后也会
	// 额外发起不带会话头的 GET SSE 流），有状态会话模式会把这些请求全部拒绝。
	for _, version := range []string{"2025-06-18", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			server := NewServer(nil, config.Config{})

			request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+version+`","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			response := httptest.NewRecorder()
			server.HTTPHandler().ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
			}
			if sessionID := response.Header().Get("Mcp-Session-Id"); sessionID != "" {
				t.Fatalf("Mcp-Session-Id = %q, want empty for stateless transport", sessionID)
			}
		})
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
