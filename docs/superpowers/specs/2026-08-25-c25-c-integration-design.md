# C2.5 C LINE Integration Design

## Goal

Prepare the C2.5 integration branch to connect the frozen D2 Agent public surface to the forthcoming AgentBackend core and OpenCode Worker exact SHAs without changing the C2 public wire contract.

## Frozen inputs

- C2_BASE_SHA: `94a8e68b64db9aefedd403bf54da2fd76df45982`
- OPENCODE_RUNTIME_SHA: `d5664f38002d8326eca179cf2efb5774c8ce1247`
- Branch: `phase-c2.5-c-integration-runtime`
- C2 status: `ACCEPTED_WITH_DEFERRED_EXTERNAL_VALIDATION`
- Deferred work explicitly excluded: C2 R5, provider credential closure, C2 package acceptance.

## Public contract decision

The frozen `agent_start` schema already accepts any lowercase provider token matching `^[a-z0-9][a-z0-9._-]*$`; it does not enumerate Anthropic/DeepSeek. Therefore `provider="opencode"` can be used as the existing public routing discriminator without changing any of the six frozen Agent schemas.

C2.5 C LINE must preserve:

- the 24 frozen target tools,
- all six frozen Agent input/output schemas byte/hash compatible with C2,
- no public `backend`, `workspace`, `runtime_scope`, or `permission_mode` routing override.

## Internal routing

C LINE owns an app-layer route classifier:

- normalized provider `opencode` => OpenCode backend route,
- every other provider => legacy/provider backend route.

The classifier is deliberately app-layer glue. It does not redefine Agent state, worker lifecycle, durable storage, or provider model resolution.

The existing `credential_ref` field remains syntactically required because the wire contract is frozen. For the OpenCode route the app layer must not require an Anthropic/DeepSeek credential record. A syntactically valid opaque ref is sufficient at the public boundary; the eventual AgentBackend integration must ensure no secret is resolved or forwarded to OpenCode. Legacy/provider routes retain the existing credential-store describe/provider-match checks.

## Durability and restart boundary

Durable backend identity belongs with the Agent durable record/backend contract after the A LINE exact SHA is imported. C LINE will not invent or duplicate the A LINE core field before that SHA exists. The imported field must default old C2 records to the legacy/provider backend so missing C2.5 data remains recoverable.

OpenCode running-at-restart records must follow the existing Agent recovery policy; C LINE must not add a new public state.

## Session and Job ownership

OpenCode execution must use the Session-resolved canonical workspace/worktree, runtime scope, and permission. No caller-provided workspace widening is allowed. Agent-owned Jobs, if used, must preserve the tuple `AgentID + SessionID + RuntimeScope + Workspace` and must not be cancellable across sessions.

Session close ordering remains Agent cancel/await first, then owned Job cancellation, then Session terminal persistence. Existing completion-order guarantees remain mandatory for both backends.

## A/B integration gate

Before exact A/B SHAs are supplied, C LINE may only implement app-owned routing seams, compatibility guards, integration fixtures, and audits that compile against C2. It must not copy guessed AgentBackend or OpenCodeWorker interfaces.

After Central supplies exact A and B integration SHAs, C LINE will import only those SHAs and wire:

`AgentBackend -> OpenCodeBackend adapter -> OpenCodeWorker`

while preserving the ownership, persistence, restart, completion-order, and public-contract gates above.

## Verification

Independent-stage verification:

- focused baseline packages pass on exact C2 base,
- routing classifier tests,
- OpenCode route does not require credential-store lookup,
- legacy provider credential validation remains unchanged,
- frozen 24-target + 6-schema gate remains pass.

Post-import verification additionally requires public-path OpenCode worker E2E, multi-session isolation, cancel/close isolation, durability, restart recovery, and legacy ProviderBackend regression.
