# Native subagent model policy

AgentDock's `opencode-native` backend resolves an omitted or `default` model to `jiyuan/deepseek-flash#max`. Explicit `provider/model#variant` selections remain unchanged.

Configure the connected OpenCode runtime with the companion [Flash configuration](examples/jiyuan-deepseek-flash.jsonc). The default and small model are both `jiyuan/deepseek-flash`; model options default to maximum reasoning. The configured reasoning variants are exactly `low`, `high`, and `max`, with `max` as the default. The configured input modalities are text and image, and the context limit is 1,048,576 tokens. These are configuration declarations; verify upstream availability and image behavior with a bounded request.

Keep only `deepseek-flash` in the normal Jiyuan model catalog. Do not alias another upstream model under this ID or automatically fall back to Pro, GLM or another provider.

When a user explicitly selects another model, first confirm that model's real upstream ID, capabilities and supported variants. Register only that selected model in a task-scoped OpenCode configuration, pass the explicit route for that task, and keep the global Flash default unchanged. Unsupported selections must be reported before switching models.
