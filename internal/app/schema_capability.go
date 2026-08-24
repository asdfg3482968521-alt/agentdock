package app

import "github.com/uvwt/agentdock/internal/config"

// InputSchemaForConfig 返回当前配置下真正对模型可见的参数契约。
// task_manage 的本地任务能力始终存在，但 Nexus 专属扩展只在 Nexus 启用时公开。
func InputSchemaForConfig(name string, cfg config.Config) map[string]any {
	if _, ok := d2AgentSchemaFiles[name]; ok {
		return frozenAgentInputSchema(name)
	}
	schema := InputSchema(name)
	if name != "task_manage" || requiresNexus(cfg) {
		return schema
	}
	props, _ := schema["properties"].(map[string]any)
	delete(props, "template_id")
	delete(props, "source_template_ids")
	delete(props, "learning_checks")
	props["project"] = map[string]any{"type": "string", "description": "Optional project identifier stored with the task."}
	props["device"] = map[string]any{"type": "string", "description": "Optional device identifier stored with the task."}
	if steps, ok := props["steps"].(map[string]any); ok {
		steps["description"] = "Concrete task steps."
	}
	return schema
}

// OutputSchemaForConfig 与输入契约保持同一能力边界，避免无 Nexus 时泄露
// Guidance、candidate 或 evidence revision 等 Evolution 概念。
func OutputSchemaForConfig(name string, cfg config.Config) map[string]any {
	if _, ok := d2AgentSchemaFiles[name]; ok {
		return frozenAgentOutputSchema(name)
	}
	schema := OutputSchema(name)
	if name != "task_manage" || requiresNexus(cfg) {
		return schema
	}
	props, _ := schema["properties"].(map[string]any)
	for _, field := range []string{"guidance_context", "review_revision", "evolution_candidates", "evolution_warning"} {
		delete(props, field)
	}
	props["next_required_action"] = map[string]any{"type": "string", "description": "Concise guidance for checkpoint progress or final review."}
	return schema
}
