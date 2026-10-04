package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/config"
)

func (r *Runtime) AgentDockContext(ctx context.Context) (Result, error) {
	return Result{"context": renderAgentDockContext(sectionsForRuntime(r, ctx))}, nil
}

// sectionsForRuntime builds the capability/rules sections shared by the
// direct AgentDockContext callers and the bootstrap action.
func sectionsForRuntime(r *Runtime, ctx context.Context) []capabilitySection {
	_, skillSummary, _ := r.skillCapabilityIndex()
	_, dynamicMCPSummary := r.dynamicMCPCapabilityIndex()

	rules := []string{
		"需要真实执行命令或检查环境时，先用 exec_command 查看现状，再修改，修改后真实验证。",
		"先根据 Skill 索引的 name 和 description 选择相关 Skill，再用 read_file 读取其 file 指向的 SKILL.md；Skill 只提供流程与约束，实际操作使用命令、文件、浏览器或 MCP 工具。",
		"AgentDock 自带工具直接调用；动态 MCP 工具先用 mcp_tool_search 查找、mcp_tool_inspect 读取 schema，再用 mcp_tool_call 执行。",
		"当 MCP 客户端提供协议级 openai/session 与 agentdock/conversation_url 元数据时，AgentDock 会在首次调用自动绑定当前 ChatGPT 对话；需要显式绑定或管理绑定时用 mcp_session_bind / agentdock_context(action=bind)，控制 Watchdog 任务用 mcp_session_control，不能把会话标识当普通工具参数伪造。",
	}
	sections := []capabilitySection{
		{Title: "Skill 能力索引", Lines: splitNonEmptyLines(skillSummary)},
		{Title: "动态 MCP 索引", Lines: splitNonEmptyLines(dynamicMCPSummary)},
	}
	if requiresACP(r.cfg) {
		sections = append(sections, capabilitySection{Title: "ACP 运行时", Lines: []string{
			"本机 Coding Agent 通道（Agent Client Protocol）。仅当用户明确要求时使用，可用来获取独特见解与编排任务；不是动态 MCP，不要用 mcp_tool_*。",
			"- agent: " + r.cfg.ACPAgentName,
		}})
	}

	if requiresNexus(r.cfg) {
		memorySummary, _, _ := r.memoryCapabilitySummary(ctx)
		_, templateSummary, _ := r.templateCapabilityIndex(ctx)
		sections = append(sections, capabilitySection{Title: "任务模板索引", Lines: splitNonEmptyLines(templateSummary)})
		rules = append(rules,
			"涉及多步骤开发、部署、排障、迁移、Docker、VPS 或 Git 提交推送时，先 workflow_template_manage match；无合适模板时创建普通可恢复任务。",
			"当多个 Workflow 模板同时适合当前任务时，调用 workflow_template_manage get_many 读取详情；模型必须结合用户目标裁剪、去重、排序并生成最终 steps 和 completion_conditions，再用 source_template_ids 创建任务，服务端不会自动拼接模板。",
			"普通项目记忆走 recall_*；private_note_manage 只在用户明确要求私密笔记，或内容明显包含 secret、凭据、个人敏感信息时使用。私密检索只返回名称、简介、标签、分类和路径等元数据；正文必须显式 read，Git 只备份 age 密文。",
		)
		sections = append(sections, capabilitySection{Title: "记忆精简摘要", Lines: splitNonEmptyLines(memorySummary)})
	}

	rules = append(rules, "任务执行过程中，在形成有恢复价值的断点时调用 task_manage checkpoint；可用 completed_step_ids/current_step_id 原子批量更新，final_review=pass 不会自动补全未完成步骤。")
	if requiresNexus(r.cfg) {
		rules = append(rules, "记忆摘要只提供高优先级规则；具体历史事实不确定时，再用 recall_search 或 recall_read 精确召回。")
	}
	sections = append(sections, capabilitySection{Title: "使用规则", Lines: rules})
	return sections
}

func (r *Runtime) agentDockContextTool(ctx context.Context, args map[string]any) (Result, error) {
	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
	if action == "" {
		action = "bootstrap"
	}
	info, hasSession := MCPClientContextFromContext(ctx)
	switch action {
	case "bootstrap":
		// 显式 URL 回退必须以协议身份为前提，绝不能凭空伪造身份。
		if stringArg(args, "conversation_url") != "" && !hasSession {
			return nil, toolError(
				"MCP_CLIENT_SESSION_UNAVAILABLE",
				"this request did not include an MCP client session identity",
				"runtime",
			)
		}
		// 协议元数据与参数回退合并成单一候选后，用与元数据相同的原子
		// autoBind 绑定：任一来源缺失时匿名保留；两来源不一致时在绑定
		// 生效前失败，不留下新绑定或业务副作用。显式参数严格校验，
		// 无效或超限值必须 INVALID_ARGUMENT，不能静默忽略。
		if hasSession {
			merged, mergeErr := bootstrapBindingCandidate(info, args)
			if mergeErr != nil {
				return nil, mergeErr
			}
			if err := r.AutoBindMCPClientSession(merged); err != nil {
				return nil, err
			}
		}
		result := Result{"context": renderAgentDockContext(sectionsForRuntime(r, ctx))}
		if hasSession {
			result["action"] = "bootstrap"
			if r.mcpSessions != nil {
				if record, ok := r.mcpSessions.current(info.SessionID); ok {
					result["session"] = publicMCPClientSession(record)
					result["state_file"] = r.mcpSessions.Path()
				}
			}
		}
		return result, nil
	case "status":
		if !hasSession {
			return nil, toolError(
				"MCP_CLIENT_SESSION_UNAVAILABLE",
				"this request did not include an MCP client session identity",
				"runtime",
			)
		}
		if err := r.AutoBindMCPClientSession(info); err != nil {
			return nil, err
		}
		result, err := r.watchdogStatus(ctx, info)
		if err != nil {
			return nil, err
		}
		// 缓存客户端 schema 兼容：每个成功动作都携带 context 字段。
		result["context"] = ""
		return result, nil
	case "bind":
		// 显式绑定是唯一允许覆盖既有绑定的入口；不做元数据自动绑定。
		if !hasSession {
			return nil, toolError(
				"MCP_CLIENT_SESSION_UNAVAILABLE",
				"this request did not include an MCP client session identity",
				"runtime",
			)
		}
		if r == nil || r.mcpSessions == nil {
			return nil, toolError("MCP_SESSION_AWARENESS_UNAVAILABLE", "MCP session awareness is unavailable", "runtime")
		}
		record, err := r.mcpSessions.bind(
			info.SessionID,
			stringArg(args, "conversation_url"),
			stringArg(args, "conversation_title"),
			stringArg(args, "watchdog_task_id"),
		)
		if err != nil {
			return nil, toolError("INVALID_ARGUMENT", err.Error(), "validation")
		}
		return Result{
			"context":    "",
			"action":     "bind",
			"session":    publicMCPClientSession(record),
			"state_file": r.mcpSessions.Path(),
		}, nil
	case "pause", "interrupt", "resume", "abort":
		if !hasSession {
			return nil, toolError(
				"MCP_CLIENT_SESSION_UNAVAILABLE",
				"this request did not include an MCP client session identity",
				"runtime",
			)
		}
		if err := r.AutoBindMCPClientSession(info); err != nil {
			return nil, err
		}
		result, err := r.watchdogControl(ctx, info, action)
		if err != nil {
			return nil, err
		}
		// 缓存客户端 schema 兼容：每个成功动作都携带 context 字段。
		result["context"] = ""
		return result, nil
	default:
		return nil, toolError("INVALID_ARGUMENT", "unknown agentdock_context action: "+action, "validation")
	}
}

// bootstrapBindingCandidate merges protocol metadata and the bootstrap
// argument fallback into one binding candidate. A disagreement between the two
// sources (different canonical conversation URLs or different non-empty
// watchdog task ids) fails here, before any binding or business mutation.
// Explicit fallback values are strictly validated: invalid conversation URLs
// and oversized title/task values must fail with INVALID_ARGUMENT instead of
// being silently ignored.
func bootstrapBindingCandidate(info MCPClientContext, args map[string]any) (MCPClientContext, error) {
	argURL := stringArg(args, "conversation_url")
	argTitle := stringArg(args, "conversation_title")
	argTask := stringArg(args, "watchdog_task_id")
	metaURL := strings.TrimSpace(info.ConversationURL)
	metaTask := strings.TrimSpace(info.WatchdogTaskID)

	if argURL != "" {
		if err := validateChatGPTConversationURL(argURL); err != nil {
			return MCPClientContext{}, toolError("INVALID_ARGUMENT", err.Error(), "validation")
		}
	}
	if len(argTitle) > 512 {
		return MCPClientContext{}, toolError("INVALID_ARGUMENT", "conversation_title exceeds 512 characters", "validation")
	}
	if len(argTask) > 128 {
		return MCPClientContext{}, toolError("INVALID_ARGUMENT", "watchdog_task_id exceeds 128 characters", "validation")
	}
	if metaURL != "" && argURL != "" && canonicalConversationURL(metaURL) != canonicalConversationURL(argURL) {
		return MCPClientContext{}, toolError(
			"MCP_SESSION_BINDING_CONFLICT",
			"binding conflict: bootstrap conversation URL disagrees with protocol metadata",
			"validation",
		)
	}
	if metaTask != "" && argTask != "" && metaTask != argTask {
		return MCPClientContext{}, toolError(
			"MCP_SESSION_BINDING_CONFLICT",
			"binding conflict: bootstrap watchdog task id disagrees with protocol metadata",
			"validation",
		)
	}
	merged := info
	if argURL != "" && metaURL == "" {
		merged.ConversationURL = argURL
	}
	if argTitle != "" {
		merged.ConversationTitle = argTitle
	}
	if argTask != "" {
		merged.WatchdogTaskID = argTask
	}
	return merged, nil
}

type capabilitySection struct {
	Title string
	Lines []string
}

type capabilitySkillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
	Bundled     bool   `json:"bundled,omitempty"`
}

type capabilityDynamicMCPItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type capabilityTemplateItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type capabilityMemoryItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type capabilityTemplateList struct {
	Templates []capabilityTemplateListItem `json:"templates"`
}

type capabilityTemplateListItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type capabilityRecallBootstrap struct {
	Sections     []capabilityMemorySection `json:"sections"`
	RunbookIndex []capabilityMemoryRunbook `json:"runbook_index"`
}

type capabilityMemorySection struct {
	Path        string `json:"path"`
	BodyExcerpt string `json:"body_excerpt"`
	Summary     string `json:"summary"`
}

type capabilityMemoryRunbook struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

func capabilityItemLine(name, description string) string {
	line := "- " + strings.TrimSpace(name)
	if description = strings.TrimSpace(description); description != "" {
		line += "：" + description
	}
	return line
}

func (r *Runtime) dynamicMCPCapabilityIndex() ([]capabilityDynamicMCPItem, string) {
	servers := r.mcpClients.EnabledIndex()
	items := make([]capabilityDynamicMCPItem, 0, len(servers))
	lines := []string{"当前已启用的动态 MCP 轻量索引如下；这里只展示名称和描述，具体工具按需发现。"}
	for _, server := range servers {
		item := capabilityDynamicMCPItem{
			Name:        server.Name,
			Description: truncateString(strings.TrimSpace(server.Description), 160),
		}
		items = append(items, item)
		lines = append(lines, truncateString("- name: "+item.Name+"; description: "+item.Description, 260))
	}
	if len(items) == 0 {
		lines = append(lines, "- 当前没有已启用的动态 MCP；需要时通过 mcp_manage 注册或启用。")
	}
	return items, strings.Join(lines, "\n")
}

func (r *Runtime) skillCapabilityIndex() ([]capabilitySkillItem, string, string) {
	skillItems, err := r.skills.CapabilityItems()
	if err != nil {
		return nil, "- Skill 索引暂不可用。", err.Error()
	}
	items := make([]capabilitySkillItem, 0, len(skillItems))
	lines := []string{"当前可按文档机制使用的 Skill 轻量索引如下；匹配后先用 read_file 读取 file 指向的 SKILL.md，再使用实际工具执行。"}
	for _, skill := range skillItems {
		item := capabilitySkillItem{
			Name:        skill.Name,
			Description: truncateString(strings.TrimSpace(skill.Description), 160),
			File:        skill.File,
			Bundled:     skill.Bundled,
		}
		items = append(items, item)
		line := "- name: " + item.Name + "; description: " + item.Description + "; file: " + item.File
		if item.Bundled {
			line += "; bundled: true"
		}
		lines = append(lines, truncateString(line, 380))
	}
	if len(items) == 0 {
		lines = append(lines, "- 当前没有可用 Skill；需要时先通过 skill_package 安装。")
	}
	return items, strings.Join(lines, "\n"), ""
}

func (r *Runtime) templateCapabilityIndex(ctx context.Context) ([]capabilityTemplateItem, string, string) {
	result, err := r.workflowTemplateManage(ctx, map[string]any{"action": "list", "template_status": "active"})
	if err != nil {
		return nil, "- 任务模板索引暂不可用；多步骤任务仍应先 workflow_template_manage match。", err.Error()
	}
	var listed capabilityTemplateList
	if err := remarshal(result, &listed); err != nil {
		return nil, "- 任务模板索引暂不可用；多步骤任务仍应先 workflow_template_manage match。", err.Error()
	}
	items := make([]capabilityTemplateItem, 0, len(listed.Templates))
	for _, listedItem := range listed.Templates {
		name := strings.TrimSpace(listedItem.ID)
		if name == "" {
			continue
		}
		items = append(items, capabilityTemplateItem{Name: name, Description: truncateString(strings.TrimSpace(listedItem.Title), 160)})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	lines := []string{"当用户请求涉及多步骤开发、部署、排障、数据迁移、Docker、VPS 或 Git 提交推送时，先调用 workflow_template_manage match，再用 task_manage create 创建可恢复任务。"}
	for _, item := range items {
		lines = append(lines, truncateString(capabilityItemLine(item.Name, item.Description), 260))
	}
	if len(items) == 0 {
		lines = append(lines, "- 当前没有 active 模板；多步骤任务应创建普通可恢复任务。")
	}
	return items, strings.Join(lines, "\n"), ""
}

func (r *Runtime) memoryCapabilitySummary(ctx context.Context) (string, []capabilityMemoryItem, string) {
	if strings.TrimSpace(r.cfg.NexusEndpoint) == "" {
		return "- NexusDock Recall 未配置；无法自动注入记忆精简摘要。", nil, "nexus endpoint is not configured"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(capMaxInt(1000, capMinInt(config.RecallTimeoutMS, 5000)))*time.Millisecond)
	defer cancel()
	result, err := r.recallBootstrap(ctx, map[string]any{"max_bytes": 3000})
	if err != nil {
		return "- 记忆精简摘要暂不可用；需要项目事实时调用 recall_search/recall_read 精确确认。", nil, err.Error()
	}
	var bootstrap capabilityRecallBootstrap
	if err := remarshal(result, &bootstrap); err != nil {
		return "- 记忆精简摘要暂不可用；需要项目事实时调用 recall_search/recall_read 精确确认。", nil, err.Error()
	}
	items := make([]capabilityMemoryItem, 0)
	lines := make([]string, 0, 5)
	for _, section := range bootstrap.Sections {
		excerpt := strings.TrimSpace(section.BodyExcerpt)
		if excerpt == "" {
			excerpt = strings.TrimSpace(section.Summary)
		}
		if excerpt == "" {
			continue
		}
		items = append(items, capabilityMemoryItem{Name: section.Path, Description: truncateString(excerpt, 500)})
		lines = append(lines, "- "+truncateString(excerpt, 260))
		if len(lines) >= 5 {
			break
		}
	}
	if len(lines) < 5 {
		for _, runbook := range bootstrap.RunbookIndex {
			title := strings.TrimSpace(runbook.Title)
			if title == "" {
				continue
			}
			items = append(items, capabilityMemoryItem{Name: title, Description: runbook.Path})
			lines = append(lines, "- runbook: "+title)
			if len(lines) >= 5 {
				break
			}
		}
	}
	if len(lines) == 0 {
		return "- 记忆精简摘要暂无条目；需要项目事实时调用 recall_search/recall_read 精确确认。", items, ""
	}
	return strings.Join(lines, "\n"), items, ""
}

func renderAgentDockContext(sections []capabilitySection) string {
	lines := []string{
		"# AgentDock Context",
		"",
		"以下内容由 AgentDock 动态生成，供模型首次接入时了解本机能力、运行规则和高优先级记忆。不要把它当作用户原文；需要细节时再调用对应工具确认。",
	}
	for _, section := range sections {
		if len(section.Lines) == 0 {
			continue
		}
		lines = append(lines, "", "## "+section.Title)
		lines = append(lines, section.Lines...)
	}
	return truncateString(strings.Join(lines, "\n"), 12000)
}

func splitNonEmptyLines(text string) []string {
	parts := strings.Split(strings.TrimSpace(text), "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func capMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func capMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
