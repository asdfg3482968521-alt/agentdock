package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestD2AgentRegistryMatchesFrozenBFinalSchemasAnd24TargetTools(t *testing.T) {
	expectedHashes := map[string]string{
		"agent_start":  "22eec042f0a39b86134f1c079f98f159cfe5c3fd321ad40601229c63752ca901",
		"agent_status": "0febbbdfc606600ab43e7045deb80a241cdce31c592eb019c3937019a04db036",
		"agent_output": "4abbc2a16553acc1c0fbc8ce9bc6524b8983d77889942bce869bdc2cac4a5bda",
		"agent_cancel": "cf9b7ad9e0c8c06a598d74bd62fe22c21e3477047f69c4f96331be60bd113937",
		"agent_result": "ca04957abc4bb631d62aa975576e0000ac8458ca67b786ae1f1ef6454fab9066",
		"agent_list":   "02b68b9eeb98a018991aadc147a55b4270f732e3cc693bfc07dd615bdbe87b51",
	}

	definitions := ToolDefinitions()
	byName := make(map[string]ToolDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}

	for name, expectedHash := range expectedHashes {
		fileName := name[len("agent_"):] + ".schema.json"
		data, err := os.ReadFile(filepath.Join("d2contract", "agent-"+fileName))
		if err != nil {
			t.Fatalf("read frozen schema for %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != expectedHash {
			t.Fatalf("%s frozen schema hash=%s want=%s", name, got, expectedHash)
		}
		var expected map[string]any
		if err := json.Unmarshal(data, &expected); err != nil {
			t.Fatalf("parse frozen schema for %s: %v", name, err)
		}
		definition, ok := byName[name]
		if !ok {
			t.Errorf("missing native tool %s", name)
			continue
		}
		if !reflect.DeepEqual(definition.InputSchema, expected) {
			t.Errorf("%s registry input schema drifted from B FINAL", name)
		}
		if success, ok := expected["x-success-output"].(map[string]any); !ok || !reflect.DeepEqual(definition.OutputSchema, success) {
			t.Errorf("%s registry output schema drifted from B FINAL x-success-output", name)
		}
	}

	target := []string{
		"opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit", "opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git",
		"job_start", "job_status", "job_output", "job_cancel", "job_list",
		"session_open", "session_status", "session_list", "session_close",
		"agent_start", "agent_status", "agent_output", "agent_cancel", "agent_result", "agent_list",
	}
	missing := make([]string, 0)
	for _, name := range target {
		if _, ok := byName[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("missing D2 target direct tools: %v", missing)
	}
}

func TestFrozenAgentStartSchemaForbidsPlaintextSecretsAndRoutingOverrides(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("d2contract", "agent-start.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for _, forbidden := range []string{"api_key", "access_token", "secret", "authorization", "password", "workspace", "runtime_scope", "agent_id", "permission_mode"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("frozen agent_start unexpectedly exposes %s", forbidden)
		}
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("agent_start additionalProperties=%v", schema["additionalProperties"])
	}
}

func TestC25OpenCodeProviderFitsFrozenAgentStartSchema(t *testing.T) {
	args := map[string]any{
		"session_id":     "session_c25_opencode",
		"provider":       "opencode",
		"model":          nil,
		"role":           "worker",
		"credential_ref": "cred_opencode_local",
		"prompt":         "inspect repository",
		"permission":     "read-only",
		"timeout_ms":     30000,
		"budget": map[string]any{
			"max_requests": 1,
		},
	}
	if err := validateFrozenAgentInput("agent_start", args); err != nil {
		t.Fatalf("frozen agent_start must accept provider=opencode without a schema change: %v", err)
	}
}

func TestD3AgentStartSchemaExposesOptionalLogicalStartIdempotencyKey(t *testing.T) {
	valid := map[string]any{
		"session_id": "session_d3_idempotency", "provider": "opencode-native", "model": nil,
		"role": "implementation", "credential_ref": "cred_opencode_local", "prompt": "inspect repository",
		"permission": "project", "timeout_ms": 30000, "budget": map[string]any{"max_requests": 1},
		"client_request_id": "logical-start-001",
	}
	if err := validateFrozenAgentInput("agent_start", valid); err != nil {
		t.Fatalf("keyed agent_start rejected: %v", err)
	}
	for _, key := range []string{"", strings.Repeat("x", 257)} {
		invalid := maps.Clone(valid)
		invalid["client_request_id"] = key
		if err := validateFrozenAgentInput("agent_start", invalid); err == nil {
			t.Fatalf("client_request_id %q unexpectedly accepted", key)
		}
	}
	output := frozenAgentOutputSchema("agent_start")
	properties := output["properties"].(map[string]any)
	for _, field := range []string{"created", "replayed"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("agent_start output does not expose %s", field)
		}
	}
}
