# D3-G1 Multi-subagent Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate the accepted D3-C autonomous OpenCode backend into the durable AgentDock Agent lifecycle so at least A/B/C top-level subagents can run concurrently with independent exact native roots, supervisor targeting, cancellation, cursors, ownership, and re-init identity preservation.

**Architecture:** Keep the six historical D2 Agent tool schemas unchanged. Add a distinct internal/public provider route (`opencode-native`) that adapts the existing Agent Manager lifecycle to D3-C's native autonomous backend, while D3-C continues to talk to OpenCode using provider id `opencode`. Bind the Agent Manager `AgentID` as the exact nativebridge `SubagentID` and `ClientRequestID`, so the frozen `agent_start` result is also the durable supervisor target without exposing raw OpenCodeSessionID authorization.

**Tech Stack:** Go 1.26.5, AgentDock Agent Manager, D3 nativebridge/F1 supervisor, D3-C opencodeautonomous backend, frozen OpenCode runtime SHA `d5664f38002d8326eca179cf2efb5774c8ce1247`, Windows real-runtime acceptance.

**Spec:** Central handoff ZIP `D3-G1_新会话实机接替启动包(1).zip` plus imported G0 contract `docs/superpowers/specs/2026-08-26-d3-g0-multi-subagent-isolation-contract.md`.

## Global Constraints

- Base exactly `D3_F1_FINAL_SHA=2b0d9328d71545bb24e09f810fbb94fa6bcbb88c`.
- Import exactly `D3_C_FINAL_SHA=0a7488de1b07c24615a753f0d345b9fca84efae0` and `D3_G0_FINAL_SHA=ba184f9a9ff0c81965c69c1286bc83c7c16cf31a`; never merge moving branch tips.
- Branch exactly `phase-d3-g1-subagent-pool` in an isolated worktree.
- D3-S1 and OS filesystem sandbox are cancelled; do not implement or claim an OS filesystem sandbox.
- Preserve AgentDock Session ownership, canonical workspace, RuntimeScope, permission authority, foreign-root/foreign-descendant denial, and raw OpenCodeSessionID non-authority.
- Preserve all six historical D2 Agent schemas byte-for-byte and hash-for-hash.
- Preserve existing provider `opencode` bounded-worker semantics for C2.5 regression; native autonomous execution uses a new route.
- One top-level AgentDock subagent maps to one AgentDock Session and one exact OpenCode root Native SessionID. Children remain descendants only.
- AgentID equals the durable top-level native SubagentID for the new route; callers never provide root OpenCodeSessionID.
- Re-init must preserve exact root mappings; LOST roots are not silently replaced.
- All production changes follow test-first red/green cycles.

---

### Task 1: Exact dependency import and frozen contract gate

**Files:**
- Import exactly: `internal/opencodeautonomous/backend.go`
- Import exactly: `internal/opencodeautonomous/backend_test.go`
- Import exactly: `internal/opencodeautonomous/real_runtime_windows_test.go`
- Import exactly: `internal/nativebridge/d3_g0_contract_test.go`
- Import exactly: `internal/nativebridge/testdata/d3-g0-isolation-contract.json`
- Import exactly: `docs/superpowers/specs/2026-08-26-d3-g0-multi-subagent-isolation-contract.md`

**Interfaces:**
- Consumes: F1 `nativebridge.Manager` and durable `session.NativeSessionBinding`.
- Produces: exact C/G0 source baseline on the G1 branch without semantic drift.

- [ ] Cherry-pick exact D3-C SHA and exact D3-G0 SHA; preserve source SHAs in history/evidence.
- [ ] Run `go test -count=1 ./internal/opencodeautonomous ./internal/nativebridge` and require PASS before G1 modifications.
- [ ] Verify `git diff` shows no edits to historical D2 schema JSON files.

### Task 2: Bind AgentID as the durable native SubagentID

**Files:**
- Modify: `internal/opencodeautonomous/backend.go`
- Modify test: `internal/opencodeautonomous/backend_test.go`

**Interfaces:**
- Consumes: `opencodeautonomous.Request.AgentID`, `nativebridge.StartRequest.SubagentID`.
- Produces: each native autonomous logical Agent uses `AgentID` as both `ClientRequestID` and explicit `SubagentID`, so repeated starts for that logical identity reconcile to the same root.

- [ ] Add a failing test asserting `Run` forwards `AgentID` as `StartRequest.SubagentID` and preserves `ClientRequestID=AgentID`.
- [ ] Run the focused test and verify RED because current D3-C leaves `SubagentID` empty.
- [ ] Set `SubagentID: request.AgentID` in the nativebridge start request.
- [ ] Re-run D3-C tests and require GREEN.

### Task 3: Add the Agent Manager adapter for native autonomous execution

**Files:**
- Create: `internal/app/opencode_native_autonomous_backend.go`
- Create test: `internal/app/opencode_native_autonomous_backend_test.go`
- Modify: `internal/app/agent_backend_routing.go`

**Interfaces:**
- Consumes: `agentruntime.BackendRequest`, D3-C `OpenCodeAutonomousBackend.Run`.
- Produces: `AgentBackend` registered under provider `opencode-native`, backend identity `opencode`, no credential requirement; resolves default model deterministically to `mimo-v2.5-free`; emits D3-C telemetry as bounded Agent output and returns final native text as Agent result summary.

- [ ] Write failing adapter tests for backend metadata, default/explicit model resolution, request mapping, `AgentID==SubagentID`, telemetry projection, final result projection, and context cancellation propagation.
- [ ] Run focused tests and verify RED because adapter/route do not exist.
- [ ] Implement the minimal adapter with an injectable runner interface for deterministic tests.
- [ ] Extend credential routing so `opencode-native` does not require a stored secret while leaving `opencode` unchanged.
- [ ] Run focused adapter and existing backend-routing tests and require GREEN.

### Task 4: Wire the production runtime without changing frozen schemas

**Files:**
- Modify: `internal/app/runtime.go`
- Modify/add test: `internal/app/opencode_native_autonomous_backend_test.go`
- Existing schema verification: `internal/app/agent_public_schema_test.go`

**Interfaces:**
- Consumes: production `nativebridge.Manager` already built when `OpenCodeNativeURL` is configured.
- Produces: a D3-C backend registered as `agentBackends["opencode-native"]`; runtime Agent concurrency permits at least three native autonomous roots to overlap.

- [ ] Write a failing runtime-construction/wiring test or a minimal production-registration test proving `opencode-native` is present only when nativebridge exists and does not replace `opencode`.
- [ ] Verify RED before changing runtime wiring.
- [ ] Retain the concrete nativebridge pointer when building F1 supervisor and construct D3-C backend from it.
- [ ] Register `opencode-native`; raise only the runtime Agent Manager concurrency needed for three top-level roots while preserving defaults elsewhere.
- [ ] Run app tests plus all six historical schema hash tests and require GREEN.

### Task 5: Production A/B/C composition gate with deterministic native harness

**Files:**
- Create: `internal/app/d3_g1_pool_test.go`

**Interfaces:**
- Consumes: real `session.Manager`, real `nativebridge.Manager`, real `agent.Manager`, production native-autonomous Agent adapter, F1 supervisor methods.
- Produces: executable proof for A/B/C identity/isolation/re-init behavior without relying on public caller-supplied workspace/root identifiers.

- [ ] Write a failing composition test that starts independent AgentDock Sessions A/B/C and three `opencode-native` Agents, drives native roots concurrently in a deterministic harness, and asserts distinct AgentID/SubagentID/root/workspace/runtime tuples.
- [ ] Include A-only correction, B-only cancel, C unaffected, A/C same-root follow-up, cross-root observe/message/abort denial, foreign child denial, per-root cursor independence, per-root message ordering, duplicate native logical start idempotency, one-root terminal isolation, and manager/session re-init exact root preservation.
- [ ] Verify RED on missing/incomplete production wiring.
- [ ] Make only minimal production fixes needed to pass; do not broaden public schemas.
- [ ] Re-run G0 unchanged alongside G1 test and require GREEN.

### Task 6: Real Windows native runtime acceptance

**Files:**
- Create: `internal/app/d3_g1_real_runtime_windows_test.go`
- Reuse imported: `internal/opencodeautonomous/real_runtime_windows_test.go`
- Reuse F1: `internal/app/d3_supervisor_real_runtime_windows_test.go`

**Interfaces:**
- Consumes: frozen real OpenCode runtime and live native server/workspaces.
- Produces: real A/B/C evidence for autonomous execution, overlapping roots, targeted correction/cancel, continued unaffected sibling execution, same-root follow-ups, and exact-root ownership.

- [ ] Build three isolated fixture workspaces with deterministic completion markers and one deliberately long B task / observable C task.
- [ ] Start A/B/C through the production Agent Manager adapter and require all three distinct exact root IDs while execution overlaps.
- [ ] Apply A-only supervisor correction and B-only `agent_cancel`; prove C continues to successful terminal state on the same root.
- [ ] Follow up A and C on their original roots; verify negative controls and cursor isolation.
- [ ] Run focused manager/service re-init and prove exact roots survive with no replacement.
- [ ] Clean up only G1-owned live native sessions/fixtures.

### Task 7: Regression, hygiene, freeze, push

**Files:**
- All changed G1 production/tests only.

**Interfaces:**
- Produces: one clean pushed exact G1 SHA suitable for D3-H input.

- [ ] Run focused D3-C/D3-D/D3-F1/D3-G0/D3-B plus affected agent/session/app/mcp/job regressions.
- [ ] Run all historical Agent schema hash tests and assert no schema file diff.
- [ ] Run full `go test -count=1 ./...`.
- [ ] Run `gofmt` on every changed Go file, then re-run focused tests affected by formatting.
- [ ] Run `git diff --check` and source/worktree hygiene checks.
- [ ] Review the complete diff for authorization leaks, raw root-id authority, accidental OS-sandbox claims, schema drift, and unrelated changes.
- [ ] Commit G1 implementation, push branch, compare local HEAD to remote exact SHA, and require a clean worktree.
- [ ] Emit the Central recovery template with `D3_G1_STATUS=PASS` only if every required gate is evidenced.
