//go:build windows

package desktopruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeHTTPSOriginAcceptsMCPURL(t *testing.T) {
	for _, input := range []string{
		"https://yc.188166.top:18443",
		"https://yc.188166.top:18443/",
		"https://yc.188166.top:18443/mcp",
		"https://yc.188166.top:18443/MCP/",
	} {
		origin, err := normalizeHTTPSOrigin(input)
		if err != nil {
			t.Fatalf("normalizeHTTPSOrigin(%q) error = %v", input, err)
		}
		if origin != "https://yc.188166.top:18443" {
			t.Fatalf("normalizeHTTPSOrigin(%q) = %q", input, origin)
		}
	}
}

func TestNormalizeHTTPSOriginRejectsOtherPaths(t *testing.T) {
	if _, err := normalizeHTTPSOrigin("https://yc.188166.top:18443/oauth/token"); err == nil {
		t.Fatal("normalizeHTTPSOrigin() should reject non-MCP paths")
	}
}

func TestClearQuickReadyMarkerPreservesServerURL(t *testing.T) {
	root := t.TempDir()
	files := tunnelFiles{
		serverURL: filepath.Join(root, "server-url.txt"),
		quickURL:  filepath.Join(root, "quick-tunnel-url.txt"),
	}
	if err := os.WriteFile(files.serverURL, []byte("https://named.example.com"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.quickURL, []byte("https://old-address.trycloudflare.com"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearQuickReadyMarker(files); err != nil {
		t.Fatalf("clearQuickReadyMarker() error = %v", err)
	}
	if _, err := os.Stat(files.quickURL); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quick ready marker should be removed, stat err = %v", err)
	}
	data, err := os.ReadFile(files.serverURL)
	if err != nil {
		t.Fatalf("server-url.txt must survive quick tunnel restarts: %v", err)
	}
	if strings.TrimSpace(string(data)) != "https://named.example.com" {
		t.Fatalf("server-url.txt content changed: %q", data)
	}
	// ready 标记已不存在时重复调用必须幂等。
	if err := clearQuickReadyMarker(files); err != nil {
		t.Fatalf("clearQuickReadyMarker() second call error = %v", err)
	}
}
