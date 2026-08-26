package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFromEnvReadsD3NativeURLWithoutEnablingOpenCode(t *testing.T) {
	setTestUserHome(t, t.TempDir())
	t.Setenv("AGENTDOCK_D3_NATIVE_URL", " http://127.0.0.1:40999/ ")
	t.Setenv("AGENTDOCK_OPENCODE_ENABLED", "false")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.OpenCodeNativeURL != "http://127.0.0.1:40999" {
		t.Fatalf("OpenCodeNativeURL=%q", cfg.OpenCodeNativeURL)
	}
	if cfg.OpenCodeEnabled {
		t.Fatal("native URL must not implicitly enable OpenCode")
	}
}

func TestNormalizeD3NativeURLAllowsLoopbackHTTPAndRemoteHTTPS(t *testing.T) {
	for _, endpoint := range []string{
		"http://localhost:40999",
		"http://127.0.0.1:40999",
		"http://[::1]:40999",
		"https://native.example.test",
	} {
		t.Run(endpoint, func(t *testing.T) {
			home := t.TempDir()
			cfg := Config{
				AgentDockHome:       filepath.Join(home, "home"),
				AgentDockDefaultDir: filepath.Join(home, "workspace"),
				OpenCodeNativeURL:   endpoint,
			}
			if err := cfg.Normalize(); err != nil {
				t.Fatalf("Normalize(%q) error=%v", endpoint, err)
			}
		})
	}
}

func TestNormalizeD3NativeURLRejectsUnsafeOrAmbiguousEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"relative/path",
		"ftp://127.0.0.1:40999",
		"http://192.0.2.10:40999",
		"http://native.example.test:40999",
		"https://user:pass@native.example.test",
		"https://native.example.test?token=x",
		"https://native.example.test#fragment",
	} {
		t.Run(strings.ReplaceAll(endpoint, "/", "_"), func(t *testing.T) {
			home := t.TempDir()
			cfg := Config{
				AgentDockHome:       filepath.Join(home, "home"),
				AgentDockDefaultDir: filepath.Join(home, "workspace"),
				OpenCodeNativeURL:   endpoint,
			}
			if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "AGENTDOCK_D3_NATIVE_URL") {
				t.Fatalf("Normalize(%q) error=%v, want D3 native URL validation error", endpoint, err)
			}
		})
	}
}
