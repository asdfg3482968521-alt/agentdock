# 私有 AgentDock（整合版）

> **注意：本仓库不是 uvwt/agentdock 上游发行仓库。** 最新私有源码为 v0.7.9 功能整合分支，包含 2026-10-04 的 MCP 会话/Watchdog 及 DeepSeek Flash 子 Agent 路由；之前的 v0.7.7 二进制是旧的预发布构建。
>
> **Windows 一键使用：** 下载本仓库 Actions 的 Personal Windows Launcher Build 构建工件，解压后双击 **Start-AgentDock.cmd**。在可视化面板中粘贴 ngrok Authtoken、配置 OpenCode/Watchdog、开启 Windows 登录自动任务。参见 [私有版一键配置说明](docs/windows-one-click.md)。
>
> 下方遗留的“uvwt/agentdock 官方下载链接”“Docker 镜像”和“agentdock update”均指向**上游**，不是你的私有版。请不要用那些入口覆盖私有构建。

---

<div align="center">

[English](./README.md) | 简体中文

<img src="./docs/assets/agentdock-logo.png" alt="AgentDock logo" width="128" />

# AgentDock MCP

**让 AI 的双手，真正触达你的每一台设备。**

打开网页版 ChatGPT，即可管理多台电脑与服务器：在真实设备上写代码、改配置、跑命令与部署，执行发生在你的机器上，不消耗Codex额度。


[快速开始](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/install) · [在线文档](https://uvwt.github.io/agentdock-docs/zh-CN/) · [版本发布](https://github.com/uvwt/agentdock/releases) · [问题反馈](https://github.com/uvwt/agentdock/issues)

[![CI](https://github.com/uvwt/agentdock/actions/workflows/ci.yml/badge.svg)](https://github.com/uvwt/agentdock/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/uvwt/agentdock?display_name=tag&logo=github)](https://github.com/uvwt/agentdock/releases)
[![Docker Hub](https://img.shields.io/docker/pulls/agentdockio/agentdock?logo=docker&label=Docker%20Hub)](https://hub.docker.com/r/agentdockio/agentdock)
[![GHCR](https://img.shields.io/badge/GHCR-ghcr.io%2Fuvwt%2Fagentdock-2496ED?logo=docker&logoColor=white)](https://github.com/uvwt/agentdock/pkgs/container/agentdock)
[![License](https://img.shields.io/github/license/uvwt/agentdock)](./LICENSE)

</div>

<p align="center">
  <img
    src="./docs/assets/agentdock-multi-device.png"
    alt="AgentDock：通过一个 AI 对话统一操控多台设备"
    width="100%"
  />
</p>

## AgentDock 是什么

AgentDock 是一个面向 AI Agent 的独立工具运行层。

它为本地电脑、远程服务器与容器环境提供统一、安全、可控的文件、命令、Git、Skill、MCP、浏览器自动化和任务执行能力。配置多台 AgentDock，还能跨设备协同，把原本要在多机之间来回切换的工作，收敛到一次对话里完成。

AgentDock 不提供聊天界面，也不负责模型推理。它专注于解决一件事：

> 让 AI Agent 在明确的权限边界内操作真实环境，并返回结构化、可追踪、可验证的执行结果。

```text
              ChatGPT / Claude / Codex
                        │
                        │ MCP（可接入多台）
          ┌─────────────┼─────────────┐
          ▼             ▼             ▼
   ┌───────────┐ ┌───────────┐ ┌───────────┐
   │ AgentDock │ │ AgentDock │ │ AgentDock │
   │  本机电脑  │ │  内网机器   │ │  云服务器  │
   └─────┬─────┘ └─────┬─────┘ └─────┬─────┘
         │             │             │
         ▼             ▼             ▼
   文件·命令·Git  穿透客户端等   转发·反代·部署
```

## 你可以用 AgentDock 做什么

- 用网页版 ChatGPT 直接管理多台电脑与服务器，无需分别 SSH 登录来回操作
- 在真实设备上写代码、改项目、跑测试并操作 Git，执行发生在本机或服务器，不依赖专用编程 Agent 额度
- 让 AI 管理 VPS、Docker 服务、反向代理和部署配置
- 让 AI 检查日志、进程、端口和真实运行状态
- 让 AI 操作登录后的网页与 macOS 桌面应用
- 配置多台 AgentDock，在一次对话中完成跨设备协同任务
- 通过 Skill 和动态 MCP 扩展外部能力
- 保存长时间任务的执行状态，并在中断后继续
- 用同一套工具模型连接 macOS、Linux、Windows 与容器环境
- 等等

## 典型场景：跨设备完成内网穿透

例如你有一台位于内网中的电脑。要让外部访问它，通常需要：

1. **本地电脑**：启动并检查穿透客户端
2. **服务器**：配置转发、端口、域名和反向代理

过去需要分别登录两台设备来回操作。现在只需打开一个 ChatGPT 网页，把两台机器上的 AgentDock 都接入对话，即可让 AI 同时操作电脑和服务器，完成整套流程。

这类能力可以继续扩展到更多实际场景：多机部署、本地开发联调公网服务、跨环境排障、批量配置与状态巡检等。


## 快速开始

普通用户直接使用正式安装包即可，不需要下载源码、安装 Go 或自己构建 AgentDock。

完整步骤见 [安装 AgentDock](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/install)。

| 平台 | 推荐安装方式 |
| --- | --- |
| Windows 11 | Windows 图形安装程序（.exe） |
| macOS 13 或更高版本 | 通用 DMG 图形应用 |
| Linux | 官方自动安装脚本 |
| 已安装 Docker | Docker Compose |

### Windows

1. 打开 [最新 Release](https://github.com/uvwt/agentdock/releases/latest)。
2. 大多数 Intel/AMD 电脑下载 `AgentDockSetup-amd64.exe`；Windows ARM 设备下载 `AgentDockSetup-arm64.exe`。
3. 双击安装程序，并按界面提示继续。
4. 根据 MCP 客户端所在位置和实际使用方式选择连接方式。
5. 点击“完成”后会打开控制面板；“添加桌面快捷方式”默认已勾选。

安装包已经包含 AgentDock 核心、控制面板、核心 Skill 和 Cloudflare 组件。升级时重新运行最新版 Setup，默认会保留任务、Skill、配置、连接方式和工作目录。

详细说明见 [Windows 安装](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/windows)。

### macOS

1. 从 [最新 Release](https://github.com/uvwt/agentdock/releases/latest) 下载 `AgentDock-macos-universal.dmg`。
2. 打开 DMG，把 `AgentDock.app` 拖到“应用程序”。
3. 第一次启动时，在“应用程序”中右键 AgentDock 并选择“打开”。
4. 在图形界面中选择“仅本机”“临时地址”或“固定域名”，然后点击“安装并启动”。

同一个 DMG 支持 Apple 芯片和 Intel Mac。当前版本尚未经过 Apple 公证，因此第一次启动需要手动确认一次，不需要关闭 Gatekeeper。

详细说明见 [macOS 安装](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/macos)。

### Linux

在终端运行：

```bash
curl -fsSL https://github.com/uvwt/agentdock/releases/latest/download/install.sh \
  -o /tmp/install-agentdock.sh
sudo env AGENTDOCK_NONINTERACTIVE=true sh /tmp/install-agentdock.sh
```

安装器会使用安全默认值完成安装、启动和健康检查。详细说明见 [Linux 安装](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/linux)。

### Docker

```bash
mkdir agentdock && cd agentdock
curl -fL \
  https://raw.githubusercontent.com/uvwt/agentdock/main/docker-compose.yml \
  -o docker-compose.yml
export AGENTDOCK_AUTH_TOKEN="$(openssl rand -hex 32)"
docker compose pull
docker compose up -d
```

默认 MCP 地址是 `http://127.0.0.1:8765/mcp`。数据持久化和公网访问见 [Docker 安装](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/docker)。

### 连接方式如何选择

- **仅本机**：客户端和 AgentDock 在同一台电脑上。
- **临时公网地址**：没有域名，但需要从 ChatGPT、手机或其他设备连接。地址可能在 Tunnel 重启后变化。
- **固定域名**：长期使用稳定公网地址，需要已接入 Cloudflare 的域名和 Tunnel Token。

安装完成后，从控制面板或终端取得 MCP 地址，以及 Bearer Token 或 OAuth 登录信息，再填入客户端的 MCP、Tools 或 Connectors 设置。公网访问必须保留认证，不要把凭据放进截图、Issue 或公开聊天。

## 接入 AI 客户端

AgentDock 通过 MCP Streamable HTTP 提供工具能力。下面是一个通用配置示例，具体字段格式取决于所使用的 AI 客户端：

```json
{
  "mcpServers": {
    "agentdock": {
      "url": "http://127.0.0.1:8765/mcp",
      "headers": {
        "Authorization": "Bearer <AGENTDOCK_AUTH_TOKEN>"
      }
    }
  }
}
```

如果 AgentDock 仅监听本机回环地址并明确关闭认证，可以不发送 `Authorization` 请求头。对局域网或公网开放时，必须启用认证，并配置 HTTPS 和网络访问控制。

## 平台安装

| 平台 | 文档 |
| --- | --- |
| Docker | [Docker 快速部署](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/docker) |
| Linux | [Linux 自动安装](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/linux) |
| Linux / VPS | [systemd 部署](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/vps) |
| macOS | [macOS 安装](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/macos) |
| Windows | [Windows 图形安装程序](https://uvwt.github.io/agentdock-docs/zh-CN/docs/getting-started/windows) |

每个平台文档均包含安装步骤、启动检查、MCP 地址和认证方式。浏览器自动化、macOS 桌面操作、Windows / WSL、反向代理和数据迁移等内容位于对应进阶文档。

## 版本更新

正式 Release 二进制支持直接查看版本和更新：

```bash
agentdock --version
agentdock update
```

`agentdock update` 会下载匹配当前平台的最新 Release，校验 SHA-256 并验证新二进制，然后备份和替换当前版本。检测到 LaunchAgent、systemd、Windows Service、最高权限计划任务或 Windows 用户启动项时，会自动重启并验证新版本；失败时恢复旧版本。开发构建不能通过该命令更新。Windows 托盘调用的也是这条核心更新链路；托盘程序和 Setup 本身通过重新运行新版 `AgentDockSetup.exe` 更新。

## 核心能力

### 文件与命令

- UTF-8 文本读取、搜索、目录遍历和结构化修改
- 原子文件写入、路径边界和私密目录保护
- 有超时和输出边界的命令执行
- 标准输出、标准错误和退出码分离
- 长时间命令会话、PTY、会话观察、输入和停止
- 输出截断和敏感信息脱敏
- macOS、Linux、Windows 与 WSL 支持

### Git 与 GitHub

- 仓库状态、差异和提交记录读取
- 分支、提交、拉取和推送
- GitHub 仓库访问检查
- 修改前状态检查和修改后差异验证

### Skill 与动态 MCP

官方与社区 Skill 源码统一维护在 [uvwt/agentdock-skills](https://github.com/uvwt/agentdock-skills)。本仓库只保留必须随 AgentDock 运行时发布的三个自举核心 Skill。

- Skill 包校验、安装、激活和回滚
- 稳定版、开发版、Canary 和固定版本通道
- Skill 独立环境变量与运行环境
- 动态 MCP Server 注册、启停、刷新和移除
- Streamable HTTP 与 stdio 传输
- 工具搜索、Schema 检查和受控调用
- MCP Server 之间的配置隔离

### 原生 ACP 

AgentDock 可以选择作为 ACP Client 原生托管本地 Coding Agent adapter。

- 桌面控制面板提供 Codex、Claude 和 Grok 预设；是否启用 ACP 以及选择哪个 adapter 由宿主配置决定。
- 使用 `acp_session` 创建和管理会话，使用 `acp_prompt` 发起并观察 prompt，使用 `acp_interaction` 响应 Agent 的权限请求。
- 可选 ACP 操作只会在已连接 adapter 声明相应能力时开放。
- ACP 工作目录遵循宿主进程或容器的安全边界，而不是 AgentDock 文件系统 allowlist；具体边界见下方“安全模型”。

### 浏览器与桌面自动化

- 浏览器会话启动、关闭和清理
- 页面跳转、点击、输入、选择和等待
- 页面文本、可交互元素、错误和网络响应检查
- 登录状态、持久化浏览器 Profile 和截图
- macOS 系统 Chrome 与桌面自动化支持

### 可恢复任务

- 持久化任务状态
- 明确的目标、步骤和完成条件
- 分阶段检查点
- 阻塞原因记录和中断恢复
- 最终审查与完成验证
- 可复用工作流模板

### Recall 与 NexusDock 集成

AgentDock 可以选择接入 NexusDock，为多个设备和 Agent 提供中心化能力：

- 长期项目记忆
- 运行手册和经验记录
- 工作流模板
- 私密笔记
- 多设备状态协同

NexusDock 属于可选集成。未配置时，AgentDock 的基础文件、命令、Git、Skill、MCP、浏览器和任务能力仍可独立运行。

## 使用 OAuth 接入 ChatGPT

ChatGPT 通过自定义 MCP 插件访问公网 AgentDock 时，推荐启用 OAuth。AgentDock 支持 Authorization Code、PKCE S256、动态客户端注册和 Refresh Token，ChatGPT 可以自动完成客户端注册，不需要手动创建 Client ID 或 Client Secret。

服务端至少配置：

```bash
AGENTDOCK_OAUTH_ENABLED=true
AGENTDOCK_SERVER_URL=https://agentdock.example.com
AGENTDOCK_OAUTH_PASSWORD=<至少-12-个字符的授权密码>
AGENTDOCK_OAUTH_TOKEN_SECRET=<至少-32-字节的随机签名密钥>
# 可选，默认 1h；支持 Go duration、999999d 这类整数天数，或 never（服务端永不过期）
# never 模式会为兼容 OAuth 客户端对外声明 999999d，但服务端不会让 Access Token 到期
AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL=1h
```

然后进入 ChatGPT 的 **设置 > 插件 > 高级设置**，开启开发人员模式并创建插件，MCP Server URL 填写：

```text
https://agentdock.example.com/mcp
```

保存插件后，浏览器会打开 AgentDock 授权页。确认请求来自刚刚创建的 ChatGPT 插件，输入 `AGENTDOCK_OAUTH_PASSWORD` 并完成授权，再通过一次 `server_info` 或其他只读工具调用验证连接。

公网入口必须使用 HTTPS，`AGENTDOCK_SERVER_URL` 只填写 Origin，不附加 `/mcp`。完整步骤、端点验证和常见问题见 [ChatGPT 接入教程](https://uvwt.github.io/agentdock-docs/zh-CN/docs/guides/chatgpt)。

Windows 桌面端可在控制面板“高级设置”中配置 OAuth Access Token TTL，支持 `1h`、`30d`、`never` 等与 `AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL` 相同的语法。保存后普通启动和管理员计划任务都会读取同一份 `control-panel-settings.json`；留空时才继承启动环境里的 `AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL`，两者都未设置则使用核心默认的 `1h`。桌面端也会把误填的 `https://agentdock.example.com/mcp` 自动规范为 Origin。

## 镜像版本

| 镜像标签 | 用途 |
| --- | --- |
| `latest` / `<version>` | 正式运行镜像，不包含 Go 编译工具链 |
| `dev-latest` / `dev-<version>` | 开发镜像，包含 Go、C 和 C++ 构建工具链 |
| `browser-latest` / `browser-<version>` | Go 原生 CDP 浏览器自动化镜像，包含 Chromium 运行环境 |

浏览器工具（`browser_session`、`browser_act`、`browser_snapshot`）使用 Go 原生 CDP。非 browser 镜像环境默认需要本机安装 Chrome、Chromium 或 Microsoft Edge；AgentDock 不自动下载浏览器，浏览器能力不依赖 Node.js 或 Playwright。

也可以直接复用已经开启远程调试的 Chromium 系浏览器：`browser_session start` 的 `cdp_url` 只接受本机 loopback 地址；Docker 或其他远端场景请由用户通过环境变量或桌面设置配置 `AGENTDOCK_BROWSER_CDP_URL`。AgentDock 会在外部浏览器中创建并仅管理自己的目标页，关闭 AgentDock session 不会退出外部浏览器，也不会把既有用户标签页纳入 session。外部 session 不允许注入 Cookie 或 localStorage，避免修改用户现有浏览器 profile。

`AGENTDOCK_BROWSER_REUSE_EXISTING_CDP=true` 可由用户配置开启本机自动复用，默认关闭，工具调用不能临时覆盖开启。发现逻辑只跟随 Chromium 系浏览器进程的 `--remote-debugging-port` 和对应 `DevToolsActivePort`，CDP HTTP 探测不会继承宿主机代理；没有候选时继续按原逻辑启动 AgentDock 自己的浏览器，唯一候选才自动连接，多个候选会要求用户显式配置 CDP 地址。Docker 容器无法枚举宿主机进程或 profile，因此容器接入宿主机已有浏览器时应显式配置可达的 CDP 地址。

正式镜像同步发布到：

```text
ghcr.io/uvwt/agentdock
agentdockio/agentdock
```

正式环境建议固定具体版本，不要长期直接依赖 `latest`：

```yaml
services:
  agentdock:
    image: ghcr.io/uvwt/agentdock:<version>
```

## 运行目录

| 路径 | 用途 |
| --- | --- |
| `~/AgentDock` | 相对文件操作的默认工作目录 |
| `~/.agentdock` | AgentDock 状态、配置、会话和扩展数据 |

Docker 部署默认使用 named volume 保存持久化数据，以减少 Linux bind mount 的 UID 和 GID 冲突。需要让 AgentDock 访问宿主机目录时，应显式挂载所需路径，不要直接挂载整个根目录。

## 端口说明

Docker、原生安装和本地开发的默认 MCP 地址：

`http://127.0.0.1:8765/mcp`

端口可以通过配置调整，客户端应以实际部署配置为准。

## 安全模型

AgentDock 会直接操作宿主机或容器内的真实资源，应将其视为基础设施服务进行部署和授权。

### 网络安全

- 仅监听回环地址时，可以根据实际需求关闭认证
- 监听非回环地址时，必须启用 Bearer Token 或 OAuth
- 对公网提供服务时，必须配置 HTTPS
- 推荐配合防火墙、反向代理和网络访问控制
- 不要直接将未认证的 MCP 服务暴露到公网

### 权限边界

- 使用独立系统用户运行 AgentDock
- 只授予完成实际任务所需的文件权限
- Docker 中只挂载需要访问的目录
- 不授予不必要的 root、Docker Socket 或宿主机权限
- Skill 与动态 MCP 的敏感变量使用独立环境存储
- ACP executable、参数和环境映射只能由宿主机配置，不能通过远程工具修改
- ACP 工作目录不受 AgentDock 允许根目录限制；实际安全边界是 AgentDock 进程用户以及宿主机或容器的 ACL、挂载和网络策略

### 执行验证

- 命令退出状态与工具调用状态分离
- 文件修改后检查真实差异
- 部署后验证进程、端口、日志和服务响应
- 长任务设置明确的完成条件
- 不仅根据“命令已执行”判断任务成功

## 从源码运行

本节面向 AgentDock 贡献者和需要调试运行时的开发者。

```bash
git clone https://github.com/uvwt/agentdock.git
cd agentdock

make check
make run
```

源码开发模式默认监听：

```text
http://127.0.0.1:8765/mcp
```

## 开发与贡献

提交代码前运行完整检查：

```bash
make check
```

项目使用 GitHub Actions 持续执行测试、静态检查、构建和发布验证。

用户文档独立维护在 [`uvwt/agentdock-docs`](https://github.com/uvwt/agentdock-docs)。修改用户可见行为、配置参数、安装方式或工具 Schema 时，应同步更新对应文档。

提交问题或功能建议请使用 [GitHub Issues](https://github.com/uvwt/agentdock/issues)。

## 项目边界

AgentDock 是工具运行层，不是完整的 AI 应用平台。

它不包含聊天界面、大模型推理服务、模型账号或 API 额度，也不会绕过认证或系统安全控制。AgentDock 可以被 ChatGPT、Claude、Codex 或其他支持 MCP 的 Agent 客户端调用，实际接入方式取决于客户端对 MCP 传输和认证配置的支持。

## 相关链接

- [在线文档](https://uvwt.github.io/agentdock-docs/zh-CN/)
- [文档源码](https://github.com/uvwt/agentdock-docs)
- [GitHub Releases](https://github.com/uvwt/agentdock/releases)
- [GitHub Container Registry](https://github.com/uvwt/agentdock/pkgs/container/agentdock)
- [Docker Hub](https://hub.docker.com/r/agentdockio/agentdock)
- [Linux Do](https://linux.do/)

## License

Apache License 2.0. See [LICENSE](./LICENSE).
