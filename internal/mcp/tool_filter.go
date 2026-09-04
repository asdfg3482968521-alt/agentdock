package mcp

import (
	"os"
	"strings"
)

// mcpToolExposed 实现 AGENTDOCK_MCP_TOOLS_INCLUDE / AGENTDOCK_MCP_TOOLS_EXCLUDE 过滤。
//
// 背景：Notion 自定义 MCP 连接在保存连接时无法消化过大的 tools/list
// （完整目录 40+ 个工具、约 100KB 时连接超时失败）。用这两个环境变量可以把
// 暴露给 MCP 客户端的工具收敛到所需子集。过滤只发生在 MCP 注册层，
// 不影响 Runtime 注册表与 GPT Actions 通道。
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

func commaSeparatedSet(raw string) map[string]struct{} {
	values := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		if cleaned := strings.TrimSpace(part); cleaned != "" {
			values[cleaned] = struct{}{}
		}
	}
	return values
}
