package mcp

import "testing"

func TestMCPToolExposedDefaultsToAllTools(t *testing.T) {
	t.Setenv("AGENTDOCK_MCP_TOOLS_INCLUDE", "")
	t.Setenv("AGENTDOCK_MCP_TOOLS_EXCLUDE", "")
	if !mcpToolExposed("opencode_bash") || !mcpToolExposed("server_info") {
		t.Fatal("default filter should expose all tools")
	}
}

func TestMCPToolExposedIncludeList(t *testing.T) {
	t.Setenv("AGENTDOCK_MCP_TOOLS_INCLUDE", "server_info, opencode_bash ,opencode_read")
	for _, name := range []string{"server_info", "opencode_bash", "opencode_read"} {
		if !mcpToolExposed(name) {
			t.Fatalf("include list should expose %s", name)
		}
	}
	if mcpToolExposed("exec_command") {
		t.Fatal("include list should hide unlisted tools")
	}
}

func TestMCPToolExposedExcludeList(t *testing.T) {
	t.Setenv("AGENTDOCK_MCP_TOOLS_EXCLUDE", "agent_start, agent_status")
	if mcpToolExposed("agent_start") || mcpToolExposed("agent_status") {
		t.Fatal("exclude list should hide listed tools")
	}
	if !mcpToolExposed("opencode_bash") {
		t.Fatal("exclude list should keep other tools")
	}
}
