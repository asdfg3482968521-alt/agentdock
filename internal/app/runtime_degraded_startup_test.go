package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
)

func TestRuntimeStartsDegradedWhenOpenCodeBootstrapFails(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		AgentDockHome:           filepath.Join(root, ".agentdock"),
		AgentDockDefaultDir:     filepath.Join(root, "workspace"),
		OpenCodeEnabled:         true,
		OpenCodeRuntimeManifest: filepath.Join(root, "missing-opencode-runtime-manifest.json"),
		OpenCodeLogDir:          filepath.Join(root, "logs", "opencode"),
		OpenCodeNativeURL:       "http://127.0.0.1:4096",
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}

	runtime, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime must fail open for optional subsystem failures: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	info := runtime.serverInfo()
	degraded, _ := info["degraded_startup"].(bool)
	if !degraded {
		t.Fatalf("degraded_startup = %#v, want true; info=%#v", info["degraded_startup"], info)
	}
	issues, ok := info["startup_issues"].([]StartupIssue)
	if !ok || len(issues) == 0 {
		t.Fatalf("startup_issues = %#v, want non-empty []StartupIssue", info["startup_issues"])
	}

	foundOpenCode := false
	for _, issue := range issues {
		if issue.Subsystem == "opencode_native_identity" || issue.Subsystem == "opencode_execution" {
			foundOpenCode = true
			break
		}
	}
	if !foundOpenCode {
		t.Fatalf("startup issues do not include OpenCode bootstrap failure: %#v", issues)
	}

	if _, err := runtime.Call(context.Background(), "server_info", map[string]any{}); err != nil {
		t.Fatalf("server_info unavailable in degraded mode: %v", err)
	}
}

func TestACPToolReturnsUnavailableInsteadOfPanicking(t *testing.T) {
	runtime := &Runtime{cfg: config.Config{ACPEnabled: true}}
	spec, ok := toolSpecByName("acp_session")
	if !ok {
		t.Fatal("acp_session spec not found")
	}
	_, err := spec.Handler(context.Background(), runtime, map[string]any{"action": "list"})
	if err == nil {
		t.Fatal("acp_session should report unavailable when ACP failed to initialize")
	}
	toolErr, ok := err.(*ToolError)
	if !ok || toolErr.Code != "ACP_RUNTIME_UNAVAILABLE" {
		t.Fatalf("error = %#v, want ACP_RUNTIME_UNAVAILABLE", err)
	}
}
