//go:build windows

package client

import (
	"strings"
	"testing"
)

func TestStdioEnvironmentWindowsCaseInsensitiveOverridesWin(t *testing.T) {
	const parentKey = "AgentDock_M_B_Stdio_Casing"
	const mappedHostKey = "AGENTDOCK_M_B_STDIO_MAPPED_HOST"
	t.Setenv(parentKey, "parent-value")
	t.Setenv(mappedHostKey, "mapped-value")

	environment, err := stdioEnvironment(ServerConfig{
		Name:       "demo",
		EnvFromEnv: map[string]string{"AGENTDOCK_M_B_STDIO_CASING": mappedHostKey},
		RuntimeEnv: map[string]string{"agentdock_m_b_stdio_casing": "runtime-value"},
	})
	if err != nil {
		t.Fatal(err)
	}

	matches := 0
	observedKey := ""
	observedValue := ""
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, parentKey) {
			matches++
			observedKey = key
			observedValue = value
		}
	}
	if matches != 1 {
		t.Fatalf("case-insensitive key count = %d, want 1; env=%#v", matches, environment)
	}
	if observedKey != parentKey {
		t.Fatalf("canonical parent key casing = %q, want %q", observedKey, parentKey)
	}
	if observedValue != "runtime-value" {
		t.Fatalf("RuntimeEnv override = %q, want runtime-value", observedValue)
	}
}
