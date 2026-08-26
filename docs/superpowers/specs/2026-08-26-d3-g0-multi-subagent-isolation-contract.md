# D3-G0 Multi-subagent Isolation Contract

## Goal

Freeze the ownership, negative-control, restart, cursor, and sandbox-isolation contract for a future D3-G1 production subagent pool without implementing that pool in G0.

## Frozen inputs

- BASE_SHA: `4cf08c3f76a945b7284e8f19befdda9fb3eb9ab6`
- Branch: `phase-d3-g0-subagent-pool-contract-tests`
- D3_OPENCODE_RUNTIME_SHA: `d5664f38002d8326eca179cf2efb5774c8ce1247`
- D3-A: PASS / CLOSED
- D3-B: PASS / CLOSED
- OpenCode runtime source change: NOT REQUIRED / NOT AUTHORIZED
- G0 scope: contract/tests only; production pool implementation is forbidden.

## Root identity and ownership

A, B, and C are three top-level subagents. Each root owns a distinct tuple:

`SubagentID + AgentDock SessionID + exact root OpenCodeSessionID + Workspace + RuntimeScope + status/events/result stream + sandbox capability`

The frozen topology is:

`one AgentDock top-level subagent <-> one AgentDock Session <-> one exact OpenCode root Native SessionID`

OpenCode child sessions remain descendants of that exact root. A child never becomes a fourth AgentDock top-level subagent and cannot replace a LOST root.

No root may reuse another root's AgentDock Session, root OpenCode SessionID, Workspace, RuntimeScope, event cursor, or sandbox capability snapshot.

## Supervisor targeting semantics

Targeted correction of A means `MESSAGE_CONTINUE` on A's exact root only. It does not imply preemptive interruption and does not change the frozen capability facts:

- `SAME_SESSION_CORRECTION = PASS`
- `FOLLOWUP_DURING_ACTIVE_TURN = SUPPORTED`
- `ACTIVE_CORRECTION_PREEMPTIVE = NO`
- `TRUE_INTERRUPTED_EXECUTION_RESUME = NOT_SUPPORTED`
- `RESUME_SEMANTICS = MESSAGE_CONTINUE_ONLY`

Targeted cancel of B means abort/cancel only B's current exact-root execution and its owned descendant execution closure. It must not cancel A or C.

After targeted correction of A and targeted cancel of B, C must remain independently attachable/observable and its durable identity, status, events, result, cursor, workspace, RuntimeScope, and sandbox capability must remain unchanged.

## Negative control matrix

The machine-readable source of truth is `internal/nativebridge/testdata/d3-g0-isolation-contract.json`.

Required negative cases:

1. A cannot observe B or C through A authority.
2. A cannot message/continue B or C through A authority.
3. A cannot abort B or C through A authority.
4. B/C cannot attach, observe, message, continue, or abort A descendants by presenting a child Native SessionID as a top-level token.
5. A child is never promoted to a root mapping.
6. Caller-provided workspace substitution across roots is denied.
7. Caller-provided RuntimeScope substitution across roots is denied.
8. Denial must occur at AgentDock authority before a foreign request reaches the native control plane.

The existing executable G0 bridge regression starts A/B/C concurrently and verifies distinct exact roots, targeted A correction, targeted B abort, C unaffected, cross-root denial, foreign-child denial, child non-promotion, restart exact-root preservation, single-root LOST isolation, and per-root cursor independence.

## Restart and LOST semantics

Restart must recover all three durable root mappings exactly. No replacement root is allowed merely because AgentDock restarted.

If A's exact native root disappears:

- A becomes `LOST`.
- A is not silently replaced.
- an A child is not promoted into A's root slot.
- B and C keep their exact root mappings and remain usable.
- B/C event cursors and terminal/result state are not rewritten by A's loss.

The same rule is symmetric for any one root.

## Per-subagent cursor

Every top-level root has an independent durable native-event cursor. Consuming A events advances only A's cursor. B and C cursors must not advance as a side effect.

A root owns telemetry from its descendant closure, but descendants do not get independent AgentDock top-level cursors.

## GLOBAL_SANDBOX_V1 isolation contract

G0 freezes the sandbox contract but does not implement the sandbox runtime. D3-S1 is required before G1 can claim these behaviors in production.

Per root, sandbox capability is an independent authority object. The required policy is:

- current workspace: read/write allowed; delete controlled;
- external non-protected paths: read allowed, write/delete denied by default;
- explicitly granted external writable mount: read/write allowed only inside the bounded grant; delete follows that grant;
- protected/secret/system paths: read denied or category-limited; write/delete denied;
- agent-owned temp/build/cache/scratch delete: allowed;
- normal workspace delete: approval/bounded grant required;
- recursive/bulk delete: high-risk approval required;
- workspace root delete: denied;
- external read-only root delete: denied;
- `AUTO_APPROVE_OVERRIDES_DENY = NO`;
- destructive shell commands must be path-aware, including at least `rm`, `del`, `erase`, `rd`, `rmdir`, `Remove-Item`, `Clear-Content`, `truncate`, `git clean`, `git reset --hard`, `git checkout -- .`, `git restore .`, and `git rm`;
- Windows Job Object is process-tree containment, not a filesystem sandbox, and must not be represented as filesystem virtualization.

Isolation rules across roots:

1. A being able to read an external path never grants B write access to that path.
2. A's explicit external writable mount is not inherited by B or C.
3. A child inherits A's sandbox capability snapshot and cannot self-elevate.
4. A delete approval/grant is scoped to A and is not reusable by B or C.
5. Sibling roots do not merge sandbox allowlists, writable mounts, delete approvals, or deny exceptions.
6. A deny remains a deny even if an auto-approve setting exists elsewhere.

## G1_PLAN

G1 is permitted only after Central supplies accepted exact inputs for all four dependencies: `D3-C`, `D3-D`, `D3-F1`, and `D3-S1`.

The G1 sequence is frozen as follows:

1. Import only the exact accepted dependency SHAs; never merge moving branch tips.
2. Re-run the G0 contract gate unchanged to prove no dependency weakened root ownership or negative-control rules.
3. Bind one production top-level pool entry to one AgentDock Session and one exact OpenCode root Native SessionID; do not promote child sessions.
4. Add supervisor dispatch so correction targets one root by durable top-level SubagentID and cancel targets one root execution/descendant closure.
5. Carry root-scoped status/events/result and a durable independent event cursor through restart recovery.
6. Integrate D3-S1's real sandbox capability representation as a per-root immutable/bounded authority snapshot; child sessions inherit only their root snapshot.
7. Enforce external writable mount isolation and root-scoped delete approvals before any file or shell mutation reaches the runtime.
8. Add production A/B/C acceptance covering parallel execution, A-only correction, B-only cancel, C unaffected, cross-root denial, foreign child denial, restart exact roots, one-root LOST isolation, cursor independence, sandbox mount isolation, child sandbox inheritance, and delete-approval isolation.
9. Run focused D3 regressions plus the full applicable repository regression suite.
10. Commit/push only after all gates pass and worktree is clean.

G0 does not define or authorize a production pool API surface beyond these invariants. Public shape and orchestration wiring must come from the accepted D3-C/D3-D/D3-F1/D3-S1 inputs.

## G0 acceptance

G0 is PASS only when:

- ownership matrix is frozen;
- negative-test matrix is machine-readable and validated;
- A/B/C independent roots are executable against the existing bridge baseline;
- targeted A correction and B cancel are isolated;
- cross-root observe/message/abort is denied;
- foreign descendants are denied and children are not promoted;
- restart preserves exact roots;
- one LOST root does not corrupt siblings;
- per-subagent cursor is independent;
- sandbox-isolation matrix is frozen and explicitly marked as requiring D3-S1;
- no production pool implementation or OpenCode runtime source modification is introduced.
