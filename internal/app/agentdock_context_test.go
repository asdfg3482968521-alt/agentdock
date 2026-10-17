package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
)

func TestAgentDockContextToolReturnsRuntimeIndex(t *testing.T) {
	cfg := config.Config{
		AgentDockDefaultDir: t.TempDir(),
		AgentDockHome:       filepath.Join(t.TempDir(), ".agentdock"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	installDocumentSkillForTest(t, rt, "demo-skill", "1.0.0", "Use this Skill for context index tests.")

	result, err := rt.Call(context.Background(), "agentdock_context", map[string]any{})
	if err != nil {
		t.Fatalf("agentdock_context call failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("agentdock_context should return only context: %#v", result)
	}
	contextText, _ := result["context"].(string)
	if contextText == "" {
		t.Fatalf("agentdock_context returned empty context: %#v", result)
	}
	// Built-in tools come from MCP tools/list; context should not re-list them.
	if strings.Contains(contextText, "## AgentDock 工具索引") {
		t.Fatalf("context should not include built-in tool index: %s", contextText)
	}
	for _, want := range []string{
		"## Skill 能力索引",
		"demo-skill",
		"Use this Skill for context index tests.",
		"skill://demo-skill/SKILL.md",
		"AgentDock 自带工具直接调用",
		"mcp_session_bind",
		"openai/session",
		"## 使用规则",
	} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("context missing %q: %s", want, contextText)
		}
	}
	for _, removed := range []string{"skill_read", "skill_run"} {
		if strings.Contains(contextText, removed) {
			t.Fatalf("context still references removed model-facing tool %q: %s", removed, contextText)
		}
	}

	for _, name := range []string{"ok", "skills", "dynamic_mcp", "generated_at", "summary", "counts", "base_tools", "task_templates", "memory", "rules"} {
		if _, ok := result[name]; ok {
			t.Fatalf("agentdock_context exposed unexpected field %q", name)
		}
	}
}

func TestAgentDockContextExposesShortACPOrientationWhenEnabled(t *testing.T) {
	disabled := config.Config{
		AgentDockDefaultDir: t.TempDir(),
		AgentDockHome:       filepath.Join(t.TempDir(), ".agentdock"),
	}
	if err := disabled.Normalize(); err != nil {
		t.Fatal(err)
	}
	disabledRuntime, err := NewRuntime(disabled)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disabledRuntime.Close() })

	disabledResult, err := disabledRuntime.Call(context.Background(), "agentdock_context", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	disabledText := disabledResult["context"].(string)
	for _, hidden := range []string{"## ACP 运行时", "acp_session", "acp_prompt", "acp_interaction", "Agent Client Protocol"} {
		if strings.Contains(disabledText, hidden) {
			t.Fatalf("context should hide ACP while disabled: found %q in %s", hidden, disabledText)
		}
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	enabled := config.Config{
		AgentDockDefaultDir: root,
		AgentDockHome:       filepath.Join(t.TempDir(), ".agentdock"),
		ACPEnabled:          true,
		ACPAgentName:        "helper",
		ACPCommand:          executable,
	}
	if err := enabled.Normalize(); err != nil {
		t.Fatal(err)
	}
	enabledRuntime, err := NewRuntime(enabled)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = enabledRuntime.Close() })

	enabledResult, err := enabledRuntime.Call(context.Background(), "agentdock_context", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	enabledText := enabledResult["context"].(string)
	for _, want := range []string{
		"## ACP 运行时",
		"Agent Client Protocol",
		"仅当用户明确要求时使用",
		"独特见解",
		"编排任务",
		"不是动态 MCP",
		"mcp_tool_*",
		"- agent: helper",
	} {
		if !strings.Contains(enabledText, want) {
			t.Fatalf("ACP context missing %q: %s", want, enabledText)
		}
	}
	// 操作细节留给 acp_* 工具 description，rules 不再复述会话/事件/权限契约。
	for _, operational := range []string{
		"acp_session",
		"acp_prompt",
		"acp_interaction",
		"option_id",
		"always",
	} {
		if strings.Contains(enabledText, operational) {
			t.Fatalf("ACP context should leave %q to tool descriptions: %s", operational, enabledText)
		}
	}
	// section 已说明何时用 ACP；rules 不再重复选型话术。
	// bootstrap 只做短取向，不展开协议百科或 adapter 目录。
	for _, longForm := range []string{
		"需要托管本地 Coding Agent 时走 ACP",
		"按主机配置拉起 adapter",
		"多轮编码或排障走当前 agent",
		"只在需要子 Agent 独立完成一段编码任务时使用",
		"不要用它编排主任务",
		"Claude / Codex / Grok",
		"JSON-RPC",
		"wire protocol",
	} {
		if strings.Contains(enabledText, longForm) {
			t.Fatalf("ACP context should stay short, found %q: %s", longForm, enabledText)
		}
	}
}

func TestNexusUnavailableHidesWorkflowTemplateCapability(t *testing.T) {
	cfg := config.Config{
		AgentDockDefaultDir: t.TempDir(),
		AgentDockHome:       filepath.Join(t.TempDir(), ".agentdock"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}

	toolNames := strings.Join(rt.ToolNames(), "\n")
	for _, hiddenTool := range []string{"workflow_template_manage", "evolve", "recall_bootstrap", "recall_search", "recall_read", "recall_write", "recall_maintain"} {
		if strings.Contains(toolNames, hiddenTool) {
			t.Fatalf("%s should be hidden without Nexus: %s", hiddenTool, toolNames)
		}
	}
	if !strings.Contains(toolNames, "task_manage") {
		t.Fatalf("task_manage should remain available without Nexus: %s", toolNames)
	}

	if _, err := rt.Call(context.Background(), "workflow_template_manage", map[string]any{"action": "list"}); err == nil {
		t.Fatal("workflow_template_manage call should be unavailable without Nexus")
	} else if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "UNKNOWN_TOOL" {
		t.Fatalf("workflow_template_manage error = %#v, want UNKNOWN_TOOL", err)
	}

	result, err := rt.Call(context.Background(), "agentdock_context", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	contextText := result["context"].(string)
	for _, hidden := range []string{
		"任务模板", "workflow_template_manage", "source_template_ids",
		"记忆精简摘要", "recall_search", "recall_read", "NexusDock Recall 未配置",
		"Evolution", "自进化",
	} {
		if strings.Contains(contextText, hidden) {
			t.Fatalf("context should hide %q without Nexus: %s", hidden, contextText)
		}
	}
	if !strings.Contains(contextText, "task_manage") {
		t.Fatalf("context should keep task_manage without Nexus: %s", contextText)
	}
}

func TestNexusAvailableExposesWorkflowTemplateCapability(t *testing.T) {
	rt, _ := newCodeToolsRuntime(t)

	toolNames := strings.Join(rt.ToolNames(), "\n")
	if !strings.Contains(toolNames, "workflow_template_manage") {
		t.Fatalf("workflow_template_manage should be available with Nexus: %s", toolNames)
	}

	result, err := rt.Call(context.Background(), "agentdock_context", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	contextText := result["context"].(string)
	for _, want := range []string{"## 任务模板索引", "workflow_template_manage match", "source_template_ids"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("context missing %q with Nexus: %s", want, contextText)
		}
	}
}

func TestCapabilitySkillItemExposesOnlyLightweightIndexFields(t *testing.T) {
	data, err := json.Marshal(capabilitySkillItem{
		Name:        "desktop",
		Description: "Desktop automation.",
		File:        "skill://desktop/SKILL.md",
		Bundled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"name"`, `"description"`, `"file"`, `"bundled"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("Skill index JSON missing %s: %s", want, text)
		}
	}
	for _, unwanted := range []string{`"active_version"`, `"updated_at"`, `"operation_count"`, `"version"`, `"path"`, `"manifest"`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("Skill index JSON should not expose %s: %s", unwanted, text)
		}
	}
}

func installDocumentSkillForTest(t *testing.T, rt *Runtime, name, version, description string) string {
	t.Helper()
	packageDir, err := rt.skills.InstalledPath(name, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: " + name + "\ndescription: " + description + "\nversion: " + version + "\n---\n\n# Test Skill\n\nFollow the workflow.\n"
	if err := os.WriteFile(filepath.Join(packageDir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rt.skills.Activate(context.Background(), name, version); err != nil {
		t.Fatal(err)
	}
	return packageDir
}

func TestSkillCapabilityIndexOmitsLegacyExecutableSkills(t *testing.T) {
	cfg := config.Config{
		AgentDockDefaultDir: t.TempDir(),
		AgentDockHome:       filepath.Join(t.TempDir(), ".agentdock"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	installDocumentSkillForTest(t, rt, "document-skill", "1.0.0", "A document-only Skill.")

	legacyDir, err := rt.skills.InstalledPath("legacy-skill", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDoc := "---\nname: legacy-skill\ndescription: A legacy executable Skill.\n---\n\n# Legacy\n"
	if err := os.WriteFile(filepath.Join(legacyDir, "SKILL.md"), []byte(legacyDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `apiVersion: agentdock.dev/v1
kind: Skill
metadata:
  name: legacy-skill
  version: 1.0.0
  displayName: Legacy Skill
  description: A legacy executable Skill.
spec:
  entrypoint: run.sh
  operations:
    - name: run
      description: Run the legacy entrypoint.
      inputSchema: {"type":"object","additionalProperties":false}
      outputSchema: {"type":"object","additionalProperties":true}
      timeoutSeconds: 5
  compatibility:
    platforms: [darwin]
    architectures: [arm64]
    agentdock: ">=1.0.0"
  permissions:
    filesystem: []
    network: []
    commands: []
`
	if err := os.WriteFile(filepath.Join(legacyDir, "agentdock.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "run.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := rt.skills.Activate(context.Background(), "legacy-skill", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	items, _, errText := rt.skillCapabilityIndex()
	if errText != "" {
		t.Fatalf("skillCapabilityIndex error = %s", errText)
	}
	if len(items) != 1 || items[0].Name != "document-skill" {
		t.Fatalf("legacy executable Skill should be omitted from model index: %#v", items)
	}
}
