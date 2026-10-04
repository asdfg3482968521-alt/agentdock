package app

import toolfile "github.com/uvwt/agentdock/internal/tool/file"

func OutputSchema(name string) map[string]any {
	props := map[string]any{}
	// MCP envelope 的 isError 已表达工具调用错误；这里只描述领域结果，
	// 不再要求含义模糊的通用 ok 字段。
	required := []string{}
	stringProp := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	intProp := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boolProp := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	arrayProp := func(desc string) map[string]any {
		return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "object", "additionalProperties": true}}
	}
	objectProp := func(desc string) map[string]any {
		return map[string]any{"type": "object", "description": desc, "additionalProperties": true}
	}
	mcpClientSessionProp := func() map[string]any {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"session_key", "session_id_masked", "client_name", "client_version", "protocol_version", "first_seen_at", "last_seen_at", "last_tool", "last_request_id", "tool_calls", "active_calls", "last_started_at", "last_finished_at", "last_duration_ms", "last_ok", "conversation_url", "conversation_title", "watchdog_task_id", "bound_at"},
			"properties": map[string]any{
				"session_key":       stringProp("Stable non-secret fingerprint for this MCP client session."),
				"session_id_masked": stringProp("Masked opaque client session id for operator display."),
				"client_name":       stringProp("MCP client implementation name when reported."),
				"client_version":    stringProp("MCP client implementation version when reported."),
				"protocol_version":  stringProp("Negotiated MCP protocol version when reported."),
				"first_seen_at":     stringProp("UTC first-seen timestamp."),
				"last_seen_at":      stringProp("UTC last-seen timestamp."),
				"last_tool":         stringProp("Most recently invoked AgentDock tool."),
				"last_request_id":   stringProp("Most recent AgentDock request id for log correlation."),
				"tool_calls":        intProp("Number of observed MCP tool calls in this session."),
				"active_calls":      intProp("Number of currently in-flight MCP tool calls from this session."),
				"last_started_at":   stringProp("UTC timestamp when the most recent MCP tool call started."),
				"last_finished_at":  stringProp("UTC timestamp when the most recent MCP tool call finished."),
				"last_duration_ms":  intProp("Duration in milliseconds of the most recently finished MCP tool call."),
				"last_ok":           boolProp("Whether the most recently finished MCP tool call succeeded."),
				"conversation_url":  stringProp("Bound ChatGPT conversation URL, when available."),

				"conversation_title": stringProp("Bound ChatGPT conversation title, when available."),
				"watchdog_task_id":   stringProp("Bound ChatGPT Watchdog task id, when available."),
				"bound_at":           stringProp("UTC binding timestamp, when available."),
			},
		}
	}
	sessionProp := func() map[string]any {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"session_id", "project_id", "repo_root", "workspace_type", "isolation_mode", "base_ref", "branch", "worktree", "permission_mode", "runtime_id", "job_ids", "client_tag", "created_at", "last_active_at", "state", "failure_reason", "metadata_version"},
			"properties": map[string]any{
				"session_id":       map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"project_id":       map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9][A-Za-z0-9._-]*$"},
				"repo_root":        map[string]any{"type": "string", "minLength": 1},
				"workspace_type":   map[string]any{"type": "string", "enum": []string{"git", "non_git"}},
				"isolation_mode":   map[string]any{"type": "string", "enum": []string{"worktree", "exclusive", "shared-readonly", "shared-explicit"}},
				"base_ref":         map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": 512},
				"branch":           map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": 512},
				"worktree":         map[string]any{"type": []string{"string", "null"}, "minLength": 1},
				"permission_mode":  map[string]any{"type": "string", "enum": []string{"project", "full"}},
				"runtime_id":       map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": 256},
				"job_ids":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "uniqueItems": true},
				"client_tag":       map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": 256},
				"created_at":       map[string]any{"type": "string", "format": "date-time"},
				"last_active_at":   map[string]any{"type": "string", "format": "date-time"},
				"state":            map[string]any{"type": "string", "enum": []string{"OPENING", "ACTIVE", "CLOSING", "CLOSED", "FAILED"}},
				"failure_reason":   map[string]any{"type": []string{"string", "null"}, "maxLength": 4096},
				"metadata_version": map[string]any{"type": "string", "const": "D1-v1"},
			},
			"allOf": []map[string]any{
				{
					"if": map[string]any{"properties": map[string]any{"workspace_type": map[string]any{"const": "git"}}, "required": []string{"workspace_type"}},
					"then": map[string]any{"properties": map[string]any{
						"isolation_mode": map[string]any{"const": "worktree"},
						"base_ref":       map[string]any{"type": "string", "minLength": 1},
						"branch":         map[string]any{"type": "string", "minLength": 1},
						"worktree":       map[string]any{"type": "string", "minLength": 1},
					}},
				},
				{
					"if": map[string]any{"properties": map[string]any{"workspace_type": map[string]any{"const": "non_git"}}, "required": []string{"workspace_type"}},
					"then": map[string]any{"properties": map[string]any{
						"isolation_mode": map[string]any{"enum": []string{"exclusive", "shared-readonly", "shared-explicit"}},
						"base_ref":       map[string]any{"type": "null"},
						"branch":         map[string]any{"type": "null"},
						"worktree":       map[string]any{"type": "null"},
					}},
				},
				{
					"if":   map[string]any{"properties": map[string]any{"state": map[string]any{"const": "FAILED"}}, "required": []string{"state"}},
					"then": map[string]any{"properties": map[string]any{"failure_reason": map[string]any{"type": "string", "minLength": 1}}},
					"else": map[string]any{"properties": map[string]any{"failure_reason": map[string]any{"type": "null"}}},
				},
			},
		}
	}

	switch name {
	case "opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit", "opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git":
		props["tool"] = stringProp("Underlying verified OpenCode tool name.")
		props["workspace"] = stringProp("Canonical workspace bound to the OpenCode runtime.")
		props["permission_mode"] = stringProp("OpenCode filesystem permission mode used by this runtime.")
		props["session_id"] = stringProp("D1 session identifier when the call was Session-routed.")
		props["runtime_scope"] = stringProp("Logical D1 Session runtime scope when Session-routed.")
		props["runtime"] = objectProp("OpenCode runtime identity, state, PID, health, restart count, and startup metrics.")
		props["result"] = objectProp("Unmodified MCP tool result returned by the underlying OpenCode runtime.")
		required = []string{"tool", "workspace", "permission_mode", "runtime", "result"}
	case "job_start":
		props["job_id"] = stringProp("Durable persistent job identifier.")
		props["session_id"] = stringProp("Owning D1 session identifier for Session-owned jobs.")
		props["workspace"] = stringProp("Canonical workspace bound to the job.")
		props["runtime_scope"] = stringProp("Logical D1 Session runtime scope for Session-owned jobs.")
		props["state"] = stringProp("Initial job state.")
		props["created_at"] = stringProp("UTC creation timestamp.")
		required = []string{"job_id", "state", "created_at"}
	case "job_status", "job_cancel":
		props["job_id"] = stringProp("Durable persistent job identifier.")
		props["session_id"] = stringProp("Owning D1 session identifier when present.")
		props["workspace"] = stringProp("Workspace bound to this job.")
		props["runtime_scope"] = stringProp("Logical D1 Session runtime scope when present.")
		props["tool"] = stringProp("Underlying verified OpenCode tool.")
		props["permission_mode"] = stringProp("OpenCode permission mode.")
		props["state"] = stringProp("Current durable job state.")
		props["created_at"] = stringProp("UTC creation timestamp.")
		props["started_at"] = stringProp("UTC start timestamp when available.")
		props["finished_at"] = stringProp("UTC finish timestamp when available.")
		props["duration_ms"] = intProp("Elapsed job duration in milliseconds.")
		props["runtime_id"] = stringProp("Supervisor request identity when assigned.")
		props["output_cursor"] = intProp("Current stable output cursor.")
		props["output_bytes"] = intProp("Total logical output bytes observed.")
		props["exit_code"] = intProp("Runtime result exit code when available.")
		props["result_summary"] = stringProp("Redacted bounded result summary.")
		props["error"] = stringProp("Redacted bounded error summary.")
		props["cancel_reason"] = stringProp("Redacted cancellation reason.")
		props["progress_metadata"] = objectProp("Redacted progress metadata when available.")
		required = []string{"job_id", "state", "created_at", "duration_ms", "output_cursor", "output_bytes"}
	case "job_output":
		props["chunks"] = arrayProp("Incremental output chunks beginning at the requested cursor.")
		props["text"] = stringProp("Concatenated text for returned chunks only.")
		props["next_cursor"] = intProp("Cursor to pass to the next job_output request.")
		props["truncated"] = boolProp("Whether response or retained-history bounds prevented returning every requested byte.")
		props["complete"] = boolProp("Whether the job is terminal and this response reached the current output cursor.")
		required = []string{"chunks", "text", "next_cursor", "truncated", "complete"}
	case "job_list":
		props["jobs"] = arrayProp("Bounded persistent job summaries.")
		props["count"] = intProp("Number of returned jobs.")
		required = []string{"jobs", "count"}
	case "session_open", "session_status":
		props["ok"] = map[string]any{"type": "boolean", "const": true}
		props["session"] = sessionProp()
		required = []string{"ok", "session"}
	case "session_list":
		props["ok"] = map[string]any{"type": "boolean", "const": true}
		props["sessions"] = map[string]any{"type": "array", "items": sessionProp()}
		props["next_cursor"] = map[string]any{"type": []string{"string", "null"}}
		required = []string{"ok", "sessions", "next_cursor"}
	case "session_close":
		props["ok"] = map[string]any{"type": "boolean", "const": true}
		props["session"] = sessionProp()
		props["jobs_cancelled"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "uniqueItems": true}
		props["branch_preserved"] = map[string]any{"type": "boolean", "const": true}
		props["worktree_preserved"] = map[string]any{"type": "boolean", "const": true}
		required = []string{"ok", "session", "jobs_cancelled", "branch_preserved", "worktree_preserved"}
	case "agent_supervisor_message":
		props["opencode_session_id"] = stringProp("Exact persisted OpenCode root session id used for this durable message.")
		props["message"] = objectProp("Durable supervisor message record including stable supervisor_message_id and dispatch state.")
		props["created"] = boolProp("Whether this client_message_id created a new durable message.")
		props["dispatched"] = boolProp("Whether the durable message was dispatched during this call.")
		required = []string{"opencode_session_id", "message", "created", "dispatched"}
	case "agent_supervisor_correct":
		props["mode"] = stringProp("Applied correction mode: graceful or force.")
		props["message"] = objectProp("Durable correction message record including stable supervisor_message_id and dispatch state.")
		props["opencode_session_id_before"] = stringProp("Exact persisted OpenCode root before correction dispatch.")
		props["opencode_session_id_after"] = stringProp("Exact persisted OpenCode root after correction dispatch; replacement roots are forbidden.")
		props["aborted"] = boolProp("Whether force mode aborted the current native turn before correction dispatch.")
		props["preemptive_claimed"] = boolProp("Always false for the current contract; true interrupted execution resume is not claimed.")
		required = []string{"mode", "message", "opencode_session_id_before", "opencode_session_id_after", "aborted", "preemptive_claimed"}
	case "agent_supervisor_observe":
		props["root_opencode_session_id"] = stringProp("Exact persisted OpenCode root session id for this subagent conversation.")
		props["statuses"] = objectProp("Native status map filtered to the Session-owned conversation lineage.")
		props["messages"] = arrayProp("Native messages belonging to the owned conversation lineage.")
		props["public_events"] = objectProp("Bounded durable public event page with stable cursor semantics.")
		props["pending_permissions"] = arrayProp("Pending native permission requests that belong to this Session/subagent only.")
		required = []string{"root_opencode_session_id", "statuses", "messages", "public_events", "pending_permissions"}
	case "server_info":
		props["server"] = stringProp("Server identifier.")
		props["title"] = stringProp("Human-readable server title.")
		props["version"] = stringProp("Server version.")
		props["protocol_version"] = stringProp("AgentDock protocol version.")
		props["os"] = stringProp("Host operating system reported by the Go runtime.")
		props["arch"] = stringProp("Host architecture reported by the Go runtime.")
		props["go_version"] = stringProp("Go runtime version.")
		props["agentdock_home"] = stringProp("AgentDock state and configuration directory.")
		props["agentdock_default_dir"] = stringProp("AgentDock default working directory.")
		props["default_cwd"] = stringProp("Default cwd relative to ~/AgentDock when applicable.")
		props["path_model"] = stringProp("Path resolution model used by host tools.")
		props["recall_enabled"] = boolProp("Whether NexusDock Recall integration is enabled.")
		props["nexus_endpoint"] = stringProp("Configured NexusDock endpoint.")
		props["recall_bootstrap_recommended"] = boolProp("Whether clients should load Recall bootstrap context.")
		props["recall_bootstrap_tool"] = stringProp("Tool name used to load Recall bootstrap context.")
		props["recall_bootstrap_args"] = objectProp("Recommended arguments for Recall bootstrap.")
		props["tools"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["tool_count"] = intProp("Number of exposed tools.")
		props["task_state_dir"] = stringProp("Local directory containing persistent recoverable task state.")
		props["command_session_limits"] = objectProp("Concurrent and retained command-session limits.")
		props["browser_enabled"] = boolProp("Whether browser tools are enabled by host configuration.")
		props["acp_enabled"] = boolProp("Whether the built-in ACP runtime is enabled by host configuration.")
		props["acp_agent"] = stringProp("Configured local ACP agent profile name.")
		props["opencode_enabled"] = boolProp("Whether the verified model-free OpenCode runtime integration is enabled.")
		props["opencode_manifest"] = stringProp("Configured verified OpenCode runtime manifest path.")
		props["opencode_runtimes"] = arrayProp("Current workspace-bound OpenCode runtime summaries.")
		props["opencode_tools"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["trusted_proxy_cidrs"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["auth_enabled"] = boolProp("Whether MCP authentication is required.")
		props["endpoint_path"] = stringProp("HTTP path used by the MCP endpoint.")
		props["mcp_session_awareness"] = boolProp("Whether protocol-owned MCP client session tracking is available.")
		props["mcp_session_state_file"] = stringProp("Local JSON state file containing MCP client session activity and bindings.")
	case "agentdock_context":
		required = []string{"context"}
		props["context"] = stringProp("Rendered AgentDock bootstrap context text for clients that cannot inject system prompt context.")
	case "mcp_session_current", "mcp_session_bind":
		props["session"] = mcpClientSessionProp()
		props["state_file"] = stringProp("Local MCP client session state file path.")
		required = []string{"session", "state_file"}
	case "read_file":
		props["path"] = stringProp("Host path or skill:// resource URI. Relative Host paths resolve from ~/AgentDock.")
		props["content"] = stringProp("Text content slice.")
		props["encoding"] = stringProp("Detected text encoding.")
		props["size_bytes"] = intProp("File size in bytes.")
		props["truncated"] = boolProp("Whether output was truncated.")
		props["truncated_reason"] = stringProp("Reason output was truncated.")
		props["start_line"] = intProp("Returned start line.")
		props["end_line"] = intProp("Returned end line.")
		props["next_start_line"] = intProp("Next line to read when output was truncated.")
		props["total_lines"] = intProp("Total line count.")
	case "list_dir":
		props["path"] = stringProp("Host directory path. Relative paths resolve from ~/AgentDock.")
		props["entries"] = arrayProp("Directory entries.")
		props["truncated"] = boolProp("Whether entries were truncated.")
	case "list_files":
		props["path"] = stringProp("Host directory path. Relative paths resolve from ~/AgentDock.")
		props["files"] = arrayProp("Matched files.")
		props["truncated"] = boolProp("Whether files were truncated.")
	case "search_text":
		props["matches"] = arrayProp("Text search matches.")
		props["engine"] = stringProp("Search engine used: rg or go_fallback.")
		props["truncated"] = boolProp("Whether matches were truncated.")
	case "file_edit":
		props["action"] = stringProp("File edit action.")
		props["summary"] = stringProp("Result summary.")
		props["path"] = stringProp("Host path. Relative paths resolve from ~/AgentDock.")
		props["dry_run"] = boolProp("Whether this was a dry run.")
		props["matches"] = intProp("Match count for replace.")
		props["changed"] = boolProp("Whether content changed.")
		props["diff_preview"] = stringProp("Diff preview.")
		props["files_changed"] = intProp("Changed file count.")
		props["insertions"] = intProp("Inserted line count.")
		props["deletions"] = intProp("Deleted line count.")

	case "exec_command", "session_observe", "session_act":
		props["session_id"] = stringProp("Command session id.")
		props["status"] = stringProp("Session status.")
		props["runtime"] = stringProp("Command runtime when reported by the host, such as windows or wsl.")
		props["wsl_distribution"] = stringProp("WSL distribution selected for the command when explicitly configured.")
		props["workdir"] = stringProp("Logical command working directory in the selected runtime.")
		props["stdout"] = stringProp("Captured stdout segment.")
		props["stderr"] = stringProp("Captured stderr segment.")
		props["command_ok"] = boolProp("Whether a completed command exited successfully. Omitted while the command is still running.")
		props["command_error"] = stringProp("Command process error when execution did not succeed.")
		props["exit_code"] = intProp("Process exit code, when available.")
		props["elapsed_ms"] = intProp("Session elapsed milliseconds.")
		props["timed_out"] = boolProp("Whether the command timed out.")
		if name == "exec_command" {
			props["session_reason"] = stringProp("Why exec_command returned a session instead of a completed result.")
			props["observe_after_ms"] = intProp("Suggested delay before inspecting the returned session.")
		}
	case "task_manage":
		props["action"] = stringProp("Completed task action.")
		props["task_id"] = stringProp("Persistent task id returned by create and usable with task lifecycle actions.")
		props["task"] = objectProp("Full persistent task state returned only by get.")
		props["task_summary"] = objectProp("Compact task summary returned by task lifecycle actions.")
		props["next_required_action"] = stringProp("Concise guidance for checkpoint progress, template composition, or final review.")
		props["review_status"] = stringProp("Final review status when present: not_started, pass, or failed.")
		props["final_review"] = objectProp("Compact final review state with status and counts.")
		props["tasks"] = arrayProp("Compact task summaries ordered by most recent update.")
		props["count"] = intProp("Returned item count.")
		props["state_dir"] = stringProp("Local AgentDock task state directory.")
		props["guidance_context"] = arrayProp("Mature evolution records automatically recalled before task execution.")
		props["review_revision"] = stringProp("Immutable final_review revision used to bind evolution evidence.")
		props["evolution_candidates"] = arrayProp("Read-only candidate experiences that the saved final_review may verify.")
		props["evolution_warning"] = stringProp("Non-blocking evolution-side warning; Task lifecycle still succeeded.")
	case "evolve":
		props["intent"] = stringProp("Completed evolution intent.")
		props["evolution_id"] = stringProp("Stable evolution id.")
		props["status"] = stringProp("Lifecycle status computed by AgentDock policy.")
		props["revision"] = intProp("Nexus-backed lifecycle revision.")
		props["policy_version"] = stringProp("AgentDock policy version used for the transition.")
		props["support_count"] = intProp("Independent support evidence count computed by AgentDock.")
		props["contradict_count"] = intProp("Independent contradiction evidence count computed by AgentDock.")
		props["changed"] = boolProp("Whether durable evolution state changed.")
		props["idempotent"] = boolProp("Whether the request resolved to already-applied state.")
		props["message"] = stringProp("Short non-sensitive result explanation.")
	case "acp_session":
		props["action"] = stringProp("Completed ACP session action.")
		props["protocol_version"] = intProp("Negotiated ACP protocol version for info.")
		props["auth_method_id"] = stringProp("Authentication method selected by authenticate.")
		props["authenticated"] = boolProp("Whether the advertised authentication method completed successfully.")
		props["agent"] = objectProp("Configured ACP agent identity.")
		props["capabilities"] = objectProp("Capabilities reported by the ACP agent during initialize.")
		props["auth_methods"] = arrayProp("Authentication methods advertised by the ACP agent during initialize.")
		props["context_policy"] = objectProp("Transcript ownership and restart policy used to avoid duplicated or replay-corrupted context.")
		props["event_policy"] = objectProp("Bounded incremental event delivery policy.")
		props["interaction_policy"] = objectProp("In-memory permission interaction bounds and local authorization policy.")
		props["steering_policy"] = objectProp("Native steering and observable host-owned fallback policy.")
		props["session"] = objectProp("Persistent AgentDock ACP session record.")
		props["sessions"] = arrayProp("Persistent ACP session records ordered by most recent update.")
		props["session_id"] = stringProp("AgentDock ACP session id.")
		props["modes"] = objectProp("Session modes returned by the ACP agent when present.")
		props["config_options"] = arrayProp("Current session configuration options returned by the ACP agent when present.")
		props["mode_id"] = stringProp("Session mode id applied by set_mode.")
		props["config_id"] = stringProp("Session configuration option id applied by set_config.")
		props["changed"] = boolProp("Whether a session mode or configuration option was changed.")
		props["count"] = intProp("Returned ACP session count.")
		props["deleted"] = boolProp("Whether the persistent ACP session was deleted.")
	case "acp_prompt":
		props["action"] = stringProp("Completed ACP prompt action.")
		props["run_id"] = stringProp("AgentDock ACP prompt run id.")
		props["session_id"] = stringProp("AgentDock ACP session id.")
		props["status"] = stringProp("ACP prompt run status.")
		props["events"] = arrayProp("Ordered ACP session events with monotonic seq values.")
		props["next_seq"] = intProp("Cursor to pass unchanged as after_seq on the next events call.")
		props["first_seq"] = intProp("Oldest event sequence still retained in the bounded run event ring.")
		props["latest_seq"] = intProp("Newest event sequence observed for the run when this page was read.")
		props["dropped_count"] = intProp("Number of oldest events evicted from the bounded run event ring.")
		props["has_more"] = boolProp("Whether more retained events are immediately available after next_seq.")
		props["truncated"] = boolProp("Whether requested event history was older than the retained event ring.")
		props["started_at"] = stringProp("Prompt run start timestamp.")
		props["ended_at"] = stringProp("Prompt run end timestamp when settled.")
		props["stop_reason"] = stringProp("ACP stop reason when supplied by the agent.")
		props["error_code"] = stringProp("AgentDock ACP error code when the run failed.")
		props["message"] = stringProp("ACP run error message when present.")
		props["steering"] = objectProp("ACP steering outcome.")
		props["cancel_requested"] = boolProp("Whether cancellation was requested.")
	case "acp_interaction":
		props["action"] = stringProp("Completed ACP interaction action.")
		props["interaction"] = objectProp("ACP permission interaction state.")
		props["interactions"] = arrayProp("ACP permission interactions.")
		props["count"] = intProp("Returned ACP interaction count.")
		props["responded"] = boolProp("Whether a permission option response was accepted.")
		props["cancelled"] = boolProp("Whether the interaction was cancelled.")
	case "workflow_template_manage":
		props["action"] = stringProp("Completed workflow template action.")
		props["template"] = objectProp("Full workflow template returned by get.")
		props["templates"] = arrayProp("Compact summaries from list or full active templates from get_many.")
		props["composition_required"] = boolProp("Whether the returned templates must be combined by the model before task creation.")
		props["next_required_action"] = stringProp("Required model action after get_many.")
		props["template_id"] = stringProp("Workflow template id returned by save, validate, publish, or retire.")
		props["template_summary"] = objectProp("Compact workflow template summary returned by save, validate, publish, retire, and list items.")
		props["valid"] = boolProp("Whether a draft workflow template passed validation.")
		props["count"] = intProp("Returned item count.")
		props["workflow_dir"] = stringProp("Local AgentDock workflow template directory.")
		props["candidates"] = arrayProp("Matched workflow template candidates with scores and reasons.")
		props["vector_search_enabled"] = boolProp("Whether optional embedding-backed template vector search is enabled for match.")
		props["vector_index_status"] = stringProp("Template vector index status: disabled, ready, or degraded.")
		props["vector_index_items"] = intProp("Number of persisted template vectors for the current embedding model.")
		props["vector_index_available"] = boolProp("Whether workflow vector index content is available for export.")
		props["content"] = stringProp("Raw workflow vector index JSON returned by vector_index.")
		props["embedding_model"] = stringProp("Embedding model configured for template vector search.")
		props["recommended"] = stringProp("Template recommendation: use_template, consider_template, or plain_task.")
		props["recommendation_reason"] = stringProp("Reason for recommendation.")
		props["best_candidate_score"] = intProp("Highest template match score.")
		props["score_thresholds"] = objectProp("Template match score thresholds.")

	case "skill_package":
		props["action"] = stringProp("Completed Skill package action.")
		props["skill"] = stringProp("Skill name.")
		props["name"] = stringProp("Skill name for environment actions.")
		props["key"] = stringProp("Environment variable name. Secret values are never returned.")
		props["configured"] = boolProp("Whether the environment variable has a non-empty configured value.")
		props["removed"] = boolProp("Whether the environment variable was removed.")
		props["items"] = arrayProp("Environment variable names and configured status without values.")
		props["count"] = intProp("Returned environment variable count.")
		props["valid"] = boolProp("Whether a Skill source passed validation.")
		props["source"] = stringProp("Resolved Skill source label.")
		props["digest"] = stringProp("Computed Skill package digest.")
		props["issues"] = arrayProp("Structured validation issues.")
		props["document"] = objectProp("Parsed SKILL.md frontmatter and body metadata.")
		props["result"] = objectProp("Install, activate, or rollback result.")
	case "mcp_manage":
		props["action"] = stringProp("Completed dynamic MCP management action.")
		props["servers"] = arrayProp("Registered dynamic MCP server summaries.")
		props["server"] = objectProp("Dynamic MCP server summary.")
		props["config"] = objectProp("Dynamic MCP server configuration containing only non-secret values and environment variable names.")
		props["tools"] = arrayProp("Discovered lightweight MCP tool summaries.")
		props["tool_count"] = intProp("Discovered tool count.")
		props["count"] = intProp("Registered server count.")
		props["name"] = stringProp("Dynamic MCP server name.")
		props["removed"] = boolProp("Whether the server or environment variable was removed.")
		props["key"] = stringProp("Environment variable name. Secret values are never returned.")
		props["configured"] = boolProp("Whether the environment variable has a non-empty configured value.")
		props["items"] = arrayProp("Environment variable names and configured status without values.")
	case "mcp_tool_search":
		props["query"] = stringProp("Capability query used.")
		props["server"] = stringProp("Optional server filter used.")
		props["tools"] = arrayProp("Matching lightweight MCP tool summaries.")
		props["count"] = intProp("Matching tool count.")
	case "mcp_tool_inspect":
		props["name"] = stringProp("Qualified MCP tool name.")
		props["server"] = stringProp("Dynamic MCP server name.")
		props["tool_name"] = stringProp("Upstream MCP tool name.")
		props["title"] = stringProp("Tool title.")
		props["description"] = stringProp("Tool description.")
		props["input_schema"] = objectProp("Complete upstream MCP tool input schema.")
		props["output_schema"] = objectProp("Optional upstream MCP tool output schema.")
		props["annotations"] = objectProp("Optional upstream MCP tool annotations.")
	case "mcp_tool_call":
		props["name"] = stringProp("Qualified MCP tool name.")
		props["result"] = objectProp("Raw upstream MCP tools/call result, including content and structuredContent when supplied.")
	case "file_publish":
		props["artifact_id"] = stringProp("Published artifact id.")
		props["url"] = stringProp("Optional temporary signed download URL when a reachable base URL is available.")
		props["expires_at"] = stringProp("Signed URL expiry timestamp.")
		props["sha256"] = stringProp("Snapshot payload SHA-256.")
		props["size_bytes"] = intProp("Snapshot payload size in bytes.")
		props["mime_type"] = stringProp("Payload media type.")
		props["filename"] = stringProp("Download filename used for Content-Disposition and signature verification.")
		props["archive"] = boolProp("Whether the source directory was packaged as tar.gz.")
		props["width"] = intProp("Image width when the payload is an image.")
		props["height"] = intProp("Image height when the payload is an image.")
	case "recall_bootstrap":
		props["recall_endpoint"] = stringProp("Configured NexusDock Recall endpoint.")
		props["project"] = stringProp("Backend-selected NexusDock Recall context returned by the backing service; not an input selector for the model.")
		props["sections"] = arrayProp("Packed recall sections. Raw Markdown is omitted by default and returned as raw_content only when include_raw=true.")
		props["count"] = intProp("Section count.")
		props["bytes"] = intProp("Combined bytes.")
	case "recall_search":
		props["recall_endpoint"] = stringProp("Configured NexusDock Recall endpoint.")
		props["recall_kind"] = stringProp("Search kind used.")
		props["query"] = stringProp("Search query.")
		props["results"] = arrayProp("Search results.")
		props["count"] = intProp("Search result count.")
	case "recall_read":
		props["recall_endpoint"] = stringProp("Configured NexusDock Recall endpoint.")
		props["recall"] = objectProp("NexusDock Recall document returned by the backing service. Raw Markdown is omitted by default and returned as raw_content only when include_raw=true.")
	case "recall_write":
		props["recall_endpoint"] = stringProp("Configured NexusDock Recall endpoint.")
		props["recall_target"] = stringProp("Recall target used.")
		props["recall_action"] = stringProp("Recall action used.")
		props["recall"] = objectProp("NexusDock Recall document returned by the backing service when a write occurs.")
		props["card"] = objectProp("Normalized card candidate or written card when target=card.")
		props["warnings"] = arrayProp("Review warnings before writing.")
		props["capture_plan"] = objectProp("Reviewable write plan for card captures.")
		props["similar_results"] = arrayProp("Similar existing card search results.")
		props["path"] = stringProp("NexusDock Recall-relative path.")
		props["changed"] = boolProp("Whether the proposed edit changes content.")
		props["dry_run"] = boolProp("Whether the operation only previewed changes.")
		props["confirmed"] = boolProp("Whether write confirmation was supplied.")
		props["written"] = boolProp("Whether the entry was written.")
		props["diff"] = stringProp("Unified diff preview.")
		props["updates"] = arrayProp("Fact update results.")
	case "recall_maintain":
		props["recall_endpoint"] = stringProp("Configured NexusDock Recall endpoint.")
		props["recall_action"] = stringProp("Maintenance action performed.")
		props["entries"] = arrayProp("NexusDock Recall entries for action=list.")
		props["count"] = intProp("Entry count where applicable.")
		props["terms"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		props["files_scanned"] = intProp("Files scanned for action=lint.")
		props["finding_count"] = intProp("Finding count for action=lint.")
		props["findings"] = arrayProp("Lint findings.")
		props["git_repo"] = boolProp("Whether the NexusDock Recall store is a Git repository for action=sync_status.")
		props["auto_sync_enabled"] = boolProp("Whether NexusDock Recall auto sync is enabled.")
		props["pending_push"] = boolProp("Whether NexusDock Recall has remote sync work.")
		props["conflict"] = boolProp("Whether NexusDock Recall detected a sync conflict.")
	case "private_note_manage":
		props["root"] = stringProp("NexusDock private notes root path.")
		props["private_note_store"] = stringProp("Private note store name, fixed to NexusDock Private Notes.")
		props["action"] = stringProp("Action performed.")
		props["query"] = stringProp("Metadata-only query for action=search.")
		props["results"] = arrayProp("Metadata-only search results containing title, summary, tags, category, path, updated_at, and secret marker; never plaintext snippets.")
		props["metadata_only"] = boolProp("Whether search was restricted to safe metadata.")
		props["path"] = stringProp("Plain note path for read/write/delete results.")
		props["encrypted_path"] = stringProp("Age encrypted backup path, typically encrypted/<category>/<name>.md.age.")
		props["content"] = stringProp("Plaintext content returned only by explicit action=read.")
		props["truncated"] = boolProp("Whether returned content was truncated.")
		props["contains_secret"] = boolProp("Whether the note content is marked as containing secrets.")
		props["written"] = boolProp("Whether plaintext was written.")
		props["encrypted"] = boolProp("Whether encrypted backup was written.")
		props["deleted_plaintext"] = boolProp("Whether plaintext was deleted.")
		props["deleted_encrypted"] = boolProp("Whether encrypted backup was deleted.")
		props["notes"] = arrayProp("Metadata-only private note summaries for action=status status_action=list.")
		props["count"] = intProp("Result or note count.")
		props["notes_count"] = intProp("Private note count for status checks.")
		props["encrypted_count"] = intProp("Encrypted backup count for maintenance actions.")
		props["recipient"] = stringProp("Age public recipient generated or used.")
		props["identity_created"] = boolProp("Whether a new local age identity was created.")
		props["algorithm"] = stringProp("Encryption algorithm.")
		props["missing_encrypted"] = arrayProp("Missing encrypted backup paths.")
		props["encrypted_backup_ok"] = boolProp("Whether every private note has its required encrypted backup.")
		props["plaintext_git_ignored"] = boolProp("Whether private note plaintext is Git-ignored.")
		props["keys_git_ignored"] = boolProp("Whether private note keys are Git-ignored.")
	case "browser_session":
		props["browser_ok"] = boolProp("Whether the native Go CDP browser operation succeeded.")
		props["error"] = objectProp("Structured browser error with code, message, phase, and optional details.")
		props["code"] = stringProp("One of the native browser error codes.")
		props["session_id"] = stringProp("Current-process browser session id.")
		props["page_id"] = stringProp("Active CDP target id.")
		props["pages"] = arrayProp("Current page targets.")
		props["url"] = stringProp("Active page URL after start.")
		props["title"] = stringProp("Active page title after start.")
		props["profile_id"] = stringProp("Normalized persistent profile id when configured.")
		props["connection_mode"] = stringProp("How action=start obtained the browser: owned, external_explicit, external_configured, or external_discovered.")
		props["closed"] = boolProp("Whether action=close closed the AgentDock browser session. External browsers remain running.")
		props["removed_count"] = intProp("Number of current-process stale sessions terminated by cleanup_stale.")
		props["removed_sessions"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Session ids terminated by cleanup_stale."}
	case "browser_act", "browser_snapshot":
		props["browser_ok"] = boolProp("Whether the native Go CDP browser operation succeeded.")
		props["error"] = objectProp("Structured browser error with code, message, phase, and optional details.")
		props["code"] = stringProp("One of the native browser error codes.")
		props["session_id"] = stringProp("Current-process browser session id.")
		props["page_id"] = stringProp("Active CDP target id.")
		props["pages"] = arrayProp("Current page targets with active state.")
		props["url"] = stringProp("Current page URL.")
		props["title"] = stringProp("Current page title.")
		props["text"] = stringProp("Normalized current page body text excerpt.")
		props["viewport"] = objectProp("Viewport dimensions in CSS pixels.")
		props["page_size"] = objectProp("Scrollable page dimensions in CSS pixels.")
		props["focused_element"] = objectProp("Focused DOM element summary when available.")
		props["interactive_elements"] = arrayProp("Visible interactive DOM element summaries.")
		props["screenshot"] = objectProp("Published PNG Artifact reference.")
		props["console_errors"] = arrayProp("Console errors captured for this operation.")
		props["network_errors"] = arrayProp("Network loading failures captured for this operation.")
		props["page_errors"] = arrayProp("Unhandled page exceptions captured for this operation.")
		props["closed"] = boolProp("Whether close_after terminated the session after successful Artifact publication.")
	case "view_image":
		props["source"] = objectProp("Resolved artifact, path, or URL source metadata.")
		props["image"] = objectProp("Processed image metadata attached as standard MCP image content.")
		props["original"] = objectProp("Original/crop metadata.")
		props["resized"] = boolProp("Whether image bytes changed due to crop/resize/re-encode.")
		props["warnings"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}

	switch name {
	case "read_file", "list_dir", "list_files", "search_text", "file_edit":
		toolfile.AddRuntimeOutputProperties(props)
	}
	additional := true
	switch name {
	case "session_open", "session_status", "session_list", "session_close", "agent_supervisor_message", "agent_supervisor_observe", "agent_supervisor_correct", "mcp_session_current", "mcp_session_bind":
		additional = false
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": additional}
}
