package app

import (
	"context"
	"errors"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
)

func TestJobToolsRemainRegisteredWhenOpenCodeDisabled(t *testing.T) {
	runtime := &Runtime{cfg: config.Config{OpenCodeEnabled: false}}
	seen := map[string]bool{}
	for _, name := range runtime.ToolNames() {
		seen[name] = true
	}
	for _, name := range []string{"job_start", "job_status", "job_output", "job_cancel", "job_list"} {
		if !seen[name] {
			t.Fatalf("%s missing from tools/list while OpenCode is disabled", name)
		}
	}
}

func TestJobStartReturnsStructuredRuntimeUnavailable(t *testing.T) {
	runtime := &Runtime{cfg: config.Config{OpenCodeEnabled: false}}
	_, err := runtime.Call(context.Background(), "job_start", map[string]any{
		"workspace": t.TempDir(), "tool": "bash", "arguments": map[string]any{"command": "echo hi"},
	})
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("error=%#v, want ToolError", err)
	}
	if toolErr.Code != "JOB_RUNTIME_UNAVAILABLE" || !toolErr.Retryable {
		t.Fatalf("error=%+v", toolErr)
	}
}

func TestJobSchemasExposeBoundedCursorAndListInputs(t *testing.T) {
	start := InputSchema("job_start")
	startProps := start["properties"].(map[string]any)
	for _, key := range []string{"workspace", "tool", "arguments", "permission_mode", "timeout", "metadata"} {
		if _, ok := startProps[key]; !ok {
			t.Fatalf("job_start missing %s", key)
		}
	}
	output := InputSchema("job_output")
	outputProps := output["properties"].(map[string]any)
	if _, ok := outputProps["cursor"]; !ok {
		t.Fatal("job_output missing cursor")
	}
	if _, ok := outputProps["limit"]; !ok {
		t.Fatal("job_output missing limit")
	}
	list := InputSchema("job_list")
	listProps := list["properties"].(map[string]any)
	if _, ok := listProps["limit"]; !ok {
		t.Fatal("job_list missing limit")
	}
}
