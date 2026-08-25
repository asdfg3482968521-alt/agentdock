package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
)

var c25PublicTargets = []string{
	"opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit", "opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git",
	"job_start", "job_status", "job_output", "job_cancel", "job_list",
	"session_open", "session_status", "session_list", "session_close",
	"agent_start", "agent_status", "agent_output", "agent_cancel", "agent_result", "agent_list",
}

func TestControlPlaneSurvivesUnavailableOpenCodeSupervisor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	cfg := config.Config{
		AgentDockDefaultDir:     root,
		AgentDockHome:           filepath.Join(root, ".agentdock"),
		OpenCodeEnabled:         true,
		OpenCodeRuntimeManifest: filepath.Join(root, "missing-opencode-runtime.json"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("execution-plane OpenCode readiness must not prevent MCP runtime construction: %v", err)
	}
	defer runtime.Close()

	names := map[string]bool{}
	for _, name := range runtime.ToolNames() {
		names[name] = true
	}
	for _, name := range c25PublicTargets {
		if !names[name] {
			t.Fatalf("control-plane registry lost required public target %s", name)
		}
	}

	_, err = runtime.Call(context.Background(), "job_start", map[string]any{
		"workspace": root,
		"tool":      "read",
		"arguments": map[string]any{"filePath": "missing.txt"},
	})
	var toolErr *app.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "JOB_RUNTIME_UNAVAILABLE" {
		t.Fatalf("execution-plane outage error = %v, want JOB_RUNTIME_UNAVAILABLE", err)
	}

	server := NewServer(runtime, cfg)
	for i := 0; i < 20; i++ {
		result, callErr := server.callTool(context.Background(), "server_info", &mcpsdk.CallToolRequest{})
		if callErr != nil || result == nil || result.IsError {
			t.Fatalf("server_info iteration %d result=%#v err=%v", i, result, callErr)
		}
	}
}

func TestToolRequestPanicIsContainedAtMCPBoundary(t *testing.T) {
	server := &Server{runtime: &app.Runtime{}}
	result, err := server.callTool(context.Background(), "server_info", &mcpsdk.CallToolRequest{})
	if err != nil {
		t.Fatalf("contained tool panic returned transport error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("contained tool panic result=%#v, want MCP tool error", result)
	}
}

func TestCancelledRequestDoesNotCorruptNextRequest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	cfg := config.Config{AgentDockDefaultDir: root, AgentDockHome: filepath.Join(root, ".agentdock")}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := NewServer(runtime, cfg)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = server.callTool(cancelled, "server_info", &mcpsdk.CallToolRequest{})

	result, err := server.callTool(context.Background(), "server_info", &mcpsdk.CallToolRequest{})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("request after cancellation result=%#v err=%v", result, err)
	}
}

func TestServerRestartPreservesPublicToolSchema(t *testing.T) {
	schemaSnapshot := func(t *testing.T) map[string][2]map[string]any {
		t.Helper()
		root := t.TempDir()
		t.Setenv("USERPROFILE", root)
		cfg := config.Config{AgentDockDefaultDir: root, AgentDockHome: filepath.Join(root, ".agentdock")}
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("Normalize() error = %v", err)
		}
		runtime, err := app.NewRuntime(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		server := NewServer(runtime, cfg)
		snapshot := map[string][2]map[string]any{}
		names := append([]string(nil), c25PublicTargets...)
		sort.Strings(names)
		for _, name := range names {
			if _, ok := toolDefinition(name); !ok {
				t.Fatalf("missing tool definition %s", name)
			}
			snapshot[name] = [2]map[string]any{app.InputSchemaForConfig(name, cfg), app.OutputSchemaForConfig(name, cfg)}
		}
		if server.sdk == nil {
			t.Fatal("server SDK was not initialized")
		}
		return snapshot
	}

	first := schemaSnapshot(t)
	second := schemaSnapshot(t)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("public tool schema changed across server restart")
	}
}
