//go:build windows

package desktopruntime

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentconfig "github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/securedata"
)

var managedCoreEnvironment = []string{
	"AGENTDOCK_AUTH_TOKEN",
	"AGENTDOCK_HOST",
	"AGENTDOCK_PORT",
	"AGENTDOCK_LOG_LEVEL",
	"AGENTDOCK_NEXUS_ENDPOINT",
	"AGENTDOCK_NEXUS_TOKEN",
	"AGENTDOCK_BROWSER_ENABLED",
	"AGENTDOCK_BROWSER_CDP_URL",
	"AGENTDOCK_BROWSER_REUSE_EXISTING_CDP",
	"AGENTDOCK_ACP_ENABLED",
	"AGENTDOCK_ACP_AGENT",
	"AGENTDOCK_ACP_COMMAND",
	"AGENTDOCK_ACP_ARGS_JSON",
	"AGENTDOCK_ACP_ENV_FROM_ENV_JSON",
	"AGENTDOCK_ACP_ALLOWED_ROOTS",
	"AGENTDOCK_ACP_MAX_CONCURRENT_PROMPTS",
	"AGENTDOCK_ACP_INTERACTION_TIMEOUT_MS",
	"AGENTDOCK_SERVER_URL",
	"AGENTDOCK_OAUTH_ENABLED",
	"AGENTDOCK_OAUTH_PASSWORD",
	"AGENTDOCK_OAUTH_TOKEN_SECRET",
	"AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL",
}

type controlPanelSettings struct {
	Port                    int      `json:"port"`
	LogLevel                string   `json:"log_level"`
	NexusEndpoint           string   `json:"nexus_endpoint"`
	OAuthAccessTokenTTL     string   `json:"oauth_access_token_ttl,omitempty"`
	BrowserEnabled          bool     `json:"browser_enabled"`
	BrowserCDPURL           string   `json:"browser_cdp_url"`
	BrowserReuseExistingCDP bool     `json:"browser_reuse_existing_cdp"`
	ACPEnabled              bool     `json:"acp_enabled"`
	ACPAgent                string   `json:"acp_agent"`
	ACPCommand              string   `json:"acp_command"`
	ACPArgs                 []string `json:"acp_args"`
}

// resolveOAuthServerURL 解析核心启动时应使用的公网 OAuth Origin。
//
// 优先读取 server-url.txt；配置了 Tunnel（quick/named）时若该文件为空，
// 回退到 named-server-url.txt 中的固定域名地址——Tunnel 重启/重新生成期间
// server-url.txt 可能处于短暂的空窗。两者皆空时：
//   - 强制 OAuth（forceOAuth，来自 AGENTDOCK_OAUTH_REQUIRED）：无论 Tunnel 模式如何
//     都直接报错，覆盖使用 ngrok 等外部隧道（mode=none）但要求 OAuth 常开的场景；
//   - 已配置 Tunnel：直接报错，拒绝静默降级为仅 Bearer（OAuth 消失事故根因）；
//   - 未配置 Tunnel 且未强制（none）：返回空字符串，保持本地仅 Bearer 的桌面默认行为。
func resolveOAuthServerURL(runtime tunnelRuntime, forceOAuth bool) (string, error) {
	serverURL, err := readTrimmedText(runtime.files.serverURL)
	if err != nil {
		return "", err
	}
	if serverURL == "" && runtime.mode != "none" {
		serverURL, err = readTrimmedText(runtime.files.namedServerURL)
		if err != nil {
			return "", err
		}
	}
	if serverURL == "" {
		if forceOAuth {
			return "", fmt.Errorf("已启用强制 OAuth（AGENTDOCK_OAUTH_REQUIRED=true），但没有可用的公网地址（server-url.txt 与 named-server-url.txt 均为空），拒绝降级为仅 Bearer 启动；请先把公网地址写入运行时目录的 server-url.txt（例如 ngrok 地址 https://xxx.ngrok-free.dev），或用 agentdock tunnel configure 配置 Tunnel")
		}
		if runtime.mode != "none" {
			return "", fmt.Errorf("已配置 Tunnel（%s）但没有可用的公网地址（server-url.txt 与 named-server-url.txt 均为空），拒绝降级为仅 Bearer 启动；请修复 Tunnel 或用 agentdock tunnel configure 重新设置公网地址", runtime.mode)
		}
		return "", nil
	}
	return normalizeHTTPSOrigin(serverURL)
}

// oauthForced 报告是否启用了强制 OAuth（AGENTDOCK_OAUTH_REQUIRED）。
// 启用后任何 Tunnel 模式下都不允许静默降级为仅 Bearer 启动。
func oauthForced() bool {
	value := strings.TrimSpace(os.Getenv("AGENTDOCK_OAUTH_REQUIRED"))
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func platformPrepareCoreEnvironment(runtimeRoot string) error {
	manifest, root, err := loadDesktopManifest(runtimeRoot)
	if err != nil {
		return err
	}
	settings, err := loadControlPanelSettings(root, manifest.Port)
	if err != nil {
		return err
	}
	inheritedOAuthAccessTokenTTL := strings.TrimSpace(os.Getenv("AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL"))

	for _, name := range managedCoreEnvironment {
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("清理 %s 失败: %w", name, err)
		}
	}
	authToken, err := readProtectedText(filepath.Join(root, "auth-token.dpapi"), "agentdock.startup.v1")
	if err != nil {
		return fmt.Errorf("读取 Bearer Token 失败: %w", err)
	}
	if strings.TrimSpace(authToken) == "" {
		return errors.New("Bearer Token 为空，请运行 Setup.exe 修复安装")
	}

	managed := map[string]string{
		"AGENTDOCK_RUNTIME_ROOT":               root,
		"AGENTDOCK_AUTH_TOKEN":                 authToken,
		"AGENTDOCK_HOST":                       "127.0.0.1",
		"AGENTDOCK_PORT":                       strconv.Itoa(settings.Port),
		"AGENTDOCK_LOG_LEVEL":                  settings.LogLevel,
		"AGENTDOCK_BROWSER_ENABLED":            strconv.FormatBool(settings.BrowserEnabled),
		"AGENTDOCK_BROWSER_REUSE_EXISTING_CDP": strconv.FormatBool(settings.BrowserReuseExistingCDP),
		"AGENTDOCK_ACP_ENABLED":                strconv.FormatBool(settings.ACPEnabled),
	}
	if settings.BrowserCDPURL != "" {
		managed["AGENTDOCK_BROWSER_CDP_URL"] = settings.BrowserCDPURL
	}
	if settings.NexusEndpoint != "" {
		managed["AGENTDOCK_NEXUS_ENDPOINT"] = settings.NexusEndpoint
		if token, tokenErr := readOptionalProtectedText(filepath.Join(root, "nexus-token.dpapi"), "agentdock.nexus.token.v1"); tokenErr != nil {
			return fmt.Errorf("读取 Nexus Token 失败: %w", tokenErr)
		} else if token != "" {
			managed["AGENTDOCK_NEXUS_TOKEN"] = token
		}
	}
	if settings.ACPEnabled {
		info, statErr := os.Stat(settings.ACPCommand)
		if statErr != nil {
			return fmt.Errorf("读取 Coding Agent 命令失败 %s: %w", settings.ACPCommand, statErr)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Coding Agent 命令不是普通文件: %s", settings.ACPCommand)
		}
		argsJSON, marshalErr := json.Marshal(settings.ACPArgs)
		if marshalErr != nil {
			return fmt.Errorf("编码 Coding Agent 参数失败: %w", marshalErr)
		}
		managed["AGENTDOCK_ACP_AGENT"] = settings.ACPAgent
		managed["AGENTDOCK_ACP_COMMAND"] = settings.ACPCommand
		managed["AGENTDOCK_ACP_ARGS_JSON"] = string(argsJSON)
	}

	tunnelState, err := loadTunnelRuntime(runtimeRoot)
	if err != nil {
		return err
	}
	serverURL, err := resolveOAuthServerURL(tunnelState, oauthForced())
	if err != nil {
		return err
	}
	if serverURL != "" {
		// 写回 server-url.txt：既是规范化，也能在回退到 named-server-url.txt 时完成自愈。
		if err := writeRuntimeText(filepath.Join(root, "server-url.txt"), serverURL); err != nil {
			return err
		}
		oauthPassword, passwordErr := readProtectedText(filepath.Join(root, "oauth-password.dpapi"), "agentdock.oauth.password.v1")
		if passwordErr != nil {
			return fmt.Errorf("读取 OAuth 密码失败: %w", passwordErr)
		}
		oauthSecret, secretErr := readProtectedText(filepath.Join(root, "oauth-token-secret.dpapi"), "agentdock.oauth.secret.v1")
		if secretErr != nil {
			return fmt.Errorf("读取 OAuth 签名密钥失败: %w", secretErr)
		}
		managed["AGENTDOCK_SERVER_URL"] = serverURL
		managed["AGENTDOCK_OAUTH_ENABLED"] = "true"
		managed["AGENTDOCK_OAUTH_PASSWORD"] = oauthPassword
		managed["AGENTDOCK_OAUTH_TOKEN_SECRET"] = oauthSecret
	}
	oauthAccessTokenTTL := effectiveOAuthAccessTokenTTL(settings.OAuthAccessTokenTTL, inheritedOAuthAccessTokenTTL)
	if oauthAccessTokenTTL != "" {
		managed["AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL"] = oauthAccessTokenTTL
	}

	for name, value := range managed {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("设置 %s 失败: %w", name, err)
		}
	}
	return nil
}

func loadControlPanelSettings(runtimeRoot string, fallbackPort int) (controlPanelSettings, error) {
	settings := controlPanelSettings{Port: fallbackPort, LogLevel: "info", ACPAgent: "codex"}
	data, err := os.ReadFile(filepath.Join(runtimeRoot, "control-panel-settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return controlPanelSettings{}, fmt.Errorf("读取控制面板设置失败: %w", err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return controlPanelSettings{}, fmt.Errorf("解析控制面板设置失败: %w", err)
	}
	if settings.Port < 1 || settings.Port > 65535 {
		return controlPanelSettings{}, fmt.Errorf("控制面板端口超出范围: %d", settings.Port)
	}
	settings.LogLevel = strings.ToLower(strings.TrimSpace(settings.LogLevel))
	if settings.LogLevel == "" {
		settings.LogLevel = "info"
	}
	if settings.LogLevel != "debug" && settings.LogLevel != "info" && settings.LogLevel != "warn" && settings.LogLevel != "error" {
		return controlPanelSettings{}, fmt.Errorf("不支持的日志级别: %s", settings.LogLevel)
	}
	settings.NexusEndpoint = strings.TrimSpace(settings.NexusEndpoint)
	settings.OAuthAccessTokenTTL = strings.TrimSpace(settings.OAuthAccessTokenTTL)
	if settings.OAuthAccessTokenTTL != "" {
		if err := agentconfig.ValidateOAuthAccessTokenTTL(settings.OAuthAccessTokenTTL); err != nil {
			return controlPanelSettings{}, fmt.Errorf("OAuth Access Token 有效期无效: %w", err)
		}
	}
	settings.ACPAgent = strings.ToLower(strings.TrimSpace(settings.ACPAgent))
	if settings.ACPAgent == "" {
		settings.ACPAgent = "codex"
	}
	settings.ACPCommand = strings.TrimSpace(settings.ACPCommand)
	if settings.ACPCommand != "" {
		settings.ACPCommand = filepath.Clean(settings.ACPCommand)
	}
	if settings.ACPEnabled {
		switch settings.ACPAgent {
		case "codex", "claude", "grok":
		default:
			return controlPanelSettings{}, fmt.Errorf("不支持的 Coding Agent: %s", settings.ACPAgent)
		}
		if !filepath.IsAbs(settings.ACPCommand) {
			return controlPanelSettings{}, fmt.Errorf("Coding Agent 命令必须是绝对路径: %s", settings.ACPCommand)
		}
	}
	return settings, nil
}

func effectiveOAuthAccessTokenTTL(configured, inherited string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	return strings.TrimSpace(inherited)
}

func readOptionalProtectedText(path, entropy string) (string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return readProtectedText(path, entropy)
}

func readProtectedText(path, entropy string) (string, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return "", fmt.Errorf("解析 DPAPI 数据失败: %w", err)
	}
	if len(ciphertext) == 0 {
		return "", errors.New("DPAPI 数据为空")
	}
	plain, err := securedata.Unprotect(ciphertext, []byte(entropy))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func readTrimmedText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}
