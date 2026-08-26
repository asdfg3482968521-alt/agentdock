package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

func newSessionToolsRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{AgentDockDefaultDir: root, AgentDockHome: filepath.Join(root, ".agentdock")}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt, root
}

func TestD1SessionToolsAreRegisteredWithStrictSchemas(t *testing.T) {
	rt, _ := newSessionToolsRuntime(t)
	for _, name := range []string{"session_open", "session_status", "session_list", "session_close"} {
		if !containsSessionTool(rt.ToolNames(), name) {
			t.Fatalf("missing direct tool %s", name)
		}
		schema := InputSchema(name)
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema must be strict: %+v", name, schema)
		}
	}
	open := InputSchema("session_open")
	// D3 default-root change: repo_root is now optional; a NEW project defaults
	// under AGENTDOCK_OPENCODE_PROJECT_ROOT when the caller omits it.
	assertRequiredFields(t, open, "project_id")
	status := InputSchema("session_status")
	assertRequiredFields(t, status, "session_id")
	closeSchema := InputSchema("session_close")
	assertRequiredFields(t, closeSchema, "session_id")
}

func TestD1SessionSchemasMatchFrozenContractDetails(t *testing.T) {
	open := InputSchema("session_open")
	openProps := open["properties"].(map[string]any)
	if got := openProps["permission_mode"].(map[string]any)["default"]; got != "project" {
		t.Fatalf("session_open permission_mode default=%#v want project", got)
	}
	for _, name := range []string{"base_ref", "client_tag", "isolation_mode"} {
		prop := openProps[name].(map[string]any)
		value, exists := prop["default"]
		if !exists || value != nil {
			t.Fatalf("session_open %s default=%#v exists=%v want explicit null", name, value, exists)
		}
	}
	list := InputSchema("session_list")
	listProps := list["properties"].(map[string]any)
	if got := listProps["state_filter"].(map[string]any)["default"]; got != "all" {
		t.Fatalf("session_list state_filter default=%#v want all", got)
	}
	if got := listProps["limit"].(map[string]any)["default"]; got != 100 {
		t.Fatalf("session_list limit default=%#v want 100", got)
	}
	for _, name := range []string{"project_id", "client_tag", "cursor"} {
		prop := listProps[name].(map[string]any)
		value, exists := prop["default"]
		if !exists || value != nil {
			t.Fatalf("session_list %s default=%#v exists=%v want explicit null", name, value, exists)
		}
	}

	statusOutput := OutputSchema("session_status")
	if statusOutput["additionalProperties"] != false {
		t.Fatalf("session_status output must be strict: %+v", statusOutput)
	}
	session := statusOutput["properties"].(map[string]any)["session"].(map[string]any)
	if session["additionalProperties"] != false {
		t.Fatalf("D1 Session resource must be strict: %+v", session)
	}
	sessionProps := session["properties"].(map[string]any)
	if got := sessionProps["metadata_version"].(map[string]any)["const"]; got != "D1-v1" {
		t.Fatalf("metadata_version const=%#v want D1-v1", got)
	}
	if got := sessionProps["session_id"].(map[string]any)["maxLength"]; got != 128 {
		t.Fatalf("session_id maxLength=%#v want 128", got)
	}
	if got := sessionProps["runtime_id"].(map[string]any)["maxLength"]; got != 256 {
		t.Fatalf("runtime_id maxLength=%#v want 256", got)
	}
	if got := sessionProps["failure_reason"].(map[string]any)["maxLength"]; got != 4096 {
		t.Fatalf("failure_reason maxLength=%#v want 4096", got)
	}
	allOf, ok := session["allOf"].([]map[string]any)
	if !ok || len(allOf) != 3 {
		t.Fatalf("D1 Session allOf=%T len=%d want 3 frozen conditional rules", session["allOf"], len(allOf))
	}
	closeOutput := OutputSchema("session_close")
	closeProps := closeOutput["properties"].(map[string]any)
	jobsCancelled := closeProps["jobs_cancelled"].(map[string]any)
	items := jobsCancelled["items"].(map[string]any)
	if got := items["minLength"]; got != 1 {
		t.Fatalf("session_close jobs_cancelled item minLength=%#v want 1", got)
	}
}

func TestD1SessionLifecycleReturnsContractShape(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	workspace := filepath.Join(root, "plain")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	opened, err := rt.Call(context.Background(), "session_open", map[string]any{
		"project_id": "plain", "repo_root": workspace, "client_tag": "chat-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened["ok"] != true {
		t.Fatalf("session_open missing ok=true: %+v", opened)
	}
	session, ok := opened["session"].(map[string]any)
	if !ok {
		t.Fatalf("session_open session type=%T value=%+v", opened["session"], opened["session"])
	}
	if session["state"] != "ACTIVE" || session["workspace_type"] != "non_git" || session["metadata_version"] != "D1-v1" {
		t.Fatalf("session contract mismatch: %+v", session)
	}
	if session["base_ref"] != nil || session["branch"] != nil || session["worktree"] != nil || session["failure_reason"] != nil {
		t.Fatalf("non-Git nullable fields must be explicit null: %+v", session)
	}
	sessionID, _ := session["session_id"].(string)
	if sessionID == "" {
		t.Fatal("session_id missing")
	}

	status, err := rt.Call(context.Background(), "session_status", map[string]any{"session_id": sessionID})
	if err != nil || status["ok"] != true {
		t.Fatalf("session_status=%+v err=%v", status, err)
	}
	listed, err := rt.Call(context.Background(), "session_list", map[string]any{"state_filter": "active", "project_id": "plain", "client_tag": "chat-a"})
	if err != nil {
		t.Fatal(err)
	}
	if listed["ok"] != true {
		t.Fatalf("session_list missing ok=true: %+v", listed)
	}
	items, _ := listed["sessions"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one active session: %+v", listed)
	}

	closed, err := rt.Call(context.Background(), "session_close", map[string]any{"session_id": sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if closed["ok"] != true || closed["branch_preserved"] != true || closed["worktree_preserved"] != true {
		t.Fatalf("session_close contract mismatch: %+v", closed)
	}
	closedAgain, err := rt.Call(context.Background(), "session_close", map[string]any{"session_id": sessionID})
	if err != nil || closedAgain["ok"] != true {
		t.Fatalf("idempotent close failed: %+v err=%v", closedAgain, err)
	}
}

func TestD1SessionListCursorPaginates(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	for i, name := range []string{"one", "two", "three"} {
		dir := filepath.Join(root, name)
		if err := ensureDir(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := rt.Call(context.Background(), "session_open", map[string]any{"project_id": "p" + string(rune('1'+i)), "repo_root": dir}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := rt.Call(context.Background(), "session_list", map[string]any{"limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	cursor, _ := first["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("expected next_cursor: %+v", first)
	}
	second, err := rt.Call(context.Background(), "session_list", map[string]any{"limit": 2, "cursor": cursor})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := second["sessions"].([]any)
	if len(items) != 1 || second["next_cursor"] != nil {
		t.Fatalf("unexpected second page: %+v", second)
	}
}

func TestSessionAwareSchemasPermitSessionRoutingWithoutWorkspace(t *testing.T) {
	for _, name := range []string{"opencode_read", "opencode_write"} {
		schema := InputSchema(name)
		props := schema["properties"].(map[string]any)
		if _, ok := props["session_id"]; !ok {
			t.Fatalf("%s missing session_id", name)
		}
		if requiredContains(schema, "workspace") {
			t.Fatalf("%s must not require workspace when session_id can route", name)
		}
		if !requiredContains(schema, "arguments") {
			t.Fatalf("%s must still require arguments", name)
		}
	}
	for _, name := range []string{"job_start", "job_status", "job_output", "job_cancel", "job_list"} {
		props := InputSchema(name)["properties"].(map[string]any)
		if _, ok := props["session_id"]; !ok {
			t.Fatalf("%s missing session_id ownership argument", name)
		}
	}
	if requiredContains(InputSchema("job_start"), "workspace") {
		t.Fatal("job_start must allow session_id routing without workspace")
	}
}

func TestSessionManagerLivesUnderAgentDockHome(t *testing.T) {
	rt, _ := newSessionToolsRuntime(t)
	if rt.sessions == nil {
		t.Fatal("session manager not initialized")
	}
	records := rt.sessions.List(sessionruntime.ListFilter{})
	if len(records) != 0 {
		t.Fatalf("new runtime unexpectedly recovered sessions: %+v", records)
	}
}

func containsSessionTool(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertRequiredFields(t *testing.T, schema map[string]any, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !requiredContains(schema, field) {
			t.Fatalf("schema missing required field %q: %+v", field, schema)
		}
	}
}

func requiredContains(schema map[string]any, want string) bool {
	values, _ := schema["required"].([]string)
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func ensureDir(path string) error { return os.MkdirAll(path, 0o700) }
