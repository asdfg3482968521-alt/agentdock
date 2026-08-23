package app

import (
	toolcommand "github.com/uvwt/agentdock/internal/tool/command"
	toolfile "github.com/uvwt/agentdock/internal/tool/file"
	toolrecall "github.com/uvwt/agentdock/internal/tool/recall"
)

func InputSchema(name string) map[string]any {
	props := map[string]any{}
	required := []string{}
	stringProp := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	intProp := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boundedIntProp := func(desc string, minimum, maximum int) map[string]any {
		return map[string]any{"type": "integer", "description": desc, "minimum": minimum, "maximum": maximum}
	}
	boolProp := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	objectProp := func(desc string) map[string]any {
		return map[string]any{"type": "object", "description": desc, "additionalProperties": true}
	}

	switch name {
	case "opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit", "opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git":
		props["workspace"] = stringProp("Absolute or relative existing directory bound to this OpenCode runtime. The runtime cannot widen this workspace during a tool call.")
		props["permission_mode"] = map[string]any{"type": "string", "description": "OpenCode filesystem permission mode. Defaults to project; full must be requested explicitly.", "enum": []string{"project", "full"}}
		props["arguments"] = objectProp("Arguments forwarded unchanged to the underlying verified OpenCode tool.")
		required = []string{"workspace", "arguments"}
	case "job_start":
		props["workspace"] = stringProp("Existing workspace for the persistent OpenCode job.")
		props["tool"] = map[string]any{"type": "string", "description": "Verified OpenCode tool executed by this job.", "enum": []string{"bash", "read", "glob", "grep", "edit", "write", "apply_patch", "lsp", "git"}}
		props["arguments"] = objectProp("Arguments forwarded unchanged to the underlying OpenCode tool. Persisted summaries are redacted separately.")
		props["permission_mode"] = map[string]any{"type": "string", "enum": []string{"project", "full"}, "description": "OpenCode permission mode. Defaults to project."}
		props["timeout"] = boundedIntProp("Optional job timeout in milliseconds. Timeout cancellation must fully stop the runtime request before TIMED_OUT.", 0, 86400000)
		props["metadata"] = objectProp("Optional caller metadata. Persisted and displayed metadata is secret-redacted.")
		props["concurrency_class"] = map[string]any{"type": "string", "enum": []string{"read", "write", "destructive"}, "description": "Optional concurrency classification. Defaults from the selected tool."}
		required = []string{"workspace", "tool", "arguments"}
	case "job_status":
		props["job_id"] = stringProp("Persistent job identifier returned by job_start.")
		required = []string{"job_id"}
	case "job_output":
		props["job_id"] = stringProp("Persistent job identifier returned by job_start.")
		props["cursor"] = map[string]any{"type": "integer", "minimum": 0, "description": "Stable output cursor. Defaults to 0."}
		props["limit"] = boundedIntProp("Maximum output bytes returned by this request. Defaults to the configured response cap.", 0, 262144)
		required = []string{"job_id"}
	case "job_cancel":
		props["job_id"] = stringProp("Persistent job identifier returned by job_start.")
		props["reason"] = stringProp("Optional cancellation reason. Persisted text is secret-redacted.")
		required = []string{"job_id"}
	case "job_list":
		props["state"] = map[string]any{"type": "string", "enum": []string{"QUEUED", "STARTING", "RUNNING", "SUCCEEDED", "FAILED", "CANCEL_REQUESTED", "CANCELLED", "TIMED_OUT", "INTERRUPTED"}}
		props["workspace"] = stringProp("Optional workspace filter.")
		props["limit"] = boundedIntProp("Maximum jobs to return. Defaults to 50 and is capped at 200.", 1, 200)
	case "read_file":
		props["path"] = stringProp(toolfile.PathDescription("Host path. Relative paths resolve from ~/AgentDock."))
		toolfile.AddRuntimeProperties(props)
		props["start_line"] = intProp("1-based start line.")
		props["end_line"] = intProp("Inclusive end line.")
		props["max_bytes"] = boundedIntProp("Maximum output bytes. Defaults to 262144 and is capped at 4194304.", 1, toolfile.MaxTextOutputBytes)
		required = []string{"path"}
	case "agentdock_context":

	case "list_dir":
		props["path"] = stringProp(toolfile.PathDescription("Host directory path. Relative paths resolve from ~/AgentDock."))
		toolfile.AddRuntimeProperties(props)
		props["recursive"] = boolProp("List recursively.")
		props["max_depth"] = boundedIntProp("Maximum recursive depth. Defaults to 1 and is capped at 20.", 1, 20)
		props["max_entries"] = boundedIntProp("Maximum entries. Defaults to 200 and is capped at 2000.", 1, 2000)
		props["include_hidden"] = boolProp("Include dotfiles.")
		props["include_ignored"] = boolProp("Include normally skipped directories.")
	case "list_files":
		props["path"] = stringProp(toolfile.PathDescription("Host directory path. Relative paths resolve from ~/AgentDock."))
		toolfile.AddRuntimeProperties(props)
		props["patterns"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["glob"] = stringProp("Single glob pattern override.")
		props["exclude_patterns"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["max_results"] = boundedIntProp("Maximum files. Defaults to 500 and is capped at 5000.", 1, 5000)
		props["include_hidden"] = boolProp("Include dotfiles.")
		props["include_ignored"] = boolProp("Include normally skipped directories.")
	case "search_text":
		props["path"] = stringProp(toolfile.PathDescription("Host path. Relative paths resolve from ~/AgentDock."))
		toolfile.AddRuntimeProperties(props)
		props["query"] = stringProp("Text or regex query.")
		props["regex"] = boolProp("Treat query as regex.")
		props["case_sensitive"] = boolProp("Use case-sensitive search.")
		props["include_hidden"] = boolProp("Include hidden files and directories.")
		props["include_globs"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["glob"] = stringProp("Single include glob.")
		props["exclude_globs"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["context_lines"] = boundedIntProp("Context lines around each match. Capped at 20.", 0, 20)
		props["max_results"] = boundedIntProp("Maximum matches. Defaults to 100 and is capped at 1000.", 1, 1000)
		required = []string{"query"}
	case "file_edit":
		props["action"] = map[string]any{"type": "string", "description": "File edit action.", "enum": []string{"replace", "patch", "add", "delete", "move"}}
		props["path"] = stringProp(toolfile.PathDescription("Host path for replace, add, delete, or move. Relative paths resolve from ~/AgentDock."))
		toolfile.AddRuntimeProperties(props)
		props["old"] = stringProp("Exact UTF-8 text to replace.")
		props["new"] = stringProp("Replacement UTF-8 text for action=replace.")
		props["replace_all"] = boolProp("Replace every match instead of only the first.")
		props["expected_matches"] = map[string]any{"type": "integer", "description": "Required number of matches. Defaults to 1; zero asserts no matches.", "minimum": 0}
		props["content"] = stringProp("Text content for action=add.")
		props["new_path"] = stringProp(toolfile.PathDescription("Destination path for action=move."))
		props["overwrite"] = boolProp("Allow add or move to replace an existing destination file.")
		props["recursive"] = boolProp("Required for deleting directories.")
		props["patch"] = stringProp(toolfile.PatchDescription("Patch text for action=patch."))
		props["workdir"] = stringProp(toolfile.PathDescription("Patch working directory."))
		props["dry_run"] = boolProp("Preview or validate without writing.")
		props["max_diff_bytes"] = boundedIntProp("Maximum diff preview bytes. Defaults to 65536 and is capped at 4194304.", 1, toolfile.MaxTextOutputBytes)
		required = []string{"action"}

	case "exec_command":
		props["cmd"] = stringProp("Command to run.")
		props["workdir"] = stringProp(toolcommand.WorkdirDescription())
		toolcommand.AddRuntimeProperties(props)
		props["skill"] = stringProp("Optional active Skill context. When workdir is omitted, the command runs from the active installed Skill root and loads that Skill isolated environment.")
		props["skill_env"] = stringProp("Optional Skill name whose isolated environment is loaded without changing workdir. Kept for environment-only compatibility.")
		props["env"] = map[string]any{"type": "object", "description": "Explicit command environment values. These override the selected Skill environment.", "additionalProperties": map[string]any{"type": "string"}}
		props["timeout_ms"] = boundedIntProp("Timeout in milliseconds. Must be positive and is capped at 86400000.", 1, 86400000)
		props["execution_mode"] = map[string]any{"type": "string", "description": "Execution mode. Defaults to auto: wait up to yield_time_ms, then return a running session. sync waits for exit; async returns a session immediately.", "enum": []string{"auto", "sync", "async"}}
		props["yield_time_ms"] = boundedIntProp("Foreground wait threshold for execution_mode=auto. Defaults to 5000 and is capped at 30000 milliseconds.", 0, 30000)
		props["max_output_bytes"] = boundedIntProp("Maximum output bytes. Defaults to 65536 and is capped at 4194304.", 1, toolcommand.MaxOutputBytes)
		props["stdin"] = stringProp("Initial stdin.")
		props["tty"] = boolProp("Keep stdin open.")
		required = []string{"cmd"}
	case "session_observe":
		props["action"] = map[string]any{"type": "string", "description": "Read-only session action.", "enum": []string{"list", "status"}}
		props["session_id"] = stringProp("Session id returned by exec_command, required for status.")
		props["max_output_bytes"] = boundedIntProp("Maximum output bytes. Defaults to 65536 and is capped at 4194304.", 1, toolcommand.MaxOutputBytes)
	case "session_act":
		props["action"] = map[string]any{"type": "string", "description": "Mutating session action.", "enum": []string{"write", "kill", "kill_all"}}
		props["session_id"] = stringProp("Session id returned by exec_command, required for write/kill.")
		props["chars"] = stringProp("Characters to write when action=write.")
		props["max_output_bytes"] = boundedIntProp("Maximum output bytes. Defaults to 65536 and is capped at 4194304.", 1, toolcommand.MaxOutputBytes)
	case "task_manage":
		props["action"] = map[string]any{"type": "string", "description": "Task lifecycle action. Use checkpoint to update live step progress.", "enum": []string{"create", "list", "get", "checkpoint", "block", "resume", "final_review", "complete"}}
		props["task_id"] = stringProp("Persistent task id for get, checkpoint, block, resume, final_review, or complete.")
		props["title"] = stringProp("Short task title for create.")
		props["goal"] = stringProp("Fixed task goal for create.")
		props["project"] = stringProp("Optional project identifier used to hard-scope Evolution guidance and evidence candidates. Omit only for global tasks.")
		props["device"] = stringProp("Optional device identifier used to hard-scope device-specific Evolution guidance and evidence candidates.")
		props["completion_conditions"] = map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}, "description": "Conditions that must be true before final_review can pass."}
		props["steps"] = map[string]any{
			"type": "array", "maxItems": 12, "description": "Concrete task steps. Required when composing multiple source templates.",
			"items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "title"}, "properties": map[string]any{"id": stringProp("Stable step id."), "title": stringProp("Human-readable step title.")}},
		}
		props["template_id"] = stringProp("Single active workflow template to apply. Its current active version is resolved automatically.")
		props["source_template_ids"] = map[string]any{"type": "array", "minItems": 2, "maxItems": 3, "items": map[string]any{"type": "string"}, "description": "Two or three templates already composed by the model into steps and completion_conditions."}
		props["learning_checks"] = map[string]any{
			"type": "array", "maxItems": 3, "description": "Advanced create-only blinded validation checks. Bind these Evolution ids before Guidance is generated; support-bearing targets are withheld from this Task's Guidance and may be assessed only from its frozen final_review.",
			"items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"evolution_id", "on_success", "on_failure"},
				"properties": map[string]any{
					"evolution_id": stringProp("Evolution id intentionally selected for this pre-execution validation."),
					"on_success":   map[string]any{"type": "string", "enum": []string{"support", "contradict", "none"}},
					"on_failure":   map[string]any{"type": "string", "enum": []string{"support", "contradict", "none"}},
				},
			},
		}
		props["step_id"] = stringProp("Task step id for a single-step checkpoint.")
		props["completed_step_ids"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 12, "uniqueItems": true, "items": map[string]any{"type": "string"}, "description": "Task step ids to mark completed in one atomic batch checkpoint."}
		props["current_step_id"] = stringProp("Single task step id to mark in_progress in a batch checkpoint.")
		props["status"] = map[string]any{"type": "string", "description": "Action-specific status: task list filter, single-step checkpoint status, or final review status.", "enum": []string{"active", "blocked", "completed", "pending", "in_progress", "pass", "failed"}}
		props["limit"] = intProp("Maximum tasks returned by list. Defaults to 50 and is capped at 200.")
		props["summary"] = stringProp("Current progress, blocker, resume, or final review summary.")
		props["verified"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Facts verified during final_review. Required when status=pass."}
		props["risks"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Remaining risks. Required when final_review status=failed."}
		required = []string{"action"}

	case "evolve":
		props["intent"] = map[string]any{"type": "string", "description": "Evolution intent.", "enum": []string{"propose", "bind", "supersede", "retract"}}
		props["candidate"] = map[string]any{
			"type": "object", "description": "Candidate knowledge for propose. Lifecycle state and evidence counts are intentionally not accepted.", "additionalProperties": false,
			"properties": map[string]any{
				"type": map[string]any{
					"type":        "string",
					"description": "Knowledge type accepted by the Evolution policy.",
					"enum": []string{
						"preference", "user_preference", "decision", "explicit_decision", "constraint",
						"runbook", "bug_pattern", "deploy_note", "project_trap", "architecture", "anti_pattern",
						"operational_lesson", "technical_fact", "workflow_template", "skill",
					},
				},
				"statement":     stringProp("Bounded reusable statement to learn."),
				"scope":         stringProp("Knowledge scope such as user, shared, project or device."),
				"project":       stringProp("Project identifier when scope is project."),
				"device":        stringProp("Device identifier when scope is device."),
				"canonical_key": stringProp("Optional stable exact-deduplication key."),
				"source":        stringProp("Short provenance label; never include hidden prompts, secrets, or raw conversation transcripts."),
				"tags":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"type", "statement"},
		}
		props["evolution_id"] = stringProp("Stable evolution id for bind, supersede or retract.")
		props["task_id"] = stringProp("Task id for bind. The learning check must be bound before Task execution begins.")
		props["learning_check"] = map[string]any{
			"type": "object", "description": "Required for bind. Predeclare what a later Task pass or failure means before execution; Task outcome has no learning meaning by itself.", "additionalProperties": false,
			"properties": map[string]any{
				"on_success": map[string]any{"type": "string", "enum": []string{"support", "contradict", "none"}},
				"on_failure": map[string]any{"type": "string", "enum": []string{"support", "contradict", "none"}},
			},
			"required": []string{"on_success", "on_failure"},
		}
		props["superseded_by"] = stringProp("Replacement evolution_id for supersede when already known.")
		required = []string{"intent"}

	case "acp_session":
		props["action"] = map[string]any{"type": "string", "description": "ACP session action.", "enum": []string{"info", "authenticate", "new", "load", "resume", "fork", "set_mode", "set_config", "list", "inspect", "close", "delete"}}
		props["auth_method_id"] = stringProp("Authentication method id advertised by initialize, required for authenticate.")
		props["session_id"] = stringProp("AgentDock ACP session id for load, resume, fork, set_mode, set_config, inspect, close, or delete.")
		props["cwd"] = stringProp("Working directory for new or fork. Relative paths resolve from AgentDock's default directory; absolute paths may use any host-accessible directory.")
		props["additional_directories"] = map[string]any{"type": "array", "maxItems": 16, "uniqueItems": true, "items": map[string]any{"type": "string"}, "description": "Additional workspace directories for new or fork. Each path must resolve to a host-accessible directory."}
		props["mode_id"] = stringProp("Agent-advertised session mode id for set_mode.")
		props["config_id"] = stringProp("Agent-advertised session configuration option id for set_config.")
		props["config_value"] = map[string]any{"description": "String value id or boolean value for set_config.", "oneOf": []map[string]any{{"type": "string"}, {"type": "boolean"}}}
		required = []string{"action"}

	case "acp_prompt":
		props["action"] = map[string]any{"type": "string", "description": "ACP prompt action.", "enum": []string{"start", "events", "steer", "cancel"}}
		props["session_id"] = stringProp("AgentDock ACP session id for start, steer, or cancel.")
		props["run_id"] = stringProp("ACP prompt run id for events or cancel.")
		props["text"] = stringProp("Prompt text for start or steer. Capped at 256 KiB for start.")
		props["after_seq"] = map[string]any{"type": "integer", "description": "Return events with seq greater than this value. Defaults to 0. Pass next_seq unchanged; values newer than latest_seq are rejected to prevent cursor poisoning.", "minimum": 0}
		props["limit"] = boundedIntProp("Maximum events to return. Defaults to 100 and is capped at 200.", 1, 200)
		props["wait_ms"] = boundedIntProp("Bounded long-poll duration for events. Defaults to 0 and is capped at 25000 milliseconds.", 0, 25000)
		required = []string{"action"}

	case "acp_interaction":
		props["action"] = map[string]any{"type": "string", "description": "ACP interaction action.", "enum": []string{"list", "inspect", "respond", "cancel"}}
		props["session_id"] = stringProp("Optional ACP session filter for list.")
		props["interaction_id"] = stringProp("Pending ACP interaction id for inspect, respond, or cancel.")
		props["option_id"] = stringProp("Permission option id for respond. It must be currently offered and permitted by local policy.")
		props["pending_only"] = boolProp("Return only pending interactions for list. Defaults to true.")
		required = []string{"action"}

	case "workflow_template_manage":
		props["action"] = map[string]any{"type": "string", "description": "Workflow template action. get_many returns full active templates that the model must compose before task creation.", "enum": []string{"save", "validate", "publish", "retire", "list", "get", "get_many", "match", "vector_index"}}
		props["template"] = map[string]any{"type": "object", "additionalProperties": true, "description": "Complete draft workflow template for save."}
		props["template_id"] = stringProp("Workflow template id.")
		props["template_ids"] = map[string]any{"type": "array", "minItems": 2, "maxItems": 3, "items": map[string]any{"type": "string"}, "description": "Two or three active template ids for get_many. The returned templates must be pruned, deduplicated, ordered, and combined by the model."}
		props["template_version"] = stringProp("Workflow template version for exact get, validate, publish, or retire actions. Omit it for get to resolve the current active version.")
		props["template_status"] = map[string]any{"type": "string", "enum": []string{"draft", "active", "retired"}, "description": "Optional list status filter."}
		props["allow_long_template"] = boolProp("Allow a workflow template to exceed default guardrails. Provide long_template_reason when true.")
		props["long_template_reason"] = stringProp("Reason required when allow_long_template=true.")
		props["goal"] = stringProp("Goal text for match.")
		props["device"] = stringProp("Optional device hint for match.")
		props["type"] = stringProp("Optional workflow type hint for match. This maps to template match.type.")
		required = []string{"action"}

	case "skill_package":
		props["action"] = map[string]any{"type": "string", "description": "Skill package or isolated environment action.", "enum": []string{"validate", "install", "activate", "rollback", "env_set", "env_unset", "env_list"}}
		props["skill"] = stringProp("Skill name for activate, rollback, or environment management.")
		props["version"] = stringProp("Installed Skill version for activate.")
		props["key"] = stringProp("Environment variable name for env_set/env_unset.")
		props["value"] = stringProp("Environment variable value for env_set. Secret values are never returned.")
		props["source"] = stringProp("Host path or HTTP(S) URL for validate/install.")
		props["digest"] = stringProp("Optional expected SHA-256 digest for validate/install.")
		props["activate"] = boolProp("Activate the installed version. Defaults to true.")
		props["max_bytes"] = intProp("Maximum validate/install package bytes.")
		required = []string{"action"}
	case "mcp_manage":
		props["action"] = map[string]any{"type": "string", "description": "Dynamic MCP server or isolated environment action.", "enum": []string{"list", "inspect", "add", "remove", "enable", "disable", "env_set", "env_unset", "env_list", "refresh"}}
		props["name"] = stringProp("Dynamic MCP server name. Use a stable short identifier such as figma or github.")
		props["description"] = stringProp("Short capability description shown in agentdock_context.")
		props["transport"] = map[string]any{"type": "string", "description": "MCP transport for action=add.", "enum": []string{"streamable_http", "stdio"}}
		props["url"] = stringProp("Absolute MCP endpoint URL for transport=streamable_http.")
		props["command"] = stringProp("Executable name or path for transport=stdio.")
		props["args"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Command arguments for transport=stdio."}
		props["cwd"] = stringProp("Optional absolute working directory for transport=stdio.")
		props["header_env"] = map[string]any{"type": "object", "description": "HTTP header name to host environment variable name. Secret values are never stored in the MCP registry.", "additionalProperties": map[string]any{"type": "string"}}
		props["env_from_env"] = map[string]any{"type": "object", "description": "Child process environment variable name to host environment variable name for stdio. Secret values are never stored in the MCP registry.", "additionalProperties": map[string]any{"type": "string"}}
		props["key"] = stringProp("Environment variable name for env_set/env_unset.")
		props["value"] = stringProp("Environment variable value for env_set. Secret values are never returned.")
		props["enabled"] = boolProp("Enable the server after registration. Defaults to true.")
		props["timeout_ms"] = boundedIntProp("Per-request timeout. Defaults to 30000 and is capped at 300000.", 1, 300000)
		required = []string{"action"}
	case "mcp_tool_search":
		props["query"] = stringProp("Capability or tool query.")
		props["server"] = stringProp("Optional dynamic MCP server name from agentdock_context.")
		props["limit"] = boundedIntProp("Maximum matching tools. Defaults to 10 and is capped at 100.", 1, 100)
		required = []string{"query"}
	case "mcp_tool_inspect":
		props["name"] = stringProp("Qualified dynamic MCP tool name in <server>:<tool> form.")
		required = []string{"name"}
	case "mcp_tool_call":
		props["name"] = stringProp("Qualified dynamic MCP tool name in <server>:<tool> form.")
		props["arguments"] = map[string]any{"type": "object", "description": "Arguments matching the schema returned by mcp_tool_inspect.", "additionalProperties": true}
		required = []string{"name", "arguments"}
	case "file_publish":
		props["file"] = map[string]any{"type": "string", "format": "binary", "description": "Top-level file parameter. Connector runtimes should pass the mounted local path when available."}
		props["path"] = stringProp("Local file or directory path visible to this AgentDock instance. Relative paths resolve from ~/AgentDock.")
		props["retention_seconds"] = intProp("Signed URL retention in seconds. Defaults to 86400 and is capped at 604800.")
		required = []string{}
	case "recall_bootstrap":
		props["max_bytes"] = intProp("Maximum combined NexusDock Recall pack bytes. Does not expose section bodies by itself; use include_body or recall_read when body text is needed.")
		props["include_raw"] = boolProp("Include raw Markdown as raw_content. Defaults to false to avoid duplicating body/content tokens.")
		props["include_body"] = boolProp("Include section body text in recall_bootstrap. Defaults to false; prefer recall_read for targeted full text.")
	case "recall_search":
		props["query"] = stringProp("Text query to search in NexusDock Recall files and paths.")
		props["kind"] = map[string]any{"type": "string", "description": "Search kind. Defaults to all.", "enum": []string{"all", "markdown", "card"}}
		props["max_results"] = intProp("Maximum results to return.")
		required = []string{"query"}
	case "recall_read":
		props["path"] = stringProp("NexusDock Recall-relative Markdown or card path.")
		props["include_raw"] = boolProp("Include raw Markdown as raw_content. Defaults to false to avoid duplicating body/content tokens.")
		required = []string{"path"}
	case "recall_write":
		props["target"] = map[string]any{"type": "string", "description": "Recall target selected by the model.", "enum": []string{"card", "markdown"}}
		props["action"] = map[string]any{"type": "string", "description": "Recall action selected by the model.", "enum": []string{"plan", "create", "replace", "append", "patch", "update_fact", "diff", "delete"}}
		props["confirmed"] = boolProp("Required for true writes/deletes. card create with confirmed=false returns a review plan.")
		props["path"] = stringProp("NexusDock Recall-relative path when reading, updating, deleting, or writing a known entry.")
		props["content"] = stringProp("Card or Markdown content, or proposed replacement content.")
		props["title"] = stringProp("Short title for a card or Markdown entry.")
		props["summary"] = stringProp("Short summary for a card.")
		props["overwrite"] = boolProp("Replace an existing entry when supported.")
		props["allow_warnings"] = boolProp("Card only: after reviewing warnings, allow writing a warned card. Do not use by default.")
		props["old"] = stringProp("Patch only: literal text to replace.")
		props["new"] = stringProp("Patch only: replacement text for old.")
		props["append"] = stringProp("Append/patch only: text to append to the recall document.")
		props["section"] = stringProp("Patch/update_fact only: Markdown heading title whose section should be updated.")
		props["section_content"] = stringProp("Patch only: new body for the selected Markdown section.")
		props["key"] = stringProp("Update_fact only: fact key to update.")
		props["value"] = stringProp("Update_fact only: new fact value.")
		props["facts"] = objectProp("Update_fact only: multiple key/value facts to update.")
		props["append_if_missing"] = boolProp("Update_fact only: append missing keys to the selected section or document instead of failing.")
		props["max_bytes"] = intProp("Maximum diff/output bytes.")
		required = []string{"target", "action"}
	case "recall_maintain":
		props["action"] = map[string]any{"type": "string", "description": "Maintenance action.", "enum": []string{"sync_status", "list", "lint", "embedding_status", "reindex", "reindex_cards"}}
		props["prefix"] = stringProp("Optional NexusDock Recall-relative prefix.")
		props["terms"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Terms or regex patterns for lint."}
		props["regex"] = boolProp("Treat terms as regex patterns for lint.")
		props["max_entries"] = intProp("Maximum entries to list or scan.")
		props["max_findings"] = intProp("Maximum lint findings to return.")
		props["max_results"] = intProp("Maximum results where supported.")
	case "private_note_manage":
		props["action"] = map[string]any{"type": "string", "description": "NexusDock private note action. Do not use by default; use only for explicit private note access or clearly sensitive secrets, credentials, or personal information.", "enum": []string{"search", "read", "write", "delete", "status", "maintain"}}
		props["query"] = stringProp("Metadata-only query for action=search. Matches title, summary, tags, category, and path; never searches plaintext body.")
		props["max_results"] = boundedIntProp("Maximum metadata search results to return. Defaults to 8 and is capped at 100.", 1, toolrecall.MaxPrivateNoteSearchResults)
		props["path"] = stringProp("Path under notes/ for action=read, action=write, or action=delete.")
		props["category"] = stringProp("Optional category used with title when path is omitted. Defaults to services.")
		props["title"] = stringProp("Title used for frontmatter or to derive the path when path is omitted.")
		props["summary"] = stringProp("Optional human-maintained safe summary for metadata-only search.")
		props["tags"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional safe tags for metadata-only search."}
		props["content"] = stringProp("Plaintext private note content for action=write.")
		props["confirmed"] = boolProp("Required for true action=write and action=delete mutations.")
		props["overwrite"] = boolProp("Replace an existing note for action=write.")
		props["max_bytes"] = boundedIntProp("Maximum bytes to return for explicit action=read. Defaults to 256000 and is capped at 1048576.", 1, toolrecall.MaxPrivateNoteReadBytes)
		props["status_action"] = map[string]any{"type": "string", "description": "Read-only status action when action=status.", "enum": []string{"check", "list"}}
		props["maintenance_action"] = map[string]any{"type": "string", "description": "NexusDock encryption maintenance operation when action=maintain.", "enum": []string{"init", "init-encryption", "sync-encrypted", "encrypt-all"}}
		required = []string{"action"}
	case "browser_session":
		props["action"] = map[string]any{"type": "string", "description": "Browser session action.", "enum": []string{"start", "close", "cleanup_stale"}}
		props["url"] = stringProp("Initial URL for action=start. Defaults to about:blank in the AgentDock-managed target.")
		props["browser"] = map[string]any{"type": "string", "description": "Chromium-family browser to launch. Defaults to auto.", "enum": []string{"auto", "chrome", "chromium", "edge"}}
		props["headless"] = boolProp("Run the AgentDock-owned browser headless. Defaults to true.")
		props["viewport"] = map[string]any{
			"type": "object", "description": "Viewport for action=start.", "additionalProperties": false,
			"properties": map[string]any{
				"width":  boundedIntProp("Viewport width in CSS pixels.", 320, 7680),
				"height": boundedIntProp("Viewport height in CSS pixels.", 200, 4320),
			},
		}
		props["session_id"] = stringProp("In-memory browser session id for action=close.")
		props["profile_id"] = stringProp("Optional persistent profile id stored under ~/.agentdock/browser/profiles/<id>; not valid with an external CDP browser.")
		props["cdp_url"] = stringProp("Optional loopback Chromium CDP endpoint (http(s) root or ws(s) browser websocket). Remote CDP endpoints must be configured by the user through AgentDock settings. When set, AgentDock attaches and manages only its own dedicated target instead of launching a browser.")
		props["cookies"] = map[string]any{
			"type": "array", "description": "Cookies injected when action=start.",
			"items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"name", "value"},
				"properties": map[string]any{
					"name": stringProp("Cookie name."), "value": stringProp("Cookie value."),
					"url": stringProp("URL used to scope the cookie."), "domain": stringProp("Cookie domain; provide url or domain."),
					"path": stringProp("Cookie path."), "expires": map[string]any{"type": "number", "minimum": 0},
					"http_only": boolProp("Set HttpOnly."), "secure": boolProp("Set Secure."),
					"same_site": map[string]any{"type": "string", "enum": []string{"strict", "lax", "none"}},
				},
			},
		}
		props["local_storage"] = map[string]any{
			"type": "object", "description": "Origin to string key/value localStorage map.",
			"additionalProperties": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		}
		props["reload_after_local_storage"] = boolProp("Reload the final URL after localStorage injection. Defaults to true.")
		props["max_age_ms"] = map[string]any{"type": "integer", "description": "For cleanup_stale, remove current-process sessions inactive for this age. Defaults to 6 hours.", "minimum": 1, "maximum": 31536000000}
		props["timeout_ms"] = boundedIntProp("Operation timeout in milliseconds. Defaults to 30000 and is capped at 300000.", 1, 300000)
		required = []string{"action"}
	case "browser_act":
		props["session_id"] = stringProp("In-memory browser session id.")
		props["page_id"] = stringProp("Optional CDP target id. Omit to use the active page.")
		props["actions"] = browserActionsProp()
		props["full_page"] = boolProp("Capture the full page in the final PNG screenshot.")
		props["max_text_chars"] = boundedIntProp("Maximum normalized body text characters. Defaults to 8000.", 1, 50000)
		props["max_interactive_elements"] = boundedIntProp("Maximum visible interactive elements. Defaults to 40.", 1, 200)
		props["retention_seconds"] = boundedIntProp("Screenshot Artifact retention seconds. Zero uses the Artifact default; capped at 604800.", 0, 604800)
		props["close_after"] = boolProp("Close the session only after all actions, final snapshot, and screenshot Artifact publication succeed.")
		props["timeout_ms"] = boundedIntProp("Overall operation timeout in milliseconds. Defaults to 30000 and is capped at 300000.", 1, 300000)
		required = []string{"session_id", "actions"}
	case "browser_snapshot":
		props["session_id"] = stringProp("In-memory browser session id.")
		props["page_id"] = stringProp("Optional CDP target id. Omit to use the active page.")
		props["full_page"] = boolProp("Capture the full page directly through CDP as PNG.")
		props["max_text_chars"] = boundedIntProp("Maximum normalized body text characters. Defaults to 8000.", 1, 50000)
		props["max_interactive_elements"] = boundedIntProp("Maximum visible interactive elements. Defaults to 40.", 1, 200)
		props["retention_seconds"] = boundedIntProp("Screenshot Artifact retention seconds. Zero uses the Artifact default; capped at 604800.", 0, 604800)
		props["close_after"] = boolProp("Close the session only after snapshot and screenshot Artifact publication succeed.")
		props["timeout_ms"] = boundedIntProp("Operation timeout in milliseconds. Defaults to 30000 and is capped at 300000.", 1, 300000)
		required = []string{"session_id"}
	case "view_image":
		props["artifact_id"] = stringProp("Artifact id returned by an AgentDock image-producing tool.")
		props["path"] = stringProp("Host image path. Relative paths resolve from ~/AgentDock.")
		props["url"] = stringProp("Absolute HTTP(S) image URL.")
		props["max_source_bytes"] = intProp("Maximum source bytes before processing. Defaults to 20971520 and is capped at 104857600.")
		props["source_timeout_ms"] = boundedIntProp("HTTP(S) source timeout in milliseconds. Defaults to 15000 and is capped at 120000.", 1, 120000)
		props["max_bytes"] = intProp("Maximum processed image bytes returned to the model. Defaults to 750000 and is capped at 2097152.")
		props["max_width"] = intProp("Maximum image width. Defaults to 1280.")
		props["max_height"] = intProp("Maximum image height. Defaults to 1280.")
		props["auto_resize"] = boolProp("Resize/compress when limits are exceeded. Defaults to true.")
		props["format"] = stringProp("Processed image format: jpeg or png. Defaults to jpeg.")
		props["quality"] = intProp("JPEG quality when format is jpeg. Defaults to 72.")
		props["crop"] = map[string]any{"type": "object", "description": "Optional crop rectangle {x,y,width,height} before resizing.", "additionalProperties": true}
	}

	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": true}
	if name == "view_image" {
		// 严格的工具调用供应商会独立校验 oneOf 分支类型；每个分支都显式声明为对象，
		// 避免把只有 required 的分支识别成可能接受非对象值。
		schema["oneOf"] = []map[string]any{
			{"type": "object", "required": []string{"artifact_id"}},
			{"type": "object", "required": []string{"path"}},
			{"type": "object", "required": []string{"url"}},
		}
	}
	switch name {
	case "opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit", "opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git", "job_start", "job_status", "job_output", "job_cancel", "job_list", "exec_command", "acp_session", "acp_prompt", "acp_interaction", "recall_bootstrap", "recall_search", "recall_read", "recall_write", "recall_maintain", "private_note_manage", "mcp_manage", "mcp_tool_search", "mcp_tool_inspect", "mcp_tool_call", "browser_session", "browser_act", "browser_snapshot":
		// 这些工具的参数契约需要严格收敛，避免删除或拼错的字段被静默忽略。
		schema["additionalProperties"] = false
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func browserActionsProp() map[string]any {
	stringProp := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	intProp := func(desc string, minimum, maximum int) map[string]any {
		return map[string]any{"type": "integer", "description": desc, "minimum": minimum, "maximum": maximum}
	}
	boolProp := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	actionObject := func(name string, required []string, properties map[string]any) map[string]any {
		properties["action"] = map[string]any{"type": "string", "const": name}
		return map[string]any{"type": "object", "additionalProperties": false, "required": append([]string{"action"}, required...), "properties": properties}
	}
	waitUntil := map[string]any{"type": "string", "enum": []string{"domcontentloaded", "load"}, "description": "Real CDP navigation lifecycle event to await."}
	state := map[string]any{"type": "string", "enum": []string{"visible", "hidden", "attached", "detached"}}
	timeout := intProp("Per-action timeout in milliseconds.", 1, 300000)
	selector := stringProp("CSS selector. Playwright locators and XPath are not accepted.")

	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": 100,
		"description": "Strict browser actions executed through native Go CDP.",
		"items": map[string]any{"oneOf": []map[string]any{
			actionObject("goto", []string{"url"}, map[string]any{"url": stringProp("Destination URL."), "wait_until": waitUntil, "timeout_ms": timeout}),
			actionObject("click", []string{"selector"}, map[string]any{"selector": selector}),
			actionObject("fill", []string{"selector", "value"}, map[string]any{"selector": selector, "value": stringProp("Replacement input value.")}),
			actionObject("press", []string{"key"}, map[string]any{"selector": selector, "key": stringProp("Key name or text to send.")}),
			actionObject("wait", []string{"value"}, map[string]any{"value": intProp("Duration in milliseconds.", 0, 300000)}),
			actionObject("wait_for_selector", []string{"selector"}, map[string]any{"selector": selector, "state": state, "timeout_ms": timeout}),
			actionObject("wait_for_url", []string{"url"}, map[string]any{"url": stringProp("URL substring or * / ? wildcard pattern."), "timeout_ms": timeout}),
			actionObject("wait_for_text", []string{"text"}, map[string]any{"text": stringProp("Text matched against each individual DOM element after whitespace normalization."), "exact": boolProp("Require one element's normalized text to equal text."), "state": state, "timeout_ms": timeout}),
			actionObject("wait_for_response", nil, map[string]any{"url": stringProp("Response URL substring."), "url_pattern": stringProp("Response URL regular expression."), "method": stringProp("Optional HTTP method."), "status": intProp("Optional HTTP status.", 100, 599), "timeout_ms": timeout}),
			actionObject("select", []string{"selector", "value"}, map[string]any{"selector": selector, "value": stringProp("Select element value.")}),
			actionObject("scroll", nil, map[string]any{"delta_x": intProp("Horizontal scroll delta.", -100000, 100000), "delta_y": intProp("Vertical scroll delta.", -100000, 100000)}),
			actionObject("reload", nil, map[string]any{"wait_until": waitUntil, "timeout_ms": timeout}),
			actionObject("back", nil, map[string]any{"wait_until": waitUntil, "timeout_ms": timeout}),
			actionObject("forward", nil, map[string]any{"wait_until": waitUntil, "timeout_ms": timeout}),
		}},
	}
}
