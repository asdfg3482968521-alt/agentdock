package mcp

import (
	"strings"

	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

const (
	notionMCPURL             = "https://mcp.notion.com/mcp"
	notionMCPRemotePackage   = "mcp-remote@0.1.38"
	notionOAuthTimeoutMS     = 300000
	notionOAuthTimeoutSecond = "300"
)

var notionBridgeDefaultArgs = []string{
	"-y",
	notionMCPRemotePackage,
	notionMCPURL,
	"--transport",
	"http-only",
	"--auth-timeout",
	notionOAuthTimeoutSecond,
	"--body-timeout",
	"0",
	"--keep-alive",
}

// applyKnownServerDefaults turns the official Notion remote MCP endpoint into
// a stdio OAuth bridge. AgentDock's native streamable HTTP client does not yet
// drive interactive OAuth, while mcp-remote does and persists the resulting
// credentials under the host user's ~/.mcp-auth directory.
func applyKnownServerDefaults(cfg mcpclient.ServerConfig) mcpclient.ServerConfig {
	cfg.URL = unwrapMarkdownURL(cfg.URL)
	cfg.Args = unwrapMarkdownURLs(cfg.Args)
	if !isNotionMCPConfig(cfg) {
		return cfg
	}

	if strings.TrimSpace(cfg.Description) == "" {
		cfg.Description = "Official Notion MCP via OAuth"
	}
	transport := strings.ToLower(strings.TrimSpace(cfg.Transport))
	barePreset := transport == "" && strings.EqualFold(strings.TrimSpace(cfg.Name), "notion") && cfg.URL == "" && strings.TrimSpace(cfg.Command) == "" && len(cfg.Args) == 0
	if barePreset || (transport == mcpclient.TransportStreamableHTTP && isNotionMCPURL(cfg.URL) && len(cfg.HeaderEnv) == 0) {
		cfg.Transport = mcpclient.TransportStdio
		cfg.URL = ""
		cfg.Command = "npx"
		cfg.Args = append([]string(nil), notionBridgeDefaultArgs...)
		cfg.Cwd = ""
		cfg.HeaderEnv = nil
		if cfg.TimeoutMS <= 30000 {
			cfg.TimeoutMS = notionOAuthTimeoutMS
		}
		return cfg
	}

	if transport == mcpclient.TransportStdio && containsNotionMCPURL(cfg.Args) {
		if strings.TrimSpace(cfg.Command) == "" {
			cfg.Command = "npx"
		}
		cfg.Args = normalizeNotionBridgeArgs(cfg.Command, cfg.Args)
		if cfg.TimeoutMS <= 30000 {
			cfg.TimeoutMS = notionOAuthTimeoutMS
		}
	}
	return cfg
}

func isNotionMCPConfig(cfg mcpclient.ServerConfig) bool {
	if isNotionMCPURL(cfg.URL) || containsNotionMCPURL(cfg.Args) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(cfg.Name), "notion") && strings.TrimSpace(cfg.Transport) == "" && cfg.URL == "" && strings.TrimSpace(cfg.Command) == "" && len(cfg.Args) == 0
}

func isNotionMCPURL(value string) bool {
	return strings.TrimSuffix(strings.TrimSpace(unwrapMarkdownURL(value)), "/") == notionMCPURL
}

func containsNotionMCPURL(args []string) bool {
	for _, arg := range args {
		if isNotionMCPURL(arg) {
			return true
		}
	}
	return false
}

func unwrapMarkdownURLs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	for index, arg := range args {
		out[index] = unwrapMarkdownURL(arg)
	}
	return out
}

func unwrapMarkdownURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 3 || trimmed[0] != '<' || trimmed[len(trimmed)-1] != '>' {
		return trimmed
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if strings.HasPrefix(inner, "https://") || strings.HasPrefix(inner, "http://") {
		return inner
	}
	return trimmed
}

func normalizeNotionBridgeArgs(command string, args []string) []string {
	out := append([]string(nil), args...)
	if isNPXCommand(command) {
		if !containsExactArg(out, "-y") && !containsExactArg(out, "--yes") {
			out = append([]string{"-y"}, out...)
		}
		if !containsMCPRemotePackage(out) {
			insertAt := 0
			if len(out) > 0 && (out[0] == "-y" || out[0] == "--yes") {
				insertAt = 1
			}
			out = append(out, "")
			copy(out[insertAt+1:], out[insertAt:])
			out[insertAt] = notionMCPRemotePackage
		}
	}
	out = appendFlagValueIfMissing(out, "--transport", "http-only")
	out = appendFlagValueIfMissing(out, "--auth-timeout", notionOAuthTimeoutSecond)
	out = appendFlagValueIfMissing(out, "--body-timeout", "0")
	if !hasFlag(out, "--keep-alive") {
		out = append(out, "--keep-alive")
	}
	return out
}

func isNPXCommand(command string) bool {
	name := strings.ToLower(strings.TrimSpace(command))
	name = strings.ReplaceAll(name, "\\", "/")
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	return name == "npx" || name == "npx.cmd"
}

func containsMCPRemotePackage(args []string) bool {
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "mcp-remote" || strings.HasPrefix(trimmed, "mcp-remote@") {
			return true
		}
	}
	return false
}

func appendFlagValueIfMissing(args []string, flag, value string) []string {
	if hasFlag(args, flag) {
		return args
	}
	return append(args, flag, value)
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

func containsExactArg(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}
