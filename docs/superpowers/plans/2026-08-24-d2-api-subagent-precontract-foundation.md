# Phase D2 API Subagent Pre-Contract Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the contract-independent internal foundation for AgentDock's D2 API Subagent Runtime without registering or freezing any public `agent_*` MCP schema before the B D2 Contract Freeze.

**Architecture:** Reuse D1 Session routing and Job control-plane patterns. Extract shared secret-redaction and Windows DPAPI primitives first, then introduce opaque credential/provider boundaries and a private bounded Agent manager scaffold. Public wire schemas and public state/error names remain absent until Central provides `B_D2_CONTRACT_SHA`.

**Tech Stack:** Go 1.26.5, Windows DPAPI via `golang.org/x/sys/windows`, AgentDock D1 Session/Job runtime patterns, atomicfile durable persistence.

**Spec:** `docs/superpowers/specs/2026-08-24-d2-api-subagent-precontract-design.md`

## Global Constraints

- Exact BASE is `162e98d128166d0efe08525fa7dcf79a016b72a9`.
- Work only on branch `phase-d2-api-subagent-runtime`.
- D1 Session contract `D1-v1` is frozen; do not change its semantics.
- OpenCode runtime `d5664f38002d8326eca179cf2efb5774c8ce1247` is read-only.
- Do not register `agent_start`, `agent_status`, `agent_output`, `agent_cancel`, `agent_result`, or `agent_list` before B D2 Contract Freeze.
- Do not define public `credential_ref`, budget, permission, state, or error wire grammar before B D2 Contract Freeze.
- No Python/Node/Bun daemon and no OpenCode model/provider path.
- No resolved credential plaintext in durable metadata, output, logs, errors, fixtures, or Git.
- Every production change follows RED → GREEN → REFACTOR.

---

### Task 1: Extract shared secret redaction without changing Job behavior

**Files:**
- Create: `internal/redact/redact_test.go`
- Create: `internal/redact/redact.go`
- Modify: `internal/job/redaction.go`
- Test: `internal/job/redaction_test.go` (existing tests remain authoritative)

**Interfaces:**
- Produces: `func redact.Text(string) string`
- Consumes later: Agent output/error persistence and provider error normalization.

- [ ] **Step 1: Write the failing shared-redactor tests**

Create tests covering JSON `api_key`, key/value `token`, `Authorization: Bearer`, `Authorization: Basic`, URL user-info password, and ordinary non-secret text. Include a marker such as `D2_TEST_SECRET_7f2a` and assert it never appears after redaction.

Example desired API:

```go
func TestTextRedactsCredentialForms(t *testing.T) {
    secret := "D2_TEST_SECRET_7f2a"
    inputs := []string{
        `{"api_key":"` + secret + `"}`,
        `token=` + secret,
        `Authorization: Bearer ` + secret,
        `https://user:` + secret + `@example.invalid/path`,
    }
    for _, input := range inputs {
        if got := Text(input); strings.Contains(got, secret) {
            t.Fatalf("secret leaked: %q", got)
        }
    }
}
```

- [ ] **Step 2: Run focused test and verify RED**

Run:

```powershell
& $GO test ./internal/redact
```

Expected: package/function missing failure.

- [ ] **Step 3: Implement `internal/redact.Text` by moving the existing Job patterns unchanged**

Keep one canonical regex set in `internal/redact`. Do not add provider-specific patterns yet.

- [ ] **Step 4: Change Job `defaultRedactText` to delegate to `redact.Text`**

`internal/job/redaction.go` retains summarization/truncation helpers but no longer owns a duplicate regex set.

- [ ] **Step 5: Verify GREEN and regression**

Run:

```powershell
& $GO test ./internal/redact ./internal/job
```

Expected: PASS.

- [ ] **Step 6: Commit**

```text
refactor(security): share secret redaction
```

---

### Task 2: Extract reusable Windows DPAPI protected-data primitive

**Files:**
- Create: `internal/securedata/securedata_windows_test.go`
- Create: `internal/securedata/securedata_windows.go`
- Create: `internal/securedata/securedata_other.go`
- Modify: `internal/desktopruntime/tunnel_state_windows.go`
- Modify: `internal/desktopruntime/service_environment_windows.go`

**Interfaces:**
- Produces:

```go
func Protect(plaintext, entropy []byte) ([]byte, error)
func Unprotect(ciphertext, entropy []byte) ([]byte, error)
```

- The primitive is byte-oriented and storage/provider/ref-format neutral.

- [ ] **Step 1: Write Windows round-trip and wrong-entropy RED tests**

Tests must prove:
- non-empty plaintext round trips under identical entropy;
- wrong entropy fails or does not return original plaintext;
- empty plaintext is rejected;
- the ciphertext bytes do not contain the plaintext marker.

- [ ] **Step 2: Run focused test and verify RED**

```powershell
& $GO test ./internal/securedata
```

Expected: missing implementation failure.

- [ ] **Step 3: Implement the minimum DPAPI wrapper**

Use `windows.CryptProtectData`, `windows.CryptUnprotectData`, `windows.LocalFree`, and copied output bytes. Do not base64 encode or write files in this package.

- [ ] **Step 4: Add unsupported-platform implementation**

For `!windows`, return a stable internal unsupported error so the package compiles cross-platform without silently storing plaintext.

- [ ] **Step 5: Refactor existing desktopruntime helpers to preserve file format**

`writeProtectedText` continues to:
1. call `securedata.Protect([]byte(value), []byte(entropy))`;
2. base64 encode ciphertext;
3. `atomicfile.Write(..., 0o600)`.

`readProtectedText` continues to:
1. read file;
2. base64 decode;
3. call `securedata.Unprotect`;
4. return string plaintext.

- [ ] **Step 6: Verify GREEN and desktopruntime compatibility**

```powershell
& $GO test ./internal/securedata ./internal/desktopruntime
```

Expected: PASS.

- [ ] **Step 7: Commit**

```text
refactor(security): share windows protected data
```

---

### Task 3: Add opaque credential resolver boundary without freezing ref syntax

**Files:**
- Create: `internal/credential/resolver_test.go`
- Create: `internal/credential/resolver.go`

**Interfaces:**
- Produces:

```go
type Secret []byte

type Resolver interface {
    Resolve(context.Context, string) (Secret, error)
}

func CloneSecret(Secret) Secret
func Zero(Secret)
```

`credential_ref` remains an opaque string. There is deliberately no parser.

- [ ] **Step 1: Write RED tests for secret cloning/zeroing and opaque forwarding**

Tests must prove the resolver receives the exact reference string without interpretation and that `Zero` overwrites all bytes.

- [ ] **Step 2: Run focused test and verify RED**

```powershell
& $GO test ./internal/credential
```

- [ ] **Step 3: Implement the minimal boundary**

Do not add storage mapping, provider names, prefixes, or public errors.

- [ ] **Step 4: Verify GREEN**

```powershell
& $GO test ./internal/credential
```

- [ ] **Step 5: Commit**

```text
feat(agent): add opaque credential resolver boundary
```

---

### Task 4: Define provider-neutral internal adapter and deterministic fake-provider tests

**Files:**
- Create: `internal/agent/provider_test.go`
- Create: `internal/agent/provider.go`

**Interfaces:**
- Produces internal-only types:

```go
type ProviderRequest struct {
    AgentID      string
    SessionID    string
    Model        string
    Role         string
    Prompt       string
    Credential   credential.Secret
    MaxTurns     int
}

type ProviderEvent struct {
    Kind string
    Text string
    Tool *ToolCall
}

type ProviderResult struct {
    Summary string
    Usage   Usage
}

type ProviderAdapter interface {
    Run(context.Context, ProviderRequest, func(ProviderEvent) error) (ProviderResult, error)
}
```

These names are private Go API only; they are not MCP wire schema.

- [ ] **Step 1: Write RED tests that specify cancellation and event streaming behavior**

Use a deterministic in-test adapter. Verify a cancelled context prevents later events and returns cancellation.

- [ ] **Step 2: Run focused test and verify RED**

```powershell
& $GO test ./internal/agent -run Provider
```

- [ ] **Step 3: Add minimal provider-neutral types and validation helpers**

No Anthropic/DeepSeek HTTP implementation yet. No production fake registry.

- [ ] **Step 4: Verify GREEN**

```powershell
& $GO test ./internal/agent -run Provider
```

- [ ] **Step 5: Commit**

```text
feat(agent): add provider adapter boundary
```

---

### Task 5: Build private bounded Agent manager/persistence scaffolding

**Files:**
- Create: `internal/agent/types.go`
- Create: `internal/agent/state.go`
- Create: `internal/agent/persistence.go`
- Create: `internal/agent/output.go`
- Create: `internal/agent/manager.go`
- Create: `internal/agent/manager_test.go`
- Create: `internal/agent/persistence_test.go`

**Interfaces:**
- Consumes: `credential.Resolver`, `ProviderAdapter`, shared `redact.Text`.
- Introduce an internal session binding abstraction that returns canonical D1-derived fields; do not accept an arbitrary caller workspace:

```go
type SessionBinding struct {
    SessionID      string
    Workspace      string
    RuntimeScope   string
    PermissionMode string
}

type SessionResolver interface {
    ResolveAgentSession(context.Context, string) (SessionBinding, error)
}
```

- Manager internal API may expose `Start`, `Get`, `List`, `Cancel`, `Output`, `Result`, and `Recover`, but no MCP handler or schema is added.

- [ ] **Step 1: Write RED test for mandatory Session binding**

Start with an unknown/empty session and assert rejection before provider execution. Start with a known session and verify persisted metadata uses the resolver's canonical workspace, not any model/provider value.

- [ ] **Step 2: Write RED test for credential non-persistence**

Use `credential_ref="opaque-test-ref"` and a resolver returning `D2_TEST_SECRET_7f2a`. After completion, recursively inspect the manager storage root and assert the secret marker is absent while the opaque ref may be present.

- [ ] **Step 3: Write RED test for bounded queue/concurrency**

Configure global=2, per-session=1. Start two agents in one session and one in another. Verify no more than two provider runs are active globally and no more than one per session; queued work starts deterministically when a slot releases.

- [ ] **Step 4: Write RED test for real cancellation semantics**

Provider blocks on context. `Cancel` must cancel the context and wait for provider termination before returning terminal internal status. A second session's run remains active.

- [ ] **Step 5: Write RED test for bounded redacted cursor output**

Append provider events containing the secret marker and normal text. Verify durable output omits the secret, cursor advances monotonically, and reads are bounded by configured bytes.

- [ ] **Step 6: Write RED restart reconciliation test**

Persist a nonterminal record, construct a fresh manager over the same storage, run `Recover`, and verify it becomes terminal under a private internal restart state. Do not publish that string through MCP.

- [ ] **Step 7: Implement the minimum manager mechanics**

Follow Job Manager patterns:
- persist record before queueing;
- bounded queue and slot counters;
- resolve session before credential/provider execution;
- resolve credential only immediately before provider run;
- `defer credential.Zero(secret)`;
- redaction before durable output/error persistence;
- cancel context and wait for completion;
- atomically persist metadata;
- clear in-memory prompt/secret-bearing request data at terminal transition.

- [ ] **Step 8: Verify focused Agent tests GREEN**

```powershell
& $GO test ./internal/agent ./internal/credential ./internal/redact ./internal/securedata
```

- [ ] **Step 9: Commit**

```text
feat(agent): add private durable subagent manager foundation
```

---

### Task 6: Pre-freeze integration guardrails and full regression

**Files:**
- Create or modify tests only as needed to assert no public D2 tools are registered yet.
- Do not add production `internal/app` agent handlers/specs.

- [ ] **Step 1: Add a guard test proving public `agent_*` tools are absent before freeze**

The test should inspect the existing runtime tool registry and assert none of the six D2 names are present. This is a temporary pre-freeze safety guard and must be deliberately replaced when the frozen B contract is integrated.

- [ ] **Step 2: Run focused app registry tests**

```powershell
& $GO test ./internal/app ./internal/mcp
```

Expected: PASS.

- [ ] **Step 3: Run full regression**

```powershell
& $GO test ./...
```

Expected: PASS.

- [ ] **Step 4: Inspect scope**

```powershell
git status --short
git diff --stat 162e98d128166d0efe08525fa7dcf79a016b72a9
git grep -n 'agent_start\|agent_status\|agent_output\|agent_cancel\|agent_result\|agent_list' -- internal/app internal/mcp
```

Expected before B freeze: no production public tool registration.

- [ ] **Step 5: Stop at contract boundary**

Report:

```text
B_D2_CONTRACT_SHA = BLOCKED_PENDING_CENTRAL_FREEZE
PUBLIC_AGENT_SCHEMA = NOT_REGISTERED
PRECONTRACT_FOUNDATION = VERIFIED
```

Do not push a claim of final D2 PASS until the frozen contract is integrated and real-provider/session/cancel/restart/security acceptance is complete.
