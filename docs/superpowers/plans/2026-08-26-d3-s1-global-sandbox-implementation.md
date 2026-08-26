# D3-S1 GLOBAL_SANDBOX_V1 Implementation Plan

**Goal:** Implement the frozen D3-S0 `GLOBAL_SANDBOX_V1` policy without crippling autonomous external read-only investigation and without modifying the frozen OpenCode runtime source.

**Dependency gate:** S1 starts only after Central supplies exact `D3_D_FINAL_SHA`, `D3_F0_FINAL_SHA`, and `D3_S0_FINAL_SHA`. Import exact SHAs only. Do not merge moving branch tips and do not recreate D3-D/D3-F0 interfaces from guesses.

**Frozen runtime:** `D3_OPENCODE_RUNTIME_SHA = d5664f38002d8326eca179cf2efb5774c8ce1247`; source modification is not authorized.

**Contract:** `docs/superpowers/specs/2026-08-26-d3-global-sandbox-v1-contract.md`

**Machine-readable freeze:**

- `internal/app/d3contract/global-sandbox-v1.contract.json`
- `internal/app/d3contract/global-sandbox-v1.test-vectors.json`
- `internal/app/global_sandbox_contract_test.go`

## Global constraints

- Keep current workspace normal coding fully usable.
- Keep ordinary external non-protected read/grep/log inspection usable by root and native descendants.
- External write/delete remains denied unless an exact bounded grant says otherwise.
- Protected roots and workspace-root deletion are hard denies.
- Do not protect the whole `USERPROFILE` or whole normal source/log trees.
- Do not equate Windows Job Object containment with filesystem sandboxing.
- Do not solve shell safety by disabling all shell commands.
- Child/descendant capability is never broader than the root capability ceiling.
- Existing six Agent schemas, Session/Job/Agent semantics, D3-B native mapping, and bounded `provider="opencode"` compatibility must remain intact.

---

### Task 0: Exact-SHA integration preflight

**Inputs:** Central-provided D3-D, D3-F0, D3-S0 final SHAs.

- [ ] Create a fresh S1 integration worktree from the Central-designated exact base.
- [ ] Import the three exact SHAs only.
- [ ] Verify `git rev-parse` identities and clean state before implementation.
- [ ] Inspect D3-D/D3-F0 owned interfaces and record the real native/tool interception points.
- [ ] Confirm no OpenCode runtime source checkout is modified.
- [ ] Run frozen D3-S0 contract tests before changing production code.

**Blocker rule:** if the exact D3-D/D3-F0 contracts provide no enforceable root/descendant tool boundary without editing the frozen OpenCode runtime source, stop and report a Central blocker. Do not weaken `GLOBAL_SANDBOX_V1` and do not silently switch native agents to unrestricted `full` access.

### Task 1: Introduce the policy core without changing public schemas

**Likely ownership:** new `internal/sandbox` package plus narrow adapters in existing AgentDock packages.

- [ ] Add immutable/versioned policy types for `GLOBAL_SANDBOX_V1`.
- [ ] Represent decisions `ALLOW`, `ASK`, `HIGH_RISK_ASK`, `DENY`.
- [ ] Represent `read_roots`, `write_roots`, `delete_roots`, `protected_roots`.
- [ ] Represent bounded external grants with grant ID, canonical root, operations, delete class, issuer, owner root sandbox/session, created time, expiry, revocation.
- [ ] Represent bounded approvals with canonical targets, operation, scope, expiry, and risk class.
- [ ] Keep policy internal; do not change the six frozen Agent public schemas.
- [ ] Add unit tests for precedence and “hard deny cannot be widened by later grants.”

### Task 2: Canonical path resolver and Windows escape hardening

- [ ] Canonicalize before every policy decision.
- [ ] Collapse `.` and `..`.
- [ ] Use case-insensitive equality/containment on Windows.
- [ ] Resolve symlink components.
- [ ] Resolve Windows junction/reparse components.
- [ ] For missing write targets, resolve the deepest existing ancestor and append only a normalized relative tail.
- [ ] Normalize supported UNC/extended-UNC forms and classify them normally.
- [ ] Reject ambiguous drive-relative forms such as `C:foo`.
- [ ] Reject or normalize device namespace aliases before authorization.
- [ ] Re-check protected/ownership/root classification after resolution.
- [ ] Add tests proving a lexical workspace path cannot write through a symlink/junction into an external/protected target.
- [ ] Add tests proving legal external UNC/local read-only targets are not blanket rejected.

### Task 3: Root classification, protected categories, and ACTIVE Session ownership

- [ ] Derive current workspace root from authoritative AgentDock Session metadata.
- [ ] Treat inactive neighboring projects as ordinary external non-protected paths: read allowed, mutation denied by default.
- [ ] Build ACTIVE AgentDock Session root inventory from authoritative Session state rather than path-name heuristics.
- [ ] Deny foreign ACTIVE Session read/write/delete by default.
- [ ] Support exact explicit read-only grants for foreign ACTIVE Session roots; never convert them into write grants.
- [ ] Implement protected category matchers for credential/token/password stores, SSH private keys, AgentDock/OpenCode auth secrets, browser secrets, system-sensitive security stores, and explicit secret files.
- [ ] Add negative tests showing normal `USERPROFILE`, Desktop/Documents source, ordinary logs, and normal `C:\Windows` files are not automatically all protected.

### Task 4: External writable mount lifecycle

- [ ] Add trusted-root-authority create/grant path using exact canonical root.
- [ ] Enforce “inside granted root only”; siblings stay read-only.
- [ ] Enforce protected-root precedence over grant.
- [ ] Prevent writable mount grants from widening another ACTIVE Session.
- [ ] Implement revocation and expiry checks on every new mutation decision.
- [ ] Make delete permission independently bounded (`DENY`/`ASK`/`HIGH_RISK_ASK`/allowed disposable scope as defined by the grant), never implied by write.
- [ ] Persist only if D3-D/D3-F0 exact contracts require durability; otherwise keep the narrowest authoritative store consistent with their ownership model.
- [ ] Test grant, sibling denial, revoke, expiry, restart semantics required by the exact dependency contracts.

### Task 5: Delete and approval engine

- [ ] Mark agent-owned disposable roots by authoritative ownership/registration, not folder names.
- [ ] Allow delete inside designated disposable roots.
- [ ] Return `ASK` for normal bounded workspace file/non-recursive directory deletion.
- [ ] Return `HIGH_RISK_ASK` for recursive/bulk deletion.
- [ ] Return hard `DENY` for workspace-root deletion and external read-only deletion.
- [ ] Validate bounded approval fields: canonical targets, operation, scope, expiry.
- [ ] Permit configured auto-approve only for normal `ASK`.
- [ ] Require explicit high-risk approval for `HIGH_RISK_ASK` in V1.
- [ ] Prove approval/auto-approve cannot override `DENY`.

### Task 6: Path-aware destructive shell/Git analysis

**Required command families:** `rm`, `del`, `erase`, `rd`, `rmdir`, `Remove-Item`, `Clear-Content`, `truncate`, `git clean`, `git reset --hard`, `git checkout -- .`, `git restore`, `git rm`.

- [ ] Parse destructive intent and derive canonical target set before execution.
- [ ] Feed derived targets to the same policy engine used by file tools.
- [ ] Normal single-file workspace delete → `ASK`.
- [ ] Recursive/bulk and whole-worktree destructive Git operations → `HIGH_RISK_ASK`.
- [ ] Workspace-root/external-RO/protected destructive targets → `DENY`.
- [ ] If destructive targets cannot be resolved to a bounded canonical set, deny until resolvable.
- [ ] Preserve ordinary shell execution that is not destructive.
- [ ] Add parser tests for PowerShell quoting, Windows path casing, wildcard/bulk forms, and Git pathspecs relevant to the frozen matrix.

### Task 7: Enforce model-free OpenCode, Job, and Agent tool entry points

**Existing candidate seams:** app OpenCode tool routing, Job start routing, Agent tool bridge, Session-resolved workspace binding. Exact edit set must be chosen after Task 0 inspection.

- [ ] Evaluate sandbox before `read/glob/grep/edit/write/apply_patch/bash/git` reaches the runtime where the operation can affect the host.
- [ ] Preserve Session-resolved canonical workspace and runtime scope; caller arguments cannot widen them.
- [ ] Ensure read-only external investigation uses a controlled read path/broker from the exact D3-D/D3-F0 surface rather than granting unrestricted mutation capability.
- [ ] Ensure Job persistence/cancel semantics remain unchanged apart from carrying the authoritative sandbox context needed for enforcement.
- [ ] Ensure legacy `provider="opencode"` bounded worker remains compatible.
- [ ] Add focused tests for direct tool calls, Job-owned calls, and Agent-owned calls.

### Task 8: Native root/child sandbox inheritance

**Dependency-sensitive:** implement only against the exact D3-D/D3-F0 native control surface discovered in Task 0.

- [ ] Bind one authoritative root sandbox identity/capability ceiling to the D3 root Native Session mapping.
- [ ] Ensure every OpenCode child/descendant inherits that root sandbox automatically.
- [ ] Prevent child-created prompts/tool arguments from adding write roots, delete roots, or writable mounts.
- [ ] Permit a child to narrow requested capability without mutating the root ceiling.
- [ ] Make trusted root-authority bounded grant updates visible to descendants through the authoritative policy state.
- [ ] Prove descendants use the same enforcement boundary for file and shell/Git tools.
- [ ] Preserve D3-B descendant ownership closure: child sessions stay descendants and never become top-level AgentDock subagents.

### Task 9: Turn the S0 matrix into executable evaluator/integration tests

- [ ] Load `global-sandbox-v1.test-vectors.json` from tests and execute every filesystem/path/shell/approval vector against the production evaluator.
- [ ] Workspace normal coding PASS.
- [ ] External read PASS: direct read, grep/search, log read.
- [ ] External write/edit/delete DENIED.
- [ ] Writable mount inside PASS; sibling/revoked/expired DENIED.
- [ ] Temp delete ALLOW; normal delete ASK; bounded approval PASS; bulk HIGH_RISK_ASK; root DENY.
- [ ] Protected secret read/write/delete DENY.
- [ ] Auto-approve does not override hard deny.
- [ ] `..`, case folding, symlink, junction/reparse, UNC, and drive-relative hardening PASS.
- [ ] Other ACTIVE Session default deny and exact RO grant PASS.
- [ ] Child inheritance, self-elevation denial, and child external read research PASS.

### Task 10: Live acceptance and regression

- [ ] Run focused sandbox packages and app/session/job/agent/nativebridge tests.
- [ ] Run Windows-specific path hardening tests on a host capable of creating junctions/reparse points; skip only with explicit evidence when the host lacks privilege/capability.
- [ ] Run a real D3 native root + descendant external read-only research scenario.
- [ ] Prove the descendant can inspect an ordinary external source/log root but cannot mutate it.
- [ ] Prove another ACTIVE AgentDock Session remains inaccessible by default.
- [ ] Prove protected secret fixture is unreadable even when normal auto-approve is enabled.
- [ ] Prove workspace-root deletion remains hard denied.
- [ ] Run frozen D3-A/D3-B regression gates and the existing Session/Job/Agent/OpenCode focused suites.
- [ ] Run `go test ./... -count=1` if the branch's established full-regression policy permits it; otherwise run the exact Central-prescribed full gate.
- [ ] Verify no OpenCode runtime source change, no frozen Agent schema drift, and clean worktree.
- [ ] Commit/push once all gates pass and report exact S1 SHA to Central.

## S1 acceptance summary

S1 passes only if all of the following are simultaneously true:

- workspace coding is normal;
- ordinary external non-protected research remains read-only and usable;
- external mutation defaults to deny;
- exact writable mounts work and revoke/expire correctly;
- protected categories are hard denied without blanket home-directory blocking;
- deletion is controlled at the required risk levels;
- destructive shell/Git commands are path aware;
- auto-approve cannot bypass hard deny;
- child/descendant sessions inherit the root sandbox and cannot self-elevate;
- symlink/junction/reparse/UNC/case/`..` hardening prevents escape;
- other ACTIVE Sessions are isolated by default;
- frozen OpenCode runtime source and prior D3 contracts remain unchanged.