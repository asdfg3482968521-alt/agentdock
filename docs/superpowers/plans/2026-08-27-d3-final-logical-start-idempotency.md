# D3 Final Logical-Start Idempotency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make keyed public `agent_start` admit exactly one durable AgentDock agent and one OpenCode root.

**Architecture:** Add `client_request_id` plus additive response flags. Persist the key and canonical SHA-256 effective-request fingerprint on the existing Agent record; rebuild its index during recovery and serialize keyed admission before scheduling. Existing Native Bridge bindings continue to use the admitted stable Agent ID.

**Tech Stack:** Go, JSON Schema draft 2020-12, existing JSON file persistence, Go test.

**Spec:** `docs/superpowers/specs/2026-08-27-d3-final-logical-start-idempotency-design.md`

## Global Constraints

- Base is `57e5c9fcec025e303d673b419260a20d55388f97`; do not use moving upstream.
- Do not change Desktop, OpenCode runtime, Tray, model policy, or workspace-root behavior.
- `client_request_id` is optional; when supplied it has length 1–256.
- Scope is resolved AgentDock session authority plus the key; legacy unkeyed behavior remains unchanged.
- Same key with changed effective request returns `IDEMPOTENCY_CONFLICT` before Agent or root creation.

---

## File structure

- `internal/app/d2contract/agent-start.schema.json`: keyed public contract and replay flags.
- `internal/app/agent_public_schema_test.go`: schema golden and validation checks.
- `internal/agent/types.go`: key, outcome, persisted identity, conflict sentinel.
- `internal/agent/manager.go`: fingerprint, durable lookup, locking, recovery index.
- `internal/agent/manager_test.go`: concurrency, conflict, restart, failure coverage.
- `internal/agent/error_contract.go`: public conflict mapping.
- `internal/app/agent_public_tools.go` and `agent_public_tools_test.go`: public wiring and regression.
- `internal/app/opencode_native_autonomous_backend_test.go`: one-root proof.

### Task 1: Contract and stable error

**Files:** Modify `agent-start.schema.json`, `agent_public_schema_test.go`, `internal/agent/types.go`, and `error_contract.go`.

**Produces:** `StartRequest.ClientRequestID string`, `StartOutcome { Record Record; Created bool; Replayed bool }`, and `ErrIdempotencyConflict`.

- [ ] **Step 1: Write failing schema tests.**

```go
args := validPublicAgentStart("session_schema")
args["client_request_id"] = "logical-start-001"
if err := validateFrozenAgentInput("agent_start", args); err != nil { t.Fatal(err) }
```

Also assert empty and 257-character keys fail; successful output exposes boolean `created` and `replayed`.

- [ ] **Step 2: Verify failure.**

Run: `go test -count=1 ./internal/app -run 'TestD3.*AgentStart.*Schema|TestD2AgentRegistryMatchesFrozen'`

Expected: FAIL because the current schema rejects the extra property.

- [ ] **Step 3: Add the minimum schema and error mapping.**

```json
"client_request_id": {"type":"string","minLength":1,"maxLength":256,"description":"Stable caller idempotency key for exactly-once logical-start admission."}
```

Add output booleans, `IDEMPOTENCY_CONFLICT` in `x-errors`, its sentinel, and `AgentErrorFrom` mapping. Update only the `agent_start` SHA-256 golden.

- [ ] **Step 4: Verify and commit.**

Run: `go test -count=1 ./internal/app -run 'TestD3.*AgentStart.*Schema|TestD2AgentRegistryMatchesFrozen|TestFrozenAgentStartSchemaForbids'`

Commit: `git add internal/app/d2contract/agent-start.schema.json internal/app/agent_public_schema_test.go internal/agent/types.go internal/agent/error_contract.go && git commit -m "feat: expose keyed agent start idempotency"`

### Task 2: Durable keyed admission

**Files:** Modify `internal/agent/types.go`, `manager.go`, and `manager_test.go`.

**Produces:** `func (m *Manager) StartAdmission(context.Context, StartRequest) (StartOutcome, error)`; legacy `Start` delegates to it. `Record` stores `ClientRequestID` and `RequestFingerprint`.

- [ ] **Step 1: Write failing manager tests.**

```go
first, _ := manager.StartAdmission(ctx, request)
second, _ := manager.StartAdmission(ctx, request)
if !first.Created || !second.Replayed || first.Record.AgentID != second.Record.AgentID { t.Fatal("duplicate created another agent") }
```

Use a blocking provider for concurrent same-key calls. Cover changed-prompt conflict, other-session key reuse, terminal-failure replay, and reconstructed-manager replay.

- [ ] **Step 2: Verify failure.**

Run: `go test -count=1 ./internal/agent -run 'Test.*(Admission|Idempotency|Duplicate|Restart)'`

Expected: FAIL because `StartAdmission` does not exist.

- [ ] **Step 3: Implement the admission boundary.**

Normalize and authorize as the current `Start` path first, then hash a fixed JSON struct:

```go
type startFingerprint struct { SessionID, Provider, Model, Role, CredentialRef, Prompt, Permission string; MaxRequests int; TimeoutMS int64 }
```

For a key, lock `SessionID + "\x00" + ClientRequestID`, compare any durable/live record fingerprint, atomically save the winning record before queue insertion, and return replay for an exact duplicate. Blank keys keep the original allocation behavior.

- [ ] **Step 4: Rebuild recovery index.**

Index persisted keyed records before accepting starts. Treat duplicate persisted scope/key ownership as corrupt rather than choosing a winner. Keep an interrupted or failed record bound to its key.

- [ ] **Step 5: Verify and commit.**

Run: `go test -count=1 ./internal/agent`

Commit: `git add internal/agent/types.go internal/agent/manager.go internal/agent/manager_test.go && git commit -m "feat: persist keyed agent start admissions"`

### Task 3: Public result wiring

**Files:** Modify `internal/app/agent_public_tools.go`, `agent_public_tools_test.go`, and `agent_public_validation_test.go`.

**Consumes:** `StartAdmission`. **Produces:** `{ok, agent, created, replayed}`.

- [ ] **Step 1: Write failing public tests.**

```go
if first["created"] != true || second["replayed"] != true { t.Fatalf("first=%#v second=%#v", first, second) }
if publicAgentFromResult(t, first)["agent_id"] != publicAgentFromResult(t, second)["agent_id"] { t.Fatal("replay changed id") }
```

Cover sequential, pending, concurrent, scope-isolated, failed, conflict, and no-key paths.

- [ ] **Step 2: Verify failure.**

Run: `go test -count=1 ./internal/app -run 'TestD3.*(Idempotency|Duplicate|Legacy)|TestD2AgentStartPublicHandler'`

- [ ] **Step 3: Wire the outcome and verify.**

Read validated `client_request_id`, call `StartAdmission`, and return:

```go
Result{"ok": true, "agent": resource, "created": outcome.Created, "replayed": outcome.Replayed}
```

Run: `go test -count=1 ./internal/app`

- [ ] **Step 4: Commit.**

Commit: `git add internal/app/agent_public_tools.go internal/app/agent_public_tools_test.go internal/app/agent_public_validation_test.go && git commit -m "feat: replay keyed public agent starts"`

### Task 4: Native-root proof and full verification

**Files:** Modify `internal/app/opencode_native_autonomous_backend_test.go`.

- [ ] **Step 1: Add a counting native-runner test.**

With one public key, assert equal calls receive one Agent ID and invoke root start once. Change the prompt and assert `IDEMPOTENCY_CONFLICT` with root count still one.

- [ ] **Step 2: Verify focused native proof.**

Run: `go test -count=1 ./internal/app -run 'Test.*(OpenCodeNative|D3G1|Idempotency)'`

Expected: PASS, root count 1.

- [ ] **Step 3: Run all local gates.**

Run: `go test -count=1 ./internal/agent && go test -count=1 ./internal/session && go test -count=1 ./internal/job && go test -count=1 ./internal/mcp && go test -count=1 ./internal/app && git diff --check && go test -count=1 ./...`

- [ ] **Step 4: Commit and inspect final state.**

Commit: `git add internal/app/opencode_native_autonomous_backend_test.go && git commit -m "test: prove keyed starts create one native root"`

Run: `git status --short && git rev-parse HEAD`

## Self-review

- Tasks 1–3 cover schema, backwards compatibility, fingerprint, scope, durable state, concurrency, restart, failure, and conflict behavior.
- Task 4 proves dedupe precedes native root creation and runs all required local regression gates.
- Names are consistent: `ClientRequestID`, `RequestFingerprint`, `StartAdmission`, `StartOutcome`, `ErrIdempotencyConflict`, `created`, and `replayed`.
