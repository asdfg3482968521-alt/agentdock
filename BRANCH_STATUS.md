# AgentDock version / branch policy

Updated: 2026-10-17

## Supported source line

- `main` is the single supported source line after PR #16 is merged.
- `production-current-agentdock` mirrors the exact same commit for compatibility after promotion.
- Latest integrated source version: **0.7.9**. Includes September 30 D2/D3 native subagent hardening, October 4 MCP conversation/bootstrap and Watchdog support, October 4 Jiyuan DeepSeek Flash default route, Windows graphical launcher and ngrok/OAuth setup.
- The recommended published native Windows desktop release is `agentdock-desktop-latest`: `AgentDockSetup-amd64.exe` (approximately 104 MB). It bundles the original WPF GUI, tray, Go core, and offline desktop runtime. The older ~14 MB CLI helper release is deprecated.

## Windows entry point

- Install the [native desktop Setup](https://github.com/asdfg3482968521-alt/agentdock/releases/download/agentdock-desktop-latest/AgentDockSetup-amd64.exe) and launch AgentDock from the Start Menu. Do not confuse it with the old CLI helper ZIP.
- The original built-in WPF tunnel configuration currently manages Cloudflare; the optional legacy PowerShell companion has separate ngrok support. These are not interchangeable.
- Do not treat upstream `uvwt/agentdock` auto-updates and Docker images as releases of this private branch.

## Historical development refs

Older `feature/*`, `phase-*`, `build/*`, `fix/*`, and deprecated branches are **not supported deployment versions**. They are temporarily kept only as recovery history, not for routine development or release. New work should branch from the verified current main commit, run CI, and return to main through reviewed changes. Do not force-reset main from an old feature branch.

## Promotion

1. Keep `main` and `production-current-agentdock` aligned by fast-forward.
2. Publish the native desktop setup using `.github/workflows/native-desktop-windows.yml`; its Release uses a stable direct download URL.
3. Validate a complete install, OAuth authorization and Windows login autostart on a user machine before claiming production-ready.
