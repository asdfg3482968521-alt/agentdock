# Phase D2 API Subagent Pre-Contract Foundation Design

## Status

A LINE pre-contract design only. Public `agent_*` MCP schemas, public state names, public error codes, `credential_ref` syntax, budget schema, permission schema, provider/model schema details, and acceptance wire shapes remain blocked until Central provides the frozen B D2 Contract SHA.

## Frozen inputs

- AgentDock BASE: `162e98d128166d0efe08525fa7dcf79a016b72a9`
- Branch: `phase-d2-api-subagent-runtime`
- D1 Session contract: `D1-v1`, frozen
- OpenCode runtime: `d5664f38002d8326eca179cf2efb5774c8ce1247`, read-only
- D1 direct tools remain unchanged.

## Audit findings

### Session layer

`internal/session` already provides durable records, canonical worktree/workspace resolution, permission metadata, restart recovery, and mutation authorization. `internal/app/session_routing.go` is the existing routing boundary for converting `session_id` into the canonical workspace/runtime scope and for enforcing ownership.

D2 must consume this boundary rather than accepting an agent-supplied workspace override.

### Job layer

`internal/job` already demonstrates the desired control-plane mechanics:

- durable metadata via `internal/fs/atomicfile`
- bounded queue and global/per-workspace concurrency
- real cancellation that waits for the runtime cancel path
- timeout implemented through cancellation
- restart reconciliation to a terminal interrupted state
- bounded durable NDJSON output with stable cursor
- redaction before output persistence

Agent code should reuse these patterns, not couple itself to Job internals that are not exported for reuse.

### Secret storage

`internal/envstore` is private-by-ACL plaintext `.env` storage. It is suitable for lock/atomic-write/path-security patterns but not for D2 production provider credentials.

`internal/desktopruntime` already contains working Windows DPAPI primitives based on `CryptProtectData`/`CryptUnprotectData`, optional entropy, base64 encoding, and atomic writes. D2 should extract the cryptographic primitive into a small reusable internal package while keeping desktopruntime behavior unchanged.

### Redaction

`internal/job/redaction.go` already covers structured-looking password/token/api-key/secret/credential values, Authorization Bearer/Basic headers, and URL user-info secrets. D2 must not fork another regex set. The common text redactor should be extracted into a shared internal package and Job should continue using the same behavior.

### HTTP

The repository uses `http.NewRequestWithContext` and bounded `http.Client` instances in several packages, but there is no single provider-safe HTTP adapter with retry/error-normalization semantics. Provider HTTP policy belongs behind the future provider adapter, not in MCP handlers.

## Pre-contract architecture

### 1. Shared redaction package

Create `internal/redact` with a narrow `Text(string) string` API and tests that preserve current Job behavior. Update Job to delegate to the shared package. No public contract impact.

### 2. Shared protected-data primitive

Create `internal/securedata` with build-tagged Windows DPAPI functions and an unsupported-platform implementation. The API is intentionally storage-neutral:

```go
func Protect(plaintext []byte, entropy []byte) ([]byte, error)
func Unprotect(ciphertext []byte, entropy []byte) ([]byte, error)
```

It returns raw protected bytes; callers decide persistence encoding. This prevents `credential_ref` syntax or provider identity from leaking into the crypto primitive.

Refactor the existing desktopruntime DPAPI helpers to call this package while preserving current `.dpapi` file format (base64 of protected bytes).

### 3. Credential boundary, no public ref grammar yet

Create `internal/credential` interfaces only:

```go
type Secret []byte

type Resolver interface {
    Resolve(context.Context, string) (Secret, error)
}
```

The string is opaque at this stage. No parsing rules are frozen. Tests verify that secrets can be supplied to a consumer without being serialized into metadata or errors. Production ref parsing/storage mapping waits for B freeze.

### 4. Provider adapter boundary

Create `internal/agent/provider.go` with an internal provider-neutral request/event/result/cancel abstraction. It must accept context cancellation, usage data, and tool-call events without naming Anthropic/DeepSeek wire formats in manager code.

A deterministic test-only fake adapter lives in `_test.go` or a test-only helper package and never appears in a production provider registry.

### 5. Agent manager private scaffolding

Before B freeze, create only the private mechanics that are contract-independent:

- manager owns a bounded queue
- every start request carries an internal session binding object supplied by a resolver
- canonical workspace/permission/runtime scope come from that binding, never caller workspace
- active work has cancel functions and terminal completion channels
- durable storage is abstracted behind an internal store interface
- output spool abstraction supports bounded append/read with cursor
- manager metadata contains only opaque `credential_ref`, never resolved secret
- restart reconciliation hook exists, but public terminal state/error strings are not exposed through MCP yet

Any provisional internal lifecycle names are explicitly private and may be adapted to B D2 Contract without compatibility promises.

### 6. No public registration before freeze

Do not add any `agent_*` entries to `internal/app` tool registry/specs, `ToolNames`, server info, or MCP schemas until Central supplies `B_D2_CONTRACT_SHA` and the frozen machine-readable contract.

## Security invariants

- Resolved credential plaintext exists only in memory and provider request construction.
- Durable Agent metadata may contain only opaque credential reference text.
- Redaction runs before output/log/error persistence.
- Agent tool execution later enters through the D1 session-routed AgentDock/OpenCode gateway; no direct filesystem/shell bypass is introduced by this foundation.
- Agent permission can never be derived from model/provider output.
- Cancellation context is propagated to provider execution and future tool loop work.

## Verification gates

1. Baseline `go test ./...` must pass before D2 production code. Confirmed on exact BASE using Go 1.26.5.
2. Every production change follows red-green-refactor.
3. Focused tests must pass after each task.
4. Full `go test ./...` runs after the pre-contract foundation batch.
5. `git diff` must show no OpenCode repository change, no D1 contract change, and no public `agent_*` schema registration.
