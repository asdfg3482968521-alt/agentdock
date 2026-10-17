package app

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
)

// server_info 的 outputSchema 把 trusted_proxy_cidrs、opencode_tools、
// opencode_runtimes、tools、startup_issues 声明为 array。严格校验 structuredContent
// 的客户端（如 Notion）会把 Go nil 切片序列化出的 null 判为校验失败，
// 因此这些字段为空时也必须序列化为 []。
func TestServerInfoSerializesArrayFieldsAsJSONArrays(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		AgentDockDefaultDir: root,
		AgentDockHome:       filepath.Join(root, ".agentdock"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	payload, err := json.Marshal(rt.serverInfo())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"trusted_proxy_cidrs", "opencode_tools", "opencode_runtimes", "tools", "startup_issues"} {
		if _, ok := decoded[field].([]any); !ok {
			t.Fatalf("server_info field %q must serialize as a JSON array, got %#v", field, decoded[field])
		}
	}
}
