# D3-F0 Multi-turn Contract + Test Design

## 1. Identity and scope

- `BASE_SHA = 4cf08c3f76a945b7284e8f19befdda9fb3eb9ab6`
- This line freezes contract and tests only. It does **not** implement production orchestration.
- OpenCode runtime source changes are not required or authorized.
- One AgentDock top-level subagent remains mapped to one AgentDock Session and one exact OpenCode root native SessionID.
- OpenCode child sessions remain descendants and never become top-level AgentDock subagents.
- Existing provider=`opencode` bounded-worker compatibility and existing Agent/Session/Job schemas remain compatible.
- `TRUE_INTERRUPTED_EXECUTION_RESUME = NOT_SUPPORTED`.
- Resume means **same native conversation + a new message** (`MESSAGE_CONTINUE_ONLY`).

## 2. Frozen message identity contract

### 2.1 Scope keys

Every supervisor message is scoped by the tuple:

`(agentdock_session_id, subagent_id, client_message_id)`

`client_message_id` is required for mutating supervisor message admission.

The tuple is the idempotency key. Reusing the same `client_message_id` under another AgentDock Session or another subagent is a different logical message.

### 2.2 Supervisor message ID

On first successful admission AgentDock allocates exactly one `supervisor_message_id`, stores it durably together with the idempotency key, and returns it.

Retries with the same scope key MUST return the same `supervisor_message_id`; they MUST NOT allocate another supervisor message or issue another native submission.

The same `supervisor_message_id` MUST be forwarded as OpenCode `PromptRequest.MessageID` for every attempt/reconciliation of that logical message. This stable native identity is required for restart reconciliation.

A supervisor message ID is immutable and never reused by another logical message.

## 3. Frozen ordering contract

### 3.1 Per-subagent linearization

All message-admission and message-dispatch mutations for one top-level subagent are serialized through one per-subagent linearization boundary.

Each newly admitted logical message receives a strictly increasing durable `message_seq`.

For concurrent distinct messages, the winner of the durable admission transaction receives the lower sequence. The resulting persisted sequence is the canonical order. After that point all readers, retries, restarts, dispatchers, and UI projections MUST observe the same order.

No cross-subagent global ordering is required.

### 3.2 Dispatch order

A dispatcher may have only one admission/dispatch mutation in flight per subagent. Native submissions are issued in ascending `message_seq`.

Busy corrections and active-turn follow-ups are allowed to be submitted to the same native conversation, but their admission and native POST still pass through the same ordering boundary. "Serialization" therefore means deterministic mutation/POST order, not "wait for the previous OpenCode turn to become terminal."

### 3.3 Cursor/event order

AgentDock owns a strictly increasing durable `event_cursor` per top-level subagent.

An owned native event is applied at most once. A duplicate native event identity MUST NOT advance the cursor twice.

Where the native source exposes a stable event sequence, AgentDock persists it as provenance but does not replace the AgentDock-owned cursor.

Message records carry their `message_seq`; approval records carry both their parent `message_seq` and a strictly increasing per-subagent `approval_seq`.

## 4. Frozen duplicate-protection contract

### 4.1 Sequential duplicate

Submitting the same scope key twice returns one logical message, one `supervisor_message_id`, one `message_seq`, and at most one successful native submission.

### 4.2 Concurrent duplicate

Concurrent submissions of the same scope key are linearized. Exactly one admission wins; every caller receives the same durable record. No caller may trigger an independent native message.

### 4.3 Restart ambiguity

The durable message record MUST be persisted before the first native submission.

If AgentDock restarts while a message is in `DISPATCHING`/unknown outcome, F1 MUST reconcile the exact OpenCode root session by listing native messages and looking for `supervisor_message_id` as the native `messageID`.

- Found: mark the existing record accepted/submitted; do not resend.
- Not found after bounded reconciliation: resend the **same** `supervisor_message_id`, never allocate a replacement message.
- A restart never creates a replacement OpenCode root session for a healthy mapped conversation.

## 5. Execution terminal state versus conversation lifetime

Conversation state and execution/turn state are distinct.

A successful, failed, cancelled, timed-out, or interrupted execution is terminal for that execution record only. It does not close the mapped native conversation.

After `SUCCEEDED`, `FAILED`, `CANCELLED`, `TIMED_OUT`, or `INTERRUPTED`, a new supervisor message may be admitted with the next `message_seq` and sent to the same `OpenCodeSessionID`.

`INTERRUPTED` does **not** authorize execution resume. The only supported continuation is a new message on the same native conversation.

Conversation replacement is forbidden unless a separate future contract explicitly closes/replaces it. D3-B `LOST` semantics remain authoritative.

## 6. Busy correction contract

A correction submitted while the native turn is active is a new logical supervisor message with its own idempotency key, `supervisor_message_id`, and next `message_seq`.

It is delivered to the same OpenCode root native session using message-continue semantics.

Busy correction is non-preemptive:
- no implicit abort,
- no hidden execution-resume operation,
- no root-session replacement.

`ACTIVE_CORRECTION_PREEMPTIVE = NO`.

## 7. Restart/idempotency contract

The durable state required to survive restart includes at least:

- idempotency scope key,
- `supervisor_message_id`,
- `message_seq`,
- message kind (`TASK`, `FOLLOWUP`, `CORRECTION`),
- dispatch state,
- native message identity/provenance when observed,
- per-message terminal execution outcome,
- next sequence allocator,
- owned event cursor/dedup provenance,
- permission approval records described below.

Re-initialization reconstructs the same canonical message order. Already admitted duplicates remain duplicates after restart. Already decided approvals remain decided after restart.

Recovery MUST NOT claim `TRUE_INTERRUPTED_EXECUTION_RESUME`.

## 8. D3-S1 durable permission-approval ordering input

### 8.1 Approval request identity

Every native permission request that is surfaced to the supervisor is durably bound to:

`(agentdock_session_id, subagent_id, supervisor_message_id, native_permission_request_id)`

The native permission request ID is the request idempotency key within that parent supervisor message.

First admission allocates one immutable `approval_request_id` and one strictly increasing per-subagent `approval_seq`.

Duplicate observation of the same native permission request returns the existing approval record and MUST NOT surface a second logical approval request.

### 8.2 Ordering

The parent supervisor message MUST already be durable before its approval request can be durable.

An approval record includes:
- parent `message_seq`,
- `approval_seq`,
- observed AgentDock `event_cursor`,
- `approval_request_id`,
- native permission request identity,
- decision state.

UI/API projections sort supervisor messages by `message_seq`. Approval requests are attached to their parent message and ordered by `approval_seq`; an approval can never appear before its parent message.

### 8.3 Bounded decision semantics

A permission decision is bounded to exactly one `approval_request_id`. It never authorizes another subagent, another message, another native request, or a future request.

The first durable terminal decision wins.

A retry of the same decision is idempotent and returns the existing terminal decision without a second native decision delivery.

A conflicting later decision is rejected and cannot overwrite the first decision.

The durable decision is written before native delivery. On restart, undecided requests remain pending; decided requests reconcile native delivery using the same immutable approval identity and are never re-decided.

## 9. Frozen logical states

Recommended message states for D3-F1:

`QUEUED -> DISPATCHING -> ACCEPTED -> EXECUTING -> TERMINAL`

`TERMINAL` carries a separate outcome:
`SUCCEEDED | FAILED | CANCELLED | TIMED_OUT | INTERRUPTED`.

Recovery may revisit `DISPATCHING` only through identity reconciliation; it may not manufacture a new logical message.

Recommended approval states for D3-S1:

`PENDING -> DECIDED`

with terminal decision value such as `ALLOW | DENY` (transport-specific names may differ). First decision is immutable.

## 10. Test matrix

The F0 contract test suite freezes at least these scenarios:

| Scenario | Frozen expectation |
| --- | --- |
| task -> response -> follow-up | next `message_seq`, same native conversation |
| busy -> correction | new ordered message, same native conversation, no implicit abort |
| success -> follow-up | allowed after execution terminal |
| failure -> follow-up | allowed after execution terminal |
| cancel -> new same-session message | allowed, same OpenCode root session |
| duplicate `client_message_id` exactly once | same supervisor ID/seq, one native dispatch |
| concurrent duplicate exactly once | one admission under race |
| two simultaneous supervisor messages deterministic | durable total order; replay preserves it |
| restart/re-init order persists | message IDs/sequences/dedup map preserved |
| cursor duplicate | duplicate native event does not advance AgentDock cursor |
| approval duplicate request | one approval record |
| approval duplicate decision | one terminal decision/native delivery |
| approval conflicting decision | rejected; first decision preserved |
| approval restart | request order and terminal decision survive re-init |

## 11. D3-F1 implementation boundary

D3-F1 should implement the frozen logical contract without changing the OpenCode runtime source. It should add durable per-subagent supervisor message storage/atomic admission, ordered same-session submission, recovery reconciliation using stable message IDs, and event-cursor deduplication.

It must reuse D3-B native mapping authority and never turn OpenCode descendants into AgentDock top-level subagents.

The detailed F1 execution plan is in:

`docs/superpowers/plans/2026-08-26-d3-f1-multiturn-orchestration.md`

## 12. Freeze result

- `D3_F0_CONTRACT = FROZEN`
- `TEST_MATRIX = READY`
- `D3_S1_ORDERING_INPUT = READY`