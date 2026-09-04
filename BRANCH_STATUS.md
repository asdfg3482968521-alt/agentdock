# AgentDock Branch Status

> Updated: 2026-09-05 (Asia/Seoul)

## Active mainline

- `production-current-agentdock` — **CANONICAL / LATEST MAINLINE**
  - Baseline when policy was established: `fe6b4b4d4c9f1f2c8bda8582bdf582822db7c5bb`
  - Rule: all new development must branch from this branch.
- `main` — **DEFAULT-BRANCH MIRROR ONLY**
  - It was force-aligned to `production-current-agentdock` after archiving the previous main head.
  - Do not treat historical `main` commits as a separate development line.

## Deprecated branches

All branches below are deprecated. Do not start new work from them, merge them into the mainline, or cherry-pick from them unless a specific commit has been reviewed first.

| Branch | Last activity (Asia/Seoul) | Last head SHA |
|---|---|---|
| `agentdock-opencode-jobs-integration` | 2026-09-04 | `39c60cfa` |
| `agentdock-opencode-supervisor` | 2026-08-23 | `51766e0a` |
| `build/compare-5780-vs-gpt-actions-20260827` | 2026-08-27 | `0aa6436c` |
| `build/compare-run-20260827` | 2026-08-27 | `8585f981` |
| `build/degraded-startup-20260827` | 2026-08-27 | `1b33f6a2` |
| `build/degraded-startup-v2-20260827` | 2026-08-27 | `a43be3ef` |
| `build/degraded-startup-v3-20260827` | 2026-08-27 | `975b19c5` |
| `build/degraded-startup-v4-20260827` | 2026-08-27 | `7e045cd6` |
| `build/gpt-actions-always-allow-20260827` | 2026-08-27 | `73521cd8` |
| `build/gpt-actions-always-allow-v2-20260827` | 2026-08-27 | `fd8f4700` |
| `build/gpt-actions-schema-fix-20260827` | 2026-08-27 | `8c294141` |
| `build/gpt-actions-windows-20260827` | 2026-08-27 | `5702d2c8` |
| `build/opencode-source-independence-20260827` | 2026-08-27 | `ada86ff5` |
| `chatgpt/session/c8841e4c9e154b508247fd96e569a247` | 2026-08-26 | `2b0d9328` |
| `deprecated/2026-09-04/main-before-production-current` | 2026-09-04 | `ec264a68` |
| `feature/gpt-actions-direct` | 2026-08-27 | `d660a32e` |
| `fix/notion-mcp-compat-20260903` | 2026-09-04 | `bcda525b` |
| `fix/notion-mcp-compatible` | 2026-08-22 | `854413e3` |
| `fix/windows-ci-constructor-20260819` | 2026-08-19 | `25e6eddd` |
| `fix/windows-installer-hash-logging-20260822` | 2026-08-22 | `2eb973a5` |
| `fix/windows-oauth-ttl-settings-20260822` | 2026-08-22 | `fe721a6d` |
| `phase-c2.5-a-agent-backend-core` | 2026-08-25 | `868f7ef3` |
| `phase-c2.5-b-opencode-worker` | 2026-08-25 | `29712a11` |
| `phase-c2.5-c-integration-runtime` | 2026-08-25 | `b2231c62` |
| `phase-c2.5-m-a-mcp-control-plane` | 2026-08-25 | `d47e144b` |
| `phase-c2.5-m-b-host-boundary` | 2026-08-25 | `1c602471` |
| `phase-c2.5-m-c-baseline-validation` | 2026-08-25 | `1649038b` |
| `phase-c2.5-m-c-behavior-aeecfb6` | 2026-08-25 | `71e1c756` |
| `phase-c2.5-m-c-hygiene-aeecfb6` | 2026-08-25 | `529dd8e4` |
| `phase-c2.5-m-c-integration` | 2026-08-25 | `aeecfb65` |
| `phase-c2.5-m-c-job-flake-aeecfb6` | 2026-08-25 | `fbb73bf0` |
| `phase-c2.5-m-c-revised-validation-aeecfb6` | 2026-08-25 | `cf3ab81d` |
| `phase-c2.5-m-c-validation` | 2026-08-25 | `47984f0f` |
| `phase-d3-b-native-session-bridge` | 2026-08-26 | `4cf08c3f` |
| `phase-d3-c-autonomous-backend` | 2026-08-26 | `0a7488de` |
| `phase-d3-d-gpt-supervisor` | 2026-08-26 | `2ee59681` |
| `phase-d3-default-root` | 2026-08-26 | `57e5c9fc` |
| `phase-d3-e-model-session-visibility` | 2026-08-26 | `94686ef8` |
| `phase-d3-f0-multiturn-contract-tests` | 2026-08-26 | `66cbce18` |
| `phase-d3-final-idempotency` | 2026-08-27 | `5780e19f` |
| `phase-d3-g0-subagent-pool-contract-tests` | 2026-08-26 | `ba184f9a` |
| `phase-d3-g1-subagent-pool` | 2026-08-26 | `71d8723d` |
| `phase-d3-h-restart-reattach` | 2026-08-26 | `3a9438f1` |
| `phase-d3-i-integration` | 2026-08-26 | `a3f669b6` |
| `phase-d3-i-production-revised` | 2026-08-26 | `fed97abd` |
| `phase-d3-s0-global-sandbox-contract` | 2026-08-26 | `eb2a9c70` |
| `phase-d-native-session-runtime` | 2026-08-24 | `162e98d1` |
| `refactor/remove-git-tools-20260819` | 2026-08-19 | `744142eb` |
| `validation/d2-r5-race-94a8e68-20260825` | 2026-08-24 | `94a8e68b` |
| `validation/d2-r5-race-94a8e68` | 2026-08-24 | `94a8e68b` |

## AI / contributor rule

Before modifying AgentDock:

1. Fetch the repository.
2. Checkout `production-current-agentdock`.
3. Verify the working base is descended from the current `production-current-agentdock` HEAD.
4. Create a new short-lived feature/fix branch from that HEAD.
5. Do **not** merge/rebase/cherry-pick any deprecated branch without naming the exact commit(s) and reviewing them first.
6. Treat `main` only as a default-branch mirror of the canonical production line.

## Historical main preservation

The previous `main` head (`ec264a68`, last active 2026-09-04 Asia/Seoul) was preserved as:

`deprecated/2026-09-04/main-before-production-current`

No historical commits were discarded.
