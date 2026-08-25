package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStdioEnvironmentInheritsParentAndScopedValuesWin(t *testing.T) {
	t.Setenv("AGENTDOCK_UNRELATED_PARENT_SENTINEL", "parent-only")
	t.Setenv("HOST_MAPPED_VALUE", "mapped-from-host")
	t.Setenv("MAPPED", "parent-mapped")
	t.Setenv("PATH", "parent-path")
	for _, key := range []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA", "GOPATH", "GOMODCACHE"} {
		t.Setenv(key, "")
	}

	environment, err := stdioEnvironment(ServerConfig{
		Name:       "demo",
		EnvFromEnv: map[string]string{"MAPPED": "HOST_MAPPED_VALUE"},
		RuntimeEnv: map[string]string{
			"MAPPED": "scoped-wins",
			"SCOPED": "configured",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := formattedEnvironmentMap(environment)
	if values["AGENTDOCK_UNRELATED_PARENT_SENTINEL"] != "parent-only" {
		t.Fatal("stdio MCP did not inherit unrelated parent environment variable")
	}
	if values["PATH"] != "parent-path" {
		t.Fatalf("PATH = %q, want parent-path", values["PATH"])
	}
	if values["MAPPED"] != "scoped-wins" || values["SCOPED"] != "configured" {
		t.Fatalf("unexpected merged environment: %#v", values)
	}
	for _, key := range []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA", "GOPATH", "GOMODCACHE"} {
		if values[key] != "" {
			t.Fatalf("%s was invented as %q", key, values[key])
		}
	}
}

func TestStdioEnvironmentEnvFromEnvOverridesParent(t *testing.T) {
	t.Setenv("MAPPED", "parent-value")
	t.Setenv("HOST_MAPPED_VALUE", "mapped-from-host")

	environment, err := stdioEnvironment(ServerConfig{
		Name:       "demo",
		EnvFromEnv: map[string]string{"MAPPED": "HOST_MAPPED_VALUE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := formattedEnvironmentMap(environment)
	if values["MAPPED"] != "mapped-from-host" {
		t.Fatalf("MAPPED = %q, want mapped-from-host", values["MAPPED"])
	}
}

func TestStdioEnvironmentRequiresMappedHostValue(t *testing.T) {
	_, err := stdioEnvironment(ServerConfig{
		Name:       "demo",
		EnvFromEnv: map[string]string{"CHILD": "AGENTDOCK_MISSING_HOST_VALUE"},
	})
	mcpErr, ok := err.(*Error)
	if !ok || mcpErr.Code != "MCP_AUTH_REQUIRED" {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
}

func TestHTTPHeaderUsesScopedEnvironmentBeforeHost(t *testing.T) {
	t.Setenv("MCP_HEADER_TOKEN", "host-token")
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()

	headers, err := resolveHTTPHeaders(ServerConfig{
		Name:       "demo",
		HeaderEnv:  map[string]string{"Authorization": "MCP_HEADER_TOKEN"},
		RuntimeEnv: map[string]string{"MCP_HEADER_TOKEN": "scoped-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Transport: headerRoundTripper{headers: headers}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if received != "scoped-token" {
		t.Fatalf("HTTP header = %q, want scoped-token", received)
	}
}

func formattedEnvironmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if found {
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			values[key] = value
		}
	}
	return values
}

func TestStdioSpawnFailureDoesNotExposeEnvironmentOrCorruptNextSpawn(t *testing.T) {
	secret := "m-b-secret-value"
	bad := newStdioClient(ServerConfig{
		Name:      "missing",
		Transport: TransportStdio,
		Command:   filepath.Join(t.TempDir(), "missing-mcp.exe"),
		RuntimeEnv: map[string]string{
			"AGENTDOCK_M_B_SECRET": secret,
		},
	})
	err := bad.initialize(context.Background())
	if err == nil {
		t.Fatal("missing MCP child unexpectedly initialized")
	}
	diagnostic := err.Error()
	var mcpErr *Error
	if errors.As(err, &mcpErr) {
		diagnostic += fmt.Sprintf(" %#v %v", mcpErr.Details, mcpErr.Cause)
	}
	if strings.Contains(diagnostic, secret) {
		t.Fatalf("spawn failure diagnostic leaked environment secret: %s", diagnostic)
	}

	t.Setenv("TEST_MCP_HELPER_MODE", "1")
	good := newStdioClient(ServerConfig{
		Name:       "healthy",
		Transport:  TransportStdio,
		Command:    os.Args[0],
		Args:       []string{"-test.run=TestMCPStdioHelperProcess"},
		EnvFromEnv: map[string]string{"GO_WANT_MCP_HELPER": "TEST_MCP_HELPER_MODE"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := good.initialize(ctx); err != nil {
		t.Fatalf("subsequent MCP spawn failed after prior error: %v", err)
	}
	defer good.close()
	tools, err := good.listTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected subsequent MCP tools: %#v", tools)
	}
}
