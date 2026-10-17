# AgentDock version / branch policy

Updated: 2026-10-17

## Supported source line

- `main` is the single supported source line after PR #16 is merged.
- `production-current-agentdock` mirrors the exact same commit for compatibility after promotion.
- Latest integrated source version: **0.7.9**. Includes September 30 D2/D3 native subagent hardening, October 4 MCP conversation/bootstrap and Watchdog support, October 4 Jiyuan DeepSeek Flash default route, Windows graphical launcher and ngrok/OAuth setup.
- The most recent *published binary* remains the older v0.7.7 prerelease until a new release is built and published. Use the private Windows GitHub Actions artifact instead of an upstream download.

## Windows entry point

- Run `Start-AgentDock.cmd` from the downloaded personal Windows artifact.
- See [docs/windows-one-click.md](docs/windows-one-click.md) for setup, ngrok paste prompts, local/OAuth handling and scheduled tasks.
- Do not treat upstream `uvwt/agentdock` auto-updates and Docker images as releases of this private branch.

## Historical development refs

Older `feature/*`, `phase-*`, `build/*`, `fix/*`, and deprecated branches are **not supported deployment versions**. They are temporarily kept only as recovery history, not for routine development or release. New work should branch from the verified current main commit, run CI, and return to main through reviewed changes. Do not force-reset main from an old feature branch.

## Promotion

1. Confirm Windows launcher syntax check and Go CI status.
2. Merge PR #16 to main; align `production-current-agentdock` by fast-forward only.
3. Package `agentdock-personal-windows` using the private Windows build workflow.
4. Validate a full Windows ngrok OAuth authorization and the Scheduled Task on a target machine before labeling the release production-ready.
