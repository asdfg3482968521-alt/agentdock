# C2.5 C LINE Independent Integration Preparation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish C-owned routing and public-contract compatibility seams that can be integrated with exact A/B SHAs later without guessing their interfaces.

**Architecture:** Keep the frozen D2 wire surface unchanged and use the existing `provider` token as the routing discriminator. Add only app-layer routing/credential-boundary glue now; defer Agent durable backend fields and OpenCode worker adapter types until Central supplies exact A/B SHAs.

**Tech Stack:** Go 1.26.5, existing AgentDock `internal/app`, `internal/credential`, frozen D2 JSON schemas, Go test.

**Spec:** `docs/superpowers/specs/2026-08-25-c25-c-integration-design.md`

## Global Constraints

- Exact base is `94a8e68b64db9aefedd403bf54da2fd76df45982`.
- Do not execute C2 R5, provider credential closure, or C2 package acceptance.
- Do not modify the six frozen `internal/app/d2contract/agent-*.schema.json` files.
- Do not modify A-owned AgentBackend core or B-owned OpenCode worker code before exact Central integration SHAs are supplied.
- Preserve legacy provider credential validation exactly.
- Use TDD for production changes.

---

### Task 1: Characterize frozen public compatibility

**Files:**
- Modify: `internal/app/agent_public_schema_test.go`

**Interfaces:**
- Consumes: `validateFrozenAgentInput("agent_start", args)` and the existing frozen D2 schema.
- Produces: regression proof that `provider="opencode"` fits the current wire schema and requires no contract bump.

- [ ] **Step 1: Add a characterization test**

Add `TestC25OpenCodeProviderFitsFrozenAgentStartSchema` with a complete valid `agent_start` argument map using `provider: "opencode"`, `credential_ref: "cred_opencode_local"`, read-only permission, positive timeout, and positive `budget.max_requests`; assert `validateFrozenAgentInput` returns nil.

- [ ] **Step 2: Run the characterization test**

Run:
`go test ./internal/app -run '^TestC25OpenCodeProviderFitsFrozenAgentStartSchema$' -count=1`

Expected: PASS on the frozen base, proving schema change is unnecessary.

### Task 2: Add the internal backend route classifier

**Files:**
- Create: `internal/app/agent_backend_routing.go`
- Create: `internal/app/agent_backend_routing_test.go`

**Interfaces:**
- Produces: `agentBackendRoute`, `agentBackendProvider`, `agentBackendOpenCode`, `routeAgentBackend(provider string)` and `requiresStoredCredential()`.
- Consumes: only normalized provider text; no A/B interfaces.

- [ ] **Step 1: Write failing route tests**

Test that `opencode`, whitespace/case variants of OpenCode route to `agentBackendOpenCode` and do not require a stored credential; test `anthropic`, `deepseek`, and unknown provider tokens route to `agentBackendProvider` and do require stored credentials.

- [ ] **Step 2: Verify RED**

Run:
`go test ./internal/app -run '^TestRouteAgentBackend' -count=1`

Expected: build failure because the routing types/functions do not yet exist.

- [ ] **Step 3: Implement the minimal classifier**

Create an unexported string-like route type with two constants. `routeAgentBackend` trims/lowercases the provider and returns OpenCode only for exact `opencode`; `requiresStoredCredential` returns false only for OpenCode.

- [ ] **Step 4: Verify GREEN**

Run the same focused test and expect PASS.

### Task 3: Route public credential validation without breaking legacy providers

**Files:**
- Modify: `internal/app/agent_public_tools.go`
- Modify: `internal/app/agent_backend_routing_test.go`

**Interfaces:**
- Produces: `validateAgentStartCredential(ctx, store, route, provider, credentialRef)` app helper.
- Consumes: `credential.ValidateRef`, `credential.Store.Describe`, `routeAgentBackend`.

- [ ] **Step 1: Write failing credential-boundary tests**

Cover three behaviors: OpenCode accepts a syntactically valid ref without requiring a stored credential; legacy provider with a missing ref returns credential-not-found; legacy provider with a stored credential bound to another provider returns the existing invalid-ref/provider-mismatch error path.

- [ ] **Step 2: Verify RED**

Run:
`go test ./internal/app -run '^TestValidateAgentStartCredential' -count=1`

Expected: build failure because the helper does not yet exist.

- [ ] **Step 3: Implement the helper and call it from `d2AgentStart`**

Keep `credential.ValidateRef` for every route. Return immediately after grammar validation for OpenCode. For legacy/provider routes, require a non-nil store, call `Describe`, and enforce metadata provider equality exactly as before. Replace the inline credential block in `d2AgentStart` with this helper.

- [ ] **Step 4: Verify GREEN and legacy behavior**

Run focused routing tests plus existing `TestD2AgentStartPublicHandler` and `TestD2AgentStartRejectsClosedSessionAndPermissionEscalation`; expect PASS.

### Task 4: Re-run frozen contract and focused regression gates

**Files:**
- No production changes.

**Interfaces:**
- Consumes: final independent-stage app changes.
- Produces: evidence for Central.

- [ ] **Step 1: Run frozen 24-target/6-schema tests**

Run:
`go test ./internal/app -run 'TestD2AgentRegistryMatchesFrozenBFinalSchemasAnd24TargetTools|TestD2AgentPublicSuccessOutputsMatchFrozenSchemas|TestC25OpenCodeProviderFitsFrozenAgentStartSchema' -count=1`

Expected: PASS.

- [ ] **Step 2: Run MCP raw schema preservation**

Run:
`go test ./internal/mcp -run '^TestOfficialSDKToolsListPreservesFrozenD2AgentSchemas$' -count=1`

Expected: PASS.

- [ ] **Step 3: Run focused package regression**

Run:
`go test ./internal/agent ./internal/app ./internal/job ./internal/session ./internal/opencode ./internal/mcp -count=1`

Expected: PASS.

- [ ] **Step 4: Commit the independent-stage C LINE changes**

Commit only C-owned docs/app/test files with message:
`feat(c2.5): prepare backend integration routing`

Record the resulting SHA as the C independent checkpoint. Do not import A/B until Central supplies exact SHAs.
