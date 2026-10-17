package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func watchdogTestConfig(t *testing.T, url string) Config {
	t.Helper()
	home := t.TempDir()
	setTestUserHome(t, home)
	cfg := Config{
		AgentDockDefaultDir:  filepath.Join(home, "AgentDock"),
		AgentDockHome:        filepath.Join(home, ".agentdock"),
		WatchdogDashboardURL: url,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	return cfg
}

func TestNormalizeWatchdogDashboardURLDefaults(t *testing.T) {
	home := t.TempDir()
	setTestUserHome(t, home)
	cfg := Config{AgentDockHome: filepath.Join(home, ".agentdock"), AgentDockDefaultDir: filepath.Join(home, "AgentDock")}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.WatchdogDashboardURL != "http://127.0.0.1:8770/" {
		t.Fatalf("WatchdogDashboardURL = %q, want default", cfg.WatchdogDashboardURL)
	}
}

func TestNormalizeWatchdogDashboardURLAcceptsLoopback(t *testing.T) {
	for _, url := range []string{
		"http://127.0.0.1:8770",
		"http://127.0.0.1:8770/",
		"http://localhost:8770/",
		"https://127.0.0.1:8770/",
		"https://localhost:8770/",
		"http://[::1]:8770/",
	} {
		cfg := watchdogTestConfig(t, url)
		if !strings.HasSuffix(cfg.WatchdogDashboardURL, "/") {
			t.Fatalf("WatchdogDashboardURL %q missing trailing slash", cfg.WatchdogDashboardURL)
		}
	}
}

func TestNormalizeWatchdogDashboardURLRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"http non-loopback", "http://example.com:8770/"},
		{"https non-loopback", "https://example.com:8770/"},
		{"http remote ip", "http://10.0.0.8:8770/"},
		{"userinfo", "http://user:secret@127.0.0.1:8770/"},
		{"query", "http://127.0.0.1:8770/?x=1"},
		{"fragment", "http://127.0.0.1:8770/#frag"},
		{"bad scheme", "ftp://127.0.0.1:8770/"},
		{"not a url", "127.0.0.1:8770"},
		{"relative", "/api/overview"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			setTestUserHome(t, home)
			cfg := Config{
				AgentDockDefaultDir:  filepath.Join(home, "AgentDock"),
				AgentDockHome:        filepath.Join(home, ".agentdock"),
				WatchdogDashboardURL: tc.url,
			}
			err := cfg.Normalize()
			if err == nil {
				t.Fatalf("Normalize() accepted %q", tc.url)
			}
			// 错误信息不得回显原始 URL（可能含凭据）。
			if strings.Contains(err.Error(), tc.url) {
				t.Fatalf("error echoed raw URL %q: %v", tc.url, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error echoed credentials: %v", err)
			}
		})
	}
}

func TestFromEnvWatchdogDashboardURL(t *testing.T) {
	home := t.TempDir()
	setTestUserHome(t, home)
	t.Setenv("AGENTDOCK_WATCHDOG_DASHBOARD_URL", "http://localhost:9876")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.WatchdogDashboardURL != "http://localhost:9876/" {
		t.Fatalf("WatchdogDashboardURL = %q", cfg.WatchdogDashboardURL)
	}
}
