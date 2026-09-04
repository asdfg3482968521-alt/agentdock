package mcp

import (
	"net/http"
	"os"
	"strings"
)

// mcpToolExposed 实现 AGENTDOCK_MCP_TOOLS_INCLUDE / AGENTDOCK_MCP_TOOLS_EXCLUDE 过滤。
//
// 背景：Notion 自定义 MCP 连接在保存连接时无法消化过大的 tools/list
// （完整目录 40+ 个工具、约 100KB 时连接超时失败）。用这两个环境变量可以把
// 暴露给 Notion 的工具收敛到所需子集。
//
// 重要：过滤只对识别为 Notion 的 MCP 客户端生效（见 isNotionMCPClient）。
// /mcp 端点对其余所有客户端始终提供完整目录；Runtime 注册表与
// GPT Actions 通道也不受影响。
//
// 规则：INCLUDE 非空时只暴露列出的工具；否则暴露除 EXCLUDE 外的全部工具。
// 值为逗号分隔的工具名，精确匹配，大小写敏感。
func mcpToolExposed(name string) bool {
	if include := commaSeparatedSet(os.Getenv("AGENTDOCK_MCP_TOOLS_INCLUDE")); len(include) > 0 {
		_, ok := include[name]
		return ok
	}
	_, excluded := commaSeparatedSet(os.Getenv("AGENTDOCK_MCP_TOOLS_EXCLUDE"))[name]
	return !excluded
}

// notionToolFilterConfigured 报告是否配置了任何过滤条件。
// 未配置时不构建 Notion 专用目录，/mcp 对所有客户端的行为与过滤功能引入前完全一致。
func notionToolFilterConfigured() bool {
	return strings.TrimSpace(os.Getenv("AGENTDOCK_MCP_TOOLS_INCLUDE")) != "" ||
		strings.TrimSpace(os.Getenv("AGENTDOCK_MCP_TOOLS_EXCLUDE")) != ""
}

// isNotionMCPClient 识别 Notion 的 MCP 客户端请求。任一命中即视为 Notion：
//   - 查询参数 client=notion（显式指定，最可靠：把 Notion 里的 MCP URL 配成 .../mcp?client=notion）
//   - User-Agent 包含 "notion"（不区分大小写，作为免配置的自动识别兜底）
func isNotionMCPClient(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("client")), "notion") {
		return true
	}
	return strings.Contains(strings.ToLower(r.UserAgent()), "notion")
}

func commaSeparatedSet(raw string) map[string]struct{} {
	values := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		if cleaned := strings.TrimSpace(part); cleaned != "" {
			values[cleaned] = struct{}{}
		}
	}
	return values
}
