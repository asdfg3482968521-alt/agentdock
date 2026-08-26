# D3-F1 Multi-turn Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement restart-safe, exactly-once logical supervisor messaging over one durable D3 OpenCode native conversation per top-level AgentDock subagent.

**Architecture:** Keep D3-B as the identity/ownership authority. Add an additive durable supervisor-message ledger scoped to each native root binding, serialize admission/native submission per subagent, use the stable `supervisor_message_id` as OpenCode `PromptRequest.MessageID`, and reconcile unknown dispatch outcomes against the exact mapped OpenCode root session after restart. Permission approval transport is left to D3-S1, but F1 must expose the frozen ordering/idempotency storage hooks.

**Tech Stack:** Go, existing `internal/session`, `internal/nativebridge`, `internal/opencodenative`, atomic session persistence, existing Go test suite.

**Spec:** `docs/superpowers/specs/2026-08-26-d3-f0-multiturn-contract.md`

## Global Constraints

- Base capability may not regress from `4cf08c3f76a945b7284e8f19befdda9fb3eb9ab6`.
- OpenCode runtime source change is not required or authorized.
- `TRUE_INTERRUPTED_EXECUTION_RESUME = NOT_SUPPORTED`.
- Resume semantics are `MESSAGE_CONTINUE_ONLY`: same native conversation plus a new message.
- One AgentDock top-level subagent maps to one exact OpenCode root native SessionID.
- OpenCode child sessions remain descendants, never top-level AgentDock subagents.
- Existing provider=`opencode` bounded-worker compatibility and existing Agent/Session/Job behavior remain additive-compatible.
- All message/approval persistence must remain inside the canonical AgentDock session-owned state.

---

### Task 1: Add durable supervisor-message and approval ledger types

**Files:**
- Modify: `internal/session/native_binding.go`
- Modify: `internal/session/types.go` only if JSON clone/persistence plumbing requires it
- Test: `internal/session/native_multiturn_binding_test.go`

**Interfaces:**
- Consumes: existing `NativeSessionBinding`, `Manager.UpdateNativeSessionBinding`, atomic session persistence.
- Produces:
  - `NativeSupervisorMessage`
  - `NativePermissionApproval`
  - message/approval state enums
  - durable `NextSupervisorMessageSeq`, `NextApprovalSeq`, `LastOwnedEventCursor`
  - manager methods for atomic admission/update.

- [ ] **Step 1: Write failing persistence/normalization tests**

Create tests that bind one native session, admit two messages, persist/reload the Session manager, and assert message IDs, `message_seq`, next allocator values, approval records, and event cursor are unchanged. Include a duplicate client-message admission assertion returning the original record.

Run:

```powershell
go test ./internal/session -run 'TestNative(Multiturn|Supervisor|Approval)' -count=1
```

Expected: FAIL because the F1 ledger types/methods do not exist.

- [ ] **Step 2: Add additive durable record types**

Add record types equivalent to:

```go
type NativeSupervisorMessageState string

const (
    NativeSupervisorQueued      NativeSupervisorMessageState = "QUEUED"
    NativeSupervisorDispatching NativeSupervisorMessageState = "DISPATCHING"
    NativeSupervisorAccepted    NativeSupervisorMessageState = "ACCEPTED"
    NativeSupervisorExecuting   NativeSupervisorMessageState = "EXECUTING"
    NativeSupervisorTerminal    NativeSupervisorMessageState = "TERMINAL"
)

type NativeSupervisorMessage struct {
    ClientMessageID     string                       `json:"client_message_id"`
    SupervisorMessageID string                       `json:"supervisor_message_id"`
    MessageSeq          int64                        `json:"message_seq"`
    Kind                string                       `json:"kind"`
    State               NativeSupervisorMessageState `json:"state"`
    TerminalOutcome     string                       `json:"terminal_outcome,omitempty"`
    NativeMessageID     string                       `json:"native_message_id,omitempty"`
    CreatedAt           time.Time                    `json:"created_at"`
    UpdatedAt           time.Time                    `json:"updated_at"`
}

type NativePermissionApproval struct {
    ApprovalRequestID         string `json:"approval_request_id"`
    NativePermissionRequestID string `json:"native_permission_request_id"`
    SupervisorMessageID       string `json:"supervisor_message_id"`
    MessageSeq                int64  `json:"message_seq"`
    ApprovalSeq               int64  `json:"approval_seq"`
    ObservedEventCursor       int64  `json:"observed_event_cursor"`
    Decision                  string `json:"decision,omitempty"`
    ClientDecisionID          string `json:"client_decision_id,omitempty"`
    NativeDecisionDelivered   bool   `json:"native_decision_delivered,omitempty"`
}
```

Store them additively on `NativeSessionBinding` (or an equivalent session-owned durable structure if a size-safe sidecar is chosen). Do not remove or rename existing fields.

- [ ] **Step 3: Add atomic manager operations**

Implement session-authority methods with the existing manager lock + persist boundary:

```go
AdmitNativeSupervisorMessage(sessionID, subagentID, clientMessageID, kind, supervisorMessageID string) (NativeSupervisorMessage, bool, error)
UpdateNativeSupervisorMessage(sessionID, subagentID, supervisorMessageID string, update func(*NativeSupervisorMessage) error) (NativeSupervisorMessage, error)
AdmitNativePermissionApproval(sessionID, subagentID string, approval NativePermissionApproval) (NativePermissionApproval, bool, error)
DecideNativePermissionApproval(sessionID, subagentID, approvalRequestID, clientDecisionID, decision string) (NativePermissionApproval, bool, error)
```

The returned `bool` is `created` for admission and `decision_applied` for decision. The duplicate path returns the existing durable record.

- [ ] **Step 4: Run focused tests**

```powershell
go test ./internal/session -run 'TestNative(Multiturn|Supervisor|Approval)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/session
git commit -m "feat: add durable native multiturn ledger"
```

### Task 2: Implement stable supervisor message identity and exactly-once admission

**Files:**
- Modify: `internal/nativebridge/types.go`
- Modify: `internal/nativebridge/manager.go`
- Create: `internal/nativebridge/multiturn.go`
- Test: `internal/nativebridge/multiturn_test.go`

**Interfaces:**
- Consumes: Task 1 ledger methods and current D3-B `Attach`, `ContinueConversation`, `ListMessages`.
- Produces:
  - `SupervisorMessageRequest`
  - `SupervisorMessageResult`
  - `Manager.SubmitSupervisorMessage`.

- [ ] **Step 1: Write failing sequential and concurrent duplicate tests**

Use a fake native client that records `PromptRequest.MessageID`. Cover:
1. same `client_message_id` twice,
2. concurrent duplicate goroutines,
3. two distinct concurrent messages.

Assert one logical record per duplicate key, one stable `supervisor_message_id`, and ascending durable `message_seq`.

Run:

```powershell
go test ./internal/nativebridge -run 'TestSupervisorMessage(SequentialDuplicate|ConcurrentDuplicate|ConcurrentDistinct)' -count=1
```

Expected: FAIL before implementation.

- [ ] **Step 2: Implement ID allocation and admission-before-send**

Add:

```go
type SupervisorMessageRequest struct {
    AgentDockSessionID string
    SubagentID         string
    Workspace          string
    ClientMessageID    string
    Kind               string
    Prompt             opencodenative.PromptRequest
}
```

Inside the existing per-subagent keyed lock:
1. validate ownership via `Attach`,
2. allocate a `supervisor_message_id` only for a newly admitted logical message,
3. durably admit the record and sequence,
4. overwrite `PromptRequest.MessageID` with the durable supervisor ID,
5. transition to `DISPATCHING`,
6. submit to the same `OpenCodeSessionID`,
7. persist `ACCEPTED` and native message provenance when available.

Do not derive a new OpenCode root session.

- [ ] **Step 3: Preserve busy correction and follow-up semantics**

Permit `TASK`, `FOLLOWUP`, and `CORRECTION` admission while the conversation is active. Do not call `Abort` implicitly. Do not block correction admission on the prior execution becoming terminal.

- [ ] **Step 4: Run focused tests**

```powershell
go test ./internal/nativebridge -run 'TestSupervisorMessage' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/nativebridge
git commit -m "feat: add ordered native supervisor messaging"
```

### Task 3: Add restart reconciliation and terminal-execution separation

**Files:**
- Modify: `internal/nativebridge/multiturn.go`
- Modify: `internal/nativebridge/manager.go`
- Test: `internal/nativebridge/multiturn_recovery_test.go`

**Interfaces:**
- Consumes: durable `DISPATCHING` records and `ListNativeMessages`.
- Produces: restart-safe `ReconcileSupervisorMessages` and terminal-outcome update behavior.

- [ ] **Step 1: Write failing crash-window tests**

Cover:
- record persisted but native POST never happened,
- native POST happened but `ACCEPTED` was not persisted,
- execution terminal `SUCCEEDED`, `FAILED`, `CANCELLED`, `TIMED_OUT`, `INTERRUPTED`, followed by a new message.

Use `MessageInfo.ID`/`PromptRequest.MessageID` fixtures to prove reconciliation uses the stable supervisor identity.

Run:

```powershell
go test ./internal/nativebridge -run 'TestSupervisorRecovery|TestTerminalExecutionAllowsFollowup' -count=1
```

Expected: FAIL.

- [ ] **Step 2: Implement bounded reconciliation**

For each nonterminal/unknown durable dispatch record in ascending `message_seq`:
1. list messages from the exact mapped root `OpenCodeSessionID`,
2. find the durable `supervisor_message_id`,
3. if found, persist accepted/native provenance and never resend,
4. if absent after the bounded reconciliation policy, resend using the same `PromptRequest.MessageID`.

Never allocate a replacement supervisor ID or OpenCode session.

- [ ] **Step 3: Separate turn terminal state from conversation state**

Terminal execution updates only the message execution outcome and `NativeSessionBinding.ExecutionState`/summary fields needed for compatibility. Keep `NativeSessionBinding.State = ACTIVE` unless D3-B authority marks the conversation `CLOSED` or `LOST`.

Do not implement true interrupted execution resume.

- [ ] **Step 4: Run focused tests**

```powershell
go test ./internal/nativebridge -run 'TestSupervisorRecovery|TestTerminalExecutionAllowsFollowup' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/nativebridge
git commit -m "feat: reconcile native supervisor messages after restart"
```

### Task 4: Make native event cursor application idempotent and message-aware

**Files:**
- Modify: `internal/nativebridge/manager.go`
- Modify: `internal/nativebridge/multiturn.go`
- Test: `internal/nativebridge/multiturn_event_test.go`

**Interfaces:**
- Consumes: D3-B descendant ownership closure and SSE events.
- Produces: duplicate-safe AgentDock-owned event cursor and message/approval linkage.

- [ ] **Step 1: Write failing duplicate-event tests**

Feed the same owned native event twice and assert:
- AgentDock event cursor advances once,
- unrelated descendant/foreign events remain filtered exactly as D3-B requires,
- message ordering is unchanged,
- an approval request cannot be recorded before its parent supervisor message.

Run:

```powershell
go test ./internal/nativebridge -run 'TestMultiturnEvent' -count=1
```

Expected: FAIL.

- [ ] **Step 2: Add durable event dedup provenance**

Persist stable native event identity/provenance before advancing `LastOwnedEventCursor`. If the same event identity is observed again, return without incrementing or re-emitting.

Continue to use D3-B `descendantOwnershipClosure`.

- [ ] **Step 3: Add approval-ordering hook only**

When an owned permission event is recognized, expose enough information to D3-S1 to atomically call the Task 1 approval-admission method with:
- parent `supervisor_message_id`,
- parent `message_seq`,
- current AgentDock event cursor,
- native permission request ID.

Do not implement UI/approval transport in F1.

- [ ] **Step 4: Run focused tests**

```powershell
go test ./internal/nativebridge -run 'TestMultiturnEvent' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/nativebridge
git commit -m "feat: make native event ordering restart safe"
```

### Task 5: Wire public supervisor operations without changing frozen D2 schemas

**Files:**
- Modify: `internal/app/agent_backend_routing.go` or the D3 supervisor-specific app file that exists on the F1 integration baseline
- Modify: `internal/app/specs.go` only for new additive D3 tools
- Add tests under: `internal/app/*supervisor*_test.go`
- Add MCP outer test only if the F1 branch exposes these operations through MCP.

**Interfaces:**
- Consumes: Task 2-4 nativebridge APIs.
- Produces: bounded D3 supervisor message/observe/correct operations scoped by AgentDock Session + subagent.

- [ ] **Step 1: Write failing app-layer contract tests**

Assert that callers cannot:
- target a foreign subagent/session,
- override workspace/runtime/permission bindings,
- omit `client_message_id`,
- use an approval decision as a broad permission grant.

Assert retries return the same durable supervisor message resource.

- [ ] **Step 2: Add minimal additive handlers**

Route through existing AgentDock Session authority and F1 nativebridge methods. Do not rewrite Session/Job/Agent Manager semantics and do not change the existing six D2 Agent schemas.

- [ ] **Step 3: Run focused app tests**

```powershell
go test ./internal/app -run 'Test.*Supervisor|Test.*Multiturn' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```powershell
git add internal/app
git commit -m "feat: expose durable d3 supervisor messaging"
```

### Task 6: Full regression, restart matrix, and freeze

**Files:**
- Test only; production files should already be complete.
- Update docs only if implementation names differ while preserving the F0 semantics.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: a clean, pushed F1 SHA ready for Central/S1.

- [ ] **Step 1: Run the frozen F0 matrix**

Run the exact focused tests for:
- task -> response -> follow-up,
- busy -> correction,
- success/failure/cancel -> follow-up,
- sequential/concurrent duplicate,
- concurrent distinct order,
- restart/re-init order,
- cursor duplicate,
- approval duplicate/restart ordering.

Expected: PASS.

- [ ] **Step 2: Run package regressions**

```powershell
go test ./internal/session ./internal/nativebridge ./internal/opencodenative ./internal/agent ./internal/job ./internal/app ./internal/mcp -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full regression**

```powershell
go test ./... -count=1
```

Expected: PASS, excluding only pre-existing environment-specific gates explicitly documented by Central.

- [ ] **Step 4: Verify forbidden semantic regression is absent**

Search the diff and tests to prove:
- no true interrupted execution resume,
- no replacement native root session on follow-up/restart,
- no promotion of OpenCode child sessions,
- no changes to OpenCode runtime source.

- [ ] **Step 5: Freeze and push**

```powershell
git status --short
git diff --check
git push -u origin <f1-branch>
git rev-parse HEAD
git ls-remote origin refs/heads/<f1-branch>
git status --porcelain
```

Expected: local SHA equals remote SHA and worktree is clean.