# D3-S0 GLOBAL_SANDBOX_V1 Contract and Test Design

## Goal

Freeze a path-aware filesystem safety contract that keeps autonomous coding useful while preventing unbounded writes, deletes, and secret access. The key usability rule is intentional: ordinary external non-protected directories remain readable by default so an Agent can inspect source, logs, documentation, and neighboring inactive projects without first receiving a write grant.

This is a contract/test-design line only. It does not claim that the production runtime already enforces this policy.

## Frozen inputs

- `BASE_SHA = 4cf08c3f76a945b7284e8f19befdda9fb3eb9ab6`
- `D3_OPENCODE_RUNTIME_SHA = d5664f38002d8326eca179cf2efb5774c8ce1247`
- OpenCode runtime source change: `NOT REQUIRED / NOT AUTHORIZED`
- D3-A native autonomous execution: frozen PASS
- D3-B one AgentDock Session ↔ one OpenCode root Native Session mapping: frozen PASS
- Existing bounded `provider="opencode"` worker and six frozen Agent schemas remain compatible.

## Non-goals

S0 does not:

- implement a complete production filesystem sandbox;
- claim Windows Job Objects provide filesystem isolation;
- add OpenCode runtime source changes;
- rewrite Session, Job, Agent Manager, or D3-B native-session ownership;
- disable external investigation by making the entire host or `USERPROFILE` unreadable;
- solve policy by filtering every shell command string.

## Decision vocabulary

The policy engine has four decisions:

- `ALLOW`: execute immediately within the evaluated capability.
- `ASK`: mutation is allowed only with a bounded approval. A configured auto-approve policy may satisfy this class.
- `HIGH_RISK_ASK`: explicit high-risk bounded approval is required. It is not silently promoted by normal auto-approve.
- `DENY`: hard policy denial. Approval and auto-approve cannot override it.

Every approval is scoped to canonical targets, operation, grant scope, and expiry. An approval for one file/root/operation cannot be reused for a sibling, another operation, or after expiry.

## Capability model

Every root sandbox exposes the capability fields:

- `read_roots`
- `write_roots`
- `delete_roots`
- `protected_roots`

These are evaluated together with frozen defaults. Root lists are not permission-by-name shortcuts: every path is canonicalized first and all higher-precedence ownership/protected rules still apply.

### Workspace

- read: `ALLOW`
- write: `ALLOW`
- delete: `CONTROLLED`

Normal coding must remain usable. Read, grep, edit, write, apply-patch, compiler output, and ordinary non-destructive shell actions inside the workspace must not be blocked merely because the sandbox exists.

### External non-protected paths

- read: `ALLOW`
- write: `DENY_BY_DEFAULT`
- delete: `DENY_BY_DEFAULT`

External read includes ordinary file reads, grep/search, and log inspection. This is a deliberate requirement to keep subagent investigation useful.

### Other projects

An inactive project that is merely outside the current workspace follows the external non-protected rule: read-only access is allowed by default.

A path owned by another ACTIVE AgentDock Session is stronger than the generic external rule:

- default read: `DENY`
- default write: `DENY`
- default delete: `DENY`
- trusted root authority may grant explicit read-only access to a bounded canonical root;
- that read-only grant never implies write/delete access.

This prevents cross-session interference without making ordinary neighboring source trees unreadable.

## Explicit external writable mounts

A trusted root authority may grant one bounded external canonical root as writable.

Frozen requirements:

- inside the exact granted root: read/write allowed;
- sibling roots: still denied for write;
- delete behavior: separately declared by the bounded grant;
- grants are revocable and expirable;
- expired/revoked grants immediately stop authorizing new mutations;
- protected-root hard denial has higher precedence than a writable mount;
- a writable mount cannot convert another ACTIVE AgentDock Session into a writable target.

S1 must represent grant identity, canonical root, allowed operations, delete class, issuer/authority, creation time, expiry, revocation state, and owning root sandbox. It must not infer write permission from a broad parent directory or string prefix.

## Protected paths

Protected paths are category based, not a blanket home-directory ban.

Required categories:

- credential/token/password stores;
- SSH private keys;
- OpenCode/AgentDock authentication secrets;
- browser secrets;
- system-sensitive credential/security stores;
- explicitly registered secret files.

Protected decision is hard `DENY` for read/write/delete unless a future separately versioned contract explicitly defines a narrower exception. `GLOBAL_SANDBOX_V1` does not.

Examples of what must NOT become blanket protected roots:

- the entire `USERPROFILE`;
- the entire user Desktop/Documents tree;
- the entire `C:\Windows` tree;
- normal source/log directories merely because they live outside the workspace.

Protected matching must be based on canonical path/category resolution and may use explicit secret-file registration. S1 must test category precision to prevent accidental overblocking.

## Precedence

Evaluation order is frozen conceptually as:

1. invalid/unresolvable path → `DENY`;
2. protected category/root → hard `DENY`;
3. other ACTIVE Session ownership guard → deny unless exact explicit read-only grant applies;
4. workspace-root deletion → hard `DENY`;
5. exact bounded external grant, if valid and not revoked/expired;
6. designated agent-owned disposable delete rule;
7. workspace defaults;
8. external non-protected defaults.

A lower-precedence rule cannot broaden a higher-precedence denial.

## Delete policy

Deletion is not equivalent to ordinary workspace write.

- agent-owned disposable temp/build/cache/scratch root: `ALLOW`;
- normal workspace file: `ASK`;
- normal workspace directory, non-recursive: `ASK`;
- recursive/bulk delete: `HIGH_RISK_ASK`;
- workspace root: `DENY`;
- external read-only path: `DENY`;
- external writable mount: according to the exact bounded delete grant.

`agent-owned disposable` must be proven by ownership/registration, not merely by directory names such as `temp`, `build`, `cache`, or `scratch`. This avoids allowing deletion of a user directory just because it has a convenient name.

A bounded approval may turn `ASK`/`HIGH_RISK_ASK` into an execution authorization for the exact evaluated targets. It cannot turn a `DENY` into allow.

## Canonical path and escape hardening

Authorization is performed on canonical paths, never on raw caller text.

Required behavior:

- collapse `.` / `..` before containment checks;
- Windows containment and equality are case-insensitive;
- resolve symlink components;
- resolve Windows junction/reparse components;
- after resolution, re-run protected/ownership/root classification before write/delete;
- for a missing write target, resolve the deepest existing ancestor and then append only a normalized relative tail before final classification;
- support and classify UNC paths rather than blindly rejecting all network paths;
- normalize extended UNC/device spellings before policy or reject unsupported aliases before execution;
- reject drive-relative forms such as `C:foo` as ambiguous;
- filesystem roots cannot be inferred as a normal workspace;
- legal external read-only paths must remain usable after hardening.

The write-escape invariant is: a path that looks lexically inside an allowed write root but resolves through a symlink/junction/reparse point to a non-writable or protected root must be denied.

## Shell destructive policy

Shell remains available for ordinary coding. `GLOBAL_SANDBOX_V1` does not use a generic command-content ban.

The shell adapter must detect destructive operations, resolve their target set, and feed each target to the same path policy. Required coverage includes at least:

- `rm`
- `del` / `erase`
- `rd` / `rmdir`
- PowerShell `Remove-Item`
- `Clear-Content`
- `truncate`
- `git clean`
- `git reset --hard`
- `git checkout -- .`
- `git restore` (including `git restore .`)
- `git rm`

Frozen decision examples:

- one normal workspace file delete → `ASK`;
- recursive/bulk workspace deletion → `HIGH_RISK_ASK`;
- `git clean -fd` → `HIGH_RISK_ASK`;
- `git reset --hard` → `HIGH_RISK_ASK`;
- `git restore .` / `git checkout -- .` → `HIGH_RISK_ASK`;
- workspace-root removal → `DENY`;
- external read-only destructive target → `DENY`;
- protected target → `DENY`.

If a destructive target cannot be resolved to a bounded canonical target set, the command is denied until it can be resolved. High-risk approval is not a substitute for an unknown target boundary.

## Auto-approve semantics

`AUTO_APPROVE_OVERRIDES_DENY = NO`.

Normal `ASK` may be satisfied by a configured bounded auto-approve policy. `HIGH_RISK_ASK` requires explicit high-risk approval in V1. Hard `DENY` always remains deny.

This must be checked at the policy decision boundary, not only in UI text, so a child session, shell path, or alternate tool cannot bypass the rule.

## Native child inheritance

Every OpenCode native child/descendant created under one D3 root inherits the root sandbox and capability ceiling.

Frozen rules:

- child inherits root sandbox: `YES`;
- child may narrow its own capability request: `YES`;
- child may self-elevate: `NO`;
- child may add an external writable mount: `NO`;
- trusted root authority may update bounded grants, and descendants observe the authoritative effective policy;
- descendant tool calls must pass through the same effective enforcement boundary as the root.

A child must not escape merely because OpenCode internally represents it as a descendant session rather than a top-level AgentDock subagent.

## Enforcement architecture constraint for S1

S1 must enforce policy in AgentDock-owned boundaries and in the exact D3-D/D3-F0 integration surface supplied by Central. It must not modify the frozen OpenCode runtime source SHA and must not claim that Windows Job Objects provide this isolation.

For native autonomous tool use, S1 must prove that the root and descendant effective tool boundary cannot bypass AgentDock's path policy. If the exact D3-D/D3-F0 contracts do not provide a safe interception/configuration point without changing OpenCode runtime source, S1 must stop and return a blocker to Central rather than weakening the contract or silently using unrestricted `full` mode.

External read-only research must remain genuinely usable; a design that enforces safety by making every external path inaccessible fails S1 acceptance.

## Frozen test matrix

The machine-readable vectors live at:

`internal/app/d3contract/global-sandbox-v1.test-vectors.json`

The matrix covers:

- workspace read/write normal coding;
- external read/grep/log read success;
- external write/edit/delete denial;
- inactive other-project read-only success;
- other ACTIVE Session default denial and explicit RO grant;
- exact external writable mount, sibling denial, revoke, expiry;
- designated disposable delete allow;
- normal delete ask + bounded approval;
- bulk delete high-risk ask;
- workspace-root hard denial;
- protected read/write/delete denial;
- auto-approve cannot override hard denial;
- `..`, Windows case folding, symlink/junction escape, UNC, drive-relative path handling;
- destructive shell and Git operations;
- child inheritance, child self-elevation denial, and child external read research.

`internal/app/global_sandbox_contract_test.go` verifies that the fixture cannot silently drift away from these frozen semantics. S0 tests contract completeness only; S1 turns the vectors into production evaluator/integration tests.

## S1 dependency gate

D3-S1 may begin only after Central supplies exact final SHAs for:

- D3-D
- D3-F0
- D3-S0

No moving branch tips. S1 must import/merge exact SHAs and preserve the frozen OpenCode runtime source SHA.