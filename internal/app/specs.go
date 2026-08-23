package app

import (
	"context"

	"github.com/uvwt/agentdock/internal/config"
	toolcommand "github.com/uvwt/agentdock/internal/tool/command"
	toolfile "github.com/uvwt/agentdock/internal/tool/file"
)

type ToolHandler func(context.Context, *Runtime, map[string]any) (Result, error)

// ToolSpec 是工具公开入口的单一事实源：运行时分发、MCP 描述、
// 配置开关都从这里派生，避免多处手写列表漂移。
type ToolSpec struct {
	Name                   string
	Title                  string
	Description            string
	FileArgRewritePaths    []string
	FileResultRewritePaths []string
	InputSchema            func() map[string]any
	OutputSchema           func() map[string]any
	Annotations            *ToolAnnotations
	Availability           func(config.Config) bool
	Handler                ToolHandler
}

type ToolAnnotations struct {
	Title           string
	ReadOnlyHint    bool
	DestructiveHint *bool
	IdempotentHint  bool
	OpenWorldHint   *bool
}

type ToolDefinition struct {
	Name                   string
	Title                  string
	Description            string
	FileArgRewritePaths    []string
	FileResultRewritePaths []string
	InputSchema            map[string]any
	OutputSchema           map[string]any
	Annotations            *ToolAnnotations
}

// ToolDefinitions 只导出 MCP 层需要的描述和 schema，不暴露 handler。
func ToolDefinitions() []ToolDefinition {
	defs := make([]ToolDefinition, 0, len(allToolSpecs()))
	for _, spec := range allToolSpecs() {
		defs = append(defs, spec.definition())
	}
	return defs
}

func (s ToolSpec) definition() ToolDefinition {
	return ToolDefinition{
		Name:                   s.Name,
		Title:                  s.Title,
		Description:            s.Description,
		FileArgRewritePaths:    append([]string(nil), s.FileArgRewritePaths...),
		FileResultRewritePaths: append([]string(nil), s.FileResultRewritePaths...),
		InputSchema:            s.InputSchema(),
		OutputSchema:           s.OutputSchema(),
		Annotations:            cloneToolAnnotations(s.Annotations),
	}
}

func cloneToolAnnotations(value *ToolAnnotations) *ToolAnnotations {
	if value == nil {
		return nil
	}
	cloned := *value
	if value.DestructiveHint != nil {
		flag := *value.DestructiveHint
		cloned.DestructiveHint = &flag
	}
	if value.OpenWorldHint != nil {
		flag := *value.OpenWorldHint
		cloned.OpenWorldHint = &flag
	}
	return &cloned
}

func (r *Runtime) availableToolSpecs() []ToolSpec {
	out := make([]ToolSpec, 0, len(allToolSpecs()))
	for _, spec := range allToolSpecs() {
		if !spec.available(r.cfg) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func (s ToolSpec) available(cfg config.Config) bool {
	if s.Availability == nil {
		return true
	}
	return s.Availability(cfg)
}

func toolSpecByName(name string) (ToolSpec, bool) {
	for _, spec := range allToolSpecs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return ToolSpec{}, false
}

func requiresNexus(cfg config.Config) bool    { return cfg.NexusEndpoint != "" }
func requiresBrowser(cfg config.Config) bool  { return cfg.BrowserEnabled }
func requiresACP(cfg config.Config) bool      { return cfg.ACPEnabled }
func requiresOpenCode(cfg config.Config) bool { return cfg.OpenCodeEnabled }

func mutatingToolAnnotations(destructive, openWorld bool) *ToolAnnotations {
	return &ToolAnnotations{DestructiveHint: boolPointer(destructive), OpenWorldHint: boolPointer(openWorld)}
}

func boolPointer(value bool) *bool { return &value }

func toolHandler(fn func(*Runtime, map[string]any) (Result, error)) ToolHandler {
	return func(_ context.Context, r *Runtime, args map[string]any) (Result, error) { return fn(r, args) }
}

func ctxToolHandler(fn func(*Runtime, context.Context, map[string]any) (Result, error)) ToolHandler {
	return func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) { return fn(r, ctx, args) }
}

func allToolSpecs() []ToolSpec {
	// 顺序保持和旧 ToolNames 一致，避免 tools/list 与 server_info 的展示顺序无谓变化。
	specs := []ToolSpec{
		{Name: "server_info", Title: "Server info", Description: "Return server, host path model, auth, and exposed-tool metadata.", Handler: func(_ context.Context, r *Runtime, _ map[string]any) (Result, error) { return r.serverInfo(), nil }},
		{Name: "agentdock_context", Title: "AgentDock context", Description: "Return AgentDock bootstrap context including available capabilities, integrations, rules, and high-priority context for clients that cannot inject system prompt context.", Handler: ctxToolHandler((*Runtime).agentDockContextTool)},
		{Name: "read_file", Title: "Read file", Description: toolfile.ToolDescription("Read a UTF-8 text file slice. Supports normal Host paths and skill://<name>/<path> resources from the active Skill version."), Handler: ctxToolHandler((*Runtime).readFile)},
		{Name: "list_dir", Title: "List directory", Description: toolfile.ToolDescription("List directory entries. Relative paths resolve from ~/AgentDock; absolute and ~/ paths use Host rules."), Handler: ctxToolHandler((*Runtime).listDir)},
		{Name: "list_files", Title: "List files", Description: toolfile.ToolDescription("List files using glob and ignore filters. Relative paths resolve from ~/AgentDock by default."), Handler: ctxToolHandler((*Runtime).listFiles)},
		{Name: "search_text", Title: "Search text", Description: toolfile.ToolDescription("Search UTF-8 files for text or regex matches. Relative paths search ~/AgentDock by default; absolute paths are allowed."), Handler: ctxToolHandler((*Runtime).searchText)},
		{Name: "file_edit", Title: "Edit file", Description: toolfile.EditDescription("Edit files through one action-based entrypoint: replace, patch, add, delete, or move. Relative paths resolve from ~/AgentDock; absolute and ~/ paths use Host rules."), Handler: ctxToolHandler((*Runtime).fileEdit)},
		{Name: "exec_command", Title: "Run command", Description: toolcommand.Description(), Handler: ctxToolHandler((*Runtime).execCommand)},
		{Name: "session_observe", Title: "Observe command sessions", Description: "List or inspect command sessions through a read-only session tool.", Handler: toolHandler((*Runtime).sessionObserve)},
		{Name: "session_act", Title: "Act on command sessions", Description: "Write to or stop command sessions through a mutating session tool.", Handler: toolHandler((*Runtime).sessionAct)},
		{Name: "task_manage", Title: "Manage recoverable tasks", Description: "Persist substantial AgentDock tasks and update live step progress with checkpoint.", Handler: ctxToolHandler((*Runtime).taskManage)},
		{Name: "evolve", Title: "Evolve AgentDock knowledge", Description: "Propose reusable knowledge, pre-bind Task learning checks, supersede, or retract. Bind must happen before execution and declares on_success/on_failure semantics; AgentDock resolves later Task outcomes and owns lifecycle policy while Recall only persists the result.", Availability: requiresNexus, Handler: ctxToolHandler((*Runtime).evolve)},
		{Name: "acp_session", Title: "Manage ACP sessions", Description: "Inspect or authenticate the configured ACP agent and create, load, resume, fork, configure, list, inspect, close, or delete persistent ACP sessions through one action-based entrypoint. Session workspaces may use any host-accessible directory and optional methods are capability-gated.", Annotations: mutatingToolAnnotations(true, true), Availability: requiresACP, Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.acp.Session(ctx, args)
		}},
		{Name: "acp_prompt", Title: "Run ACP prompts", Description: "Start asynchronous ACP prompt turns, poll ordered session events, steer a running turn, or cancel a turn. start returns immediately with a run_id; use action=events for bounded long-poll observation.", Annotations: mutatingToolAnnotations(true, true), Availability: requiresACP, Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.acp.Prompt(ctx, args)
		}},
		{Name: "acp_interaction", Title: "Handle ACP interactions", Description: "List, inspect, respond to, or cancel pending ACP permission interactions. Only options offered by the agent and permitted by the local AgentDock policy may be selected.", Annotations: mutatingToolAnnotations(true, true), Availability: requiresACP, Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.acp.Interaction(ctx, args)
		}},
		{Name: "workflow_template_manage", Title: "Manage workflow templates", Description: "List, get, get multiple, save, validate, publish, retire, or match AgentDock workflow templates. get_many requires the model to compose the returned templates before task creation.", Availability: requiresNexus, Handler: ctxToolHandler((*Runtime).workflowTemplateManage)},
		{Name: "skill_package", Title: "Manage Skill packages", Description: "Validate, install, activate, or roll back AgentDock Skill packages and manage each Skill's isolated environment without returning secret values.", Handler: ctxToolHandler((*Runtime).skillPackage)},
		{Name: "mcp_manage", Title: "Manage dynamic MCP servers", Description: "Register, inspect, enable, disable, refresh, remove, or manage the isolated environment of dynamic MCP servers. Dynamic MCP tools remain separate from AgentDock built-in tools.", Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.dynamicMCP.Manage(ctx, args)
		}},
		{Name: "mcp_tool_search", Title: "Search dynamic MCP tools", Description: "Search lightweight tool summaries from enabled dynamic MCP servers. Use a server name from agentdock_context when possible.", Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.dynamicMCP.Search(ctx, args)
		}},
		{Name: "mcp_tool_inspect", Title: "Inspect a dynamic MCP tool", Description: "Read the complete schema for one dynamic MCP tool identified as <server>:<tool>.", Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.dynamicMCP.Inspect(ctx, args)
		}},
		{Name: "mcp_tool_call", Title: "Call a dynamic MCP tool", Description: "Call one previously discovered dynamic MCP tool identified as <server>:<tool>. Arguments are validated against the discovered tool schema before forwarding.", Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.dynamicMCP.Call(ctx, args)
		}},
		{Name: "view_image", Title: "View image", Description: "Load an image by AgentDock artifact_id, Host path, or HTTP(S) URL and return it as standard MCP image content.", Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.media.ViewImage(ctx, args)
		}},
		{Name: "recall_bootstrap", Title: "Bootstrap NexusDock Recall context", Description: "Load high-priority NexusDock Recall context at the start of substantial AgentDock, project, deployment, debugging, or preference-sensitive tasks. max_bytes controls pack budget only; compact index/excerpt output is default, and full body requires include_body or targeted recall_read.", Availability: requiresNexus, Handler: ctxToolHandler((*Runtime).recallBootstrap)},
		{Name: "recall_search", Title: "Search NexusDock Recall", Description: "Search NexusDock Recall Markdown documents and cards. Use kind=all, markdown, or card; backend routing such as card prefixes stays internal.", Availability: requiresNexus, Handler: ctxToolHandler((*Runtime).recallSearch)},
		{Name: "recall_read", Title: "Read NexusDock Recall entry", Description: "Read one Markdown document or card from the configured NexusDock Recall store by path.", Availability: requiresNexus, Handler: ctxToolHandler((*Runtime).recallRead)},
		{Name: "recall_write", Title: "Write NexusDock Recall entry", Description: "Plan, create, replace, append, patch, update facts, diff, or delete NexusDock Recall content. The model must choose target=card/markdown and action explicitly.", Availability: requiresNexus, Handler: ctxToolHandler((*Runtime).recallWrite)},
		{Name: "recall_maintain", Title: "Maintain NexusDock Recall", Description: "Run NexusDock Recall maintenance actions such as sync_status, list, lint, embedding_status, reindex, or reindex_cards.", Availability: requiresNexus, Handler: ctxToolHandler((*Runtime).recallMaintain)},
		{Name: "private_note_manage", Title: "Manage private notes", Description: "Explicit low-frequency NexusDock private note vault entrypoint. Do not use by default: use only when the user explicitly requests private note access or the content clearly contains sensitive secrets, credentials, or personal information. Search is metadata-only; plaintext is returned only by explicit read, and Git backups contain age ciphertext only. Actions: search, read, write, delete, status, or maintain.", Handler: ctxToolHandler((*Runtime).privateNoteManage), Availability: requiresNexus},
		{Name: "browser_session", Title: "Browser session", Description: "Start an AgentDock-owned Chromium-family browser or attach to an existing CDP browser with a dedicated AgentDock target, then close or clean up the session. External browsers remain running when the session closes.", Availability: requiresBrowser, Handler: ctxToolHandler((*Runtime).browserSession)},
		{Name: "browser_act", Title: "Browser actions", Description: "Run strictly validated CSS/CDP browser actions against an AgentDock-managed browser target and return the final typed page snapshot plus screenshot Artifact.", Availability: requiresBrowser, Handler: ctxToolHandler((*Runtime).browserAct)},
		{Name: "browser_snapshot", Title: "Browser snapshot", Description: "Capture the active or requested CDP target with page text, viewport, page size, focus, visible interactive elements, diagnostics, and a PNG screenshot Artifact.", Availability: requiresBrowser, Handler: ctxToolHandler((*Runtime).browserSnapshot)},
		{Name: "file_publish", Title: "Publish signed file", Description: "Publish a local file or directory as an immutable Artifact snapshot under ~/.agentdock/public-artifacts. Returns artifact_id and, when a reachable base URL is available, a temporary signed download URL. Directories are packaged as tar.gz.", FileArgRewritePaths: []string{"file"}, Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
			return r.media.FilePublish(ctx, args)
		}},
	}
	specs = append(specs, openCodeToolSpecs()...)
	return bindToolSchemas(specs)
}

func openCodeToolSpecs() []ToolSpec {
	definitions := []struct {
		name        string
		title       string
		description string
		destructive bool
	}{
		{name: "bash", title: "OpenCode bash", description: "Run the verified OpenCode model-free bash tool inside an explicit workspace runtime.", destructive: true},
		{name: "read", title: "OpenCode read", description: "Read files through the verified OpenCode model-free read tool inside an explicit workspace runtime."},
		{name: "glob", title: "OpenCode glob", description: "Find files through the verified OpenCode model-free glob tool inside an explicit workspace runtime."},
		{name: "grep", title: "OpenCode grep", description: "Search text through the verified OpenCode model-free grep tool inside an explicit workspace runtime."},
		{name: "edit", title: "OpenCode edit", description: "Edit files through the verified OpenCode model-free edit tool inside an explicit workspace runtime.", destructive: true},
		{name: "write", title: "OpenCode write", description: "Write files through the verified OpenCode model-free write tool inside an explicit workspace runtime.", destructive: true},
		{name: "apply_patch", title: "OpenCode apply patch", description: "Apply a patch through the verified OpenCode model-free apply_patch tool inside an explicit workspace runtime.", destructive: true},
		{name: "lsp", title: "OpenCode LSP", description: "Query locally available language-server functionality through the verified OpenCode model-free lsp tool; runtime downloads remain disabled."},
		{name: "git", title: "OpenCode Git", description: "Use the verified OpenCode structured Git tool inside an explicit workspace runtime.", destructive: true},
	}
	result := make([]ToolSpec, 0, len(definitions))
	for _, definition := range definitions {
		remoteTool := definition.name
		result = append(result, ToolSpec{
			Name: "opencode_" + remoteTool, Title: definition.title, Description: definition.description,
			Annotations: mutatingToolAnnotations(definition.destructive, false), Availability: requiresOpenCode,
			Handler: func(ctx context.Context, r *Runtime, args map[string]any) (Result, error) {
				return r.openCodeTool(ctx, remoteTool, args)
			},
		})
	}
	return result
}

func bindToolSchemas(specs []ToolSpec) []ToolSpec {
	for i := range specs {
		name := specs[i].Name
		if specs[i].InputSchema == nil {
			specs[i].InputSchema = func() map[string]any { return InputSchema(name) }
		}
		if specs[i].OutputSchema == nil {
			specs[i].OutputSchema = func() map[string]any { return OutputSchema(name) }
		}
	}
	return specs
}
