package app

import (
	"strings"
	"testing"
)

func TestD2AgentDraft202012ValidationRejectsFrozenInvalidInputs(t *testing.T) {
	valid := map[string]any{
		"session_id": "session_a", "provider": "anthropic", "model": nil, "role": "implementation",
		"credential_ref": "cred_anthropic_default", "prompt": "inspect", "permission": "read-only",
		"timeout_ms": 30000, "budget": map[string]any{"max_requests": 2},
	}
	if err := validateFrozenAgentInput("agent_start", valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing required", map[string]any{"session_id": "session_a"}},
		{"wrong nested budget", cloneAgentPublicArgs(valid, "budget", map[string]any{"max_requests": 0})},
		{"invalid credential ref", cloneAgentPublicArgs(valid, "credential_ref", "not-a-credential")},
		{"plaintext secret", cloneAgentPublicArgs(valid, "api_key", "D2_TEST_SECRET_public_validation")},
		{"routing override", cloneAgentPublicArgs(valid, "workspace", `C:\\forged`)},
		{"invalid permission", cloneAgentPublicArgs(valid, "permission", "full")},
		{"timeout out of range", cloneAgentPublicArgs(valid, "timeout_ms", 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateFrozenAgentInput("agent_start", tc.args); err == nil {
				t.Fatal("invalid input accepted")
			} else if toolErrorCode(err) != "INVALID_ARGUMENTS" {
				t.Fatalf("code=%s err=%v", toolErrorCode(err), err)
			}
		})
	}
	marker := "D2_TEST_SECRET_schema_rejection"
	leaky := cloneAgentPublicArgs(valid, "credential_ref", marker)
	err := validateFrozenAgentInput("agent_start", leaky)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("validation error leaked rejected value: %v", err)
	}
}

func cloneAgentPublicArgs(source map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(source)+1)
	for k, v := range source {
		out[k] = v
	}
	out[key] = value
	return out
}
