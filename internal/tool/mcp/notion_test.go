package mcp

import (
	"reflect"
	"testing"

	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

func TestApplyKnownServerDefaultsExpandsBareNotionPreset(t *testing.T) {
	cfg := applyKnownServerDefaults(mcpclient.ServerConfig{
		Name:      "notion",
		Enabled:   true,
		TimeoutMS: 30000,
	})
	if cfg.Description != "Official Notion MCP via OAuth" {
		t.Fatalf("description = %q", cfg.Description)
	}
	if cfg.Transport != mcpclient.TransportStdio || cfg.Command != "npx" || cfg.URL != "" {
		t.Fatalf("unexpected Notion preset: %#v", cfg)
	}
	if cfg.TimeoutMS != notionOAuthTimeoutMS {
		t.Fatalf("timeout_ms = %d", cfg.TimeoutMS)
	}
	if !reflect.DeepEqual(cfg.Args, notionBridgeDefaultArgs) {
		t.Fatalf("args = %#v", cfg.Args)
	}
}

func TestApplyKnownServerDefaultsConvertsDirectNotionHTTP(t *testing.T) {
	cfg := applyKnownServerDefaults(mcpclient.ServerConfig{
		Name:        "workspace",
		Description: "Notion workspace",
		Transport:   mcpclient.TransportStreamableHTTP,
		URL:         "<https://mcp.notion.com/mcp>",
		Enabled:     true,
		TimeoutMS:   30000,
	})
	if cfg.Transport != mcpclient.TransportStdio || cfg.Command != "npx" || cfg.URL != "" {
		t.Fatalf("direct Notion HTTP was not converted: %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.Args, notionBridgeDefaultArgs) {
		t.Fatalf("args = %#v", cfg.Args)
	}
}

func TestApplyKnownServerDefaultsRepairsExplicitNotionBridge(t *testing.T) {
	cfg := applyKnownServerDefaults(mcpclient.ServerConfig{
		Name:        "notion",
		Description: "Notion",
		Transport:   mcpclient.TransportStdio,
		Command:     "npx",
		Args:        []string{"mcp-remote", "<https://mcp.notion.com/mcp>"},
		Enabled:     true,
		TimeoutMS:   30000,
	})
	for _, expected := range []string{"-y", "mcp-remote", notionMCPURL, "--transport", "http-only", "--auth-timeout", notionOAuthTimeoutSecond, "--body-timeout", "0", "--keep-alive"} {
		if !containsExactArg(cfg.Args, expected) {
			t.Fatalf("args %#v omit %q", cfg.Args, expected)
		}
	}
	if containsExactArg(cfg.Args, "<https://mcp.notion.com/mcp>") {
		t.Fatalf("markdown URL was not normalized: %#v", cfg.Args)
	}
	if cfg.TimeoutMS != notionOAuthTimeoutMS {
		t.Fatalf("timeout_ms = %d", cfg.TimeoutMS)
	}
}

func TestApplyKnownServerDefaultsLeavesOtherServersAlone(t *testing.T) {
	original := mcpclient.ServerConfig{
		Name:        "github",
		Description: "GitHub",
		Transport:   mcpclient.TransportStreamableHTTP,
		URL:         "https://example.com/mcp",
		Enabled:     true,
		TimeoutMS:   45000,
	}
	actual := applyKnownServerDefaults(original)
	if !reflect.DeepEqual(actual, original) {
		t.Fatalf("non-Notion config changed: actual=%#v original=%#v", actual, original)
	}
}
