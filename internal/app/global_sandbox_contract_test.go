package app

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

type d3SandboxContract struct {
	ContractVersion string   `json:"contract_version"`
	BaseSHA         string   `json:"base_sha"`
	Phase           string   `json:"phase"`
	Status          string   `json:"status"`
	Decisions       []string `json:"decisions"`
	CapabilityModel struct {
		RequiredRootFields []string `json:"required_root_fields"`
		Workspace          struct {
			Read   string `json:"read"`
			Write  string `json:"write"`
			Delete string `json:"delete"`
		} `json:"workspace"`
		ExternalNonProtected struct {
			Read   string `json:"read"`
			Write  string `json:"write"`
			Delete string `json:"delete"`
		} `json:"external_non_protected"`
		OtherProject struct {
			Read   string `json:"read"`
			Write  string `json:"write"`
			Delete string `json:"delete"`
		} `json:"other_project"`
		OtherActiveAgentDockSession struct {
			Read                  string `json:"read"`
			ExplicitReadOnlyGrant string `json:"explicit_read_only_grant"`
			Write                 string `json:"write"`
			Delete                string `json:"delete"`
		} `json:"other_active_agentdock_session"`
		ExternalWritableMount struct {
			Grantable                       bool   `json:"grantable"`
			Read                            string `json:"read"`
			Write                           string `json:"write"`
			Delete                          string `json:"delete"`
			Scope                           string `json:"scope"`
			SiblingInheritance              bool   `json:"sibling_inheritance"`
			Revocable                       bool   `json:"revocable"`
			Expirable                       bool   `json:"expirable"`
			ProtectedRootsOverrideGrant     bool   `json:"protected_roots_override_grant"`
			OtherActiveSessionWriteOverride bool   `json:"other_active_session_write_override"`
		} `json:"external_writable_mount"`
		Protected struct {
			Read                        string   `json:"read"`
			Write                       string   `json:"write"`
			Delete                      string   `json:"delete"`
			WholeUserProfileIsProtected bool     `json:"whole_userprofile_is_protected"`
			Categories                  []string `json:"categories"`
		} `json:"protected"`
	} `json:"capability_model"`
	DeletePolicy struct {
		AgentOwnedDisposable                 string   `json:"agent_owned_disposable"`
		NormalWorkspaceFile                  string   `json:"normal_workspace_file"`
		NormalWorkspaceDirectoryNonRecursive string   `json:"normal_workspace_directory_non_recursive"`
		RecursiveOrBulk                      string   `json:"recursive_or_bulk"`
		WorkspaceRoot                        string   `json:"workspace_root"`
		ExternalReadOnly                     string   `json:"external_read_only"`
		ExternalWritableMount                string   `json:"external_writable_mount"`
		DisposableClassificationIsNameOnly   bool     `json:"disposable_classification_is_name_only"`
		BoundedApprovalRequiredFields        []string `json:"bounded_approval_required_fields"`
	} `json:"delete_policy"`
	ApprovalPolicy struct {
		AskMayAutoApprove              bool `json:"ask_may_auto_approve"`
		HighRiskAskMayAutoApprove      bool `json:"high_risk_ask_may_auto_approve"`
		HardDenyOverridable            bool `json:"hard_deny_overridable"`
		AutoApproveOverridesDeny       bool `json:"auto_approve_overrides_deny"`
		ApprovalIsBounded              bool `json:"approval_is_bounded"`
		ApprovalReuseOutsideExactScope bool `json:"approval_reuse_outside_exact_scope"`
	} `json:"approval_policy"`
	CanonicalPathPolicy struct {
		CanonicalizeBeforePolicy                               bool `json:"canonicalize_before_policy"`
		WindowsCaseInsensitive                                 bool `json:"windows_case_insensitive"`
		DotDotCollapsedBeforeClassification                    bool `json:"dotdot_collapsed_before_classification"`
		SymlinkComponentsResolved                              bool `json:"symlink_components_resolved"`
		JunctionReparseComponentsResolved                      bool `json:"junction_reparse_components_resolved"`
		WriteDeleteRecheckedAfterResolution                    bool `json:"write_delete_rechecked_after_resolution"`
		MissingWriteTargetUsesDeepestExistingAncestor          bool `json:"missing_write_target_uses_deepest_existing_ancestor"`
		UNCSupportedAndClassified                              bool `json:"unc_supported_and_classified"`
		ExtendedUNCNormalized                                  bool `json:"extended_unc_normalized"`
		DriveRelativePathsRejected                             bool `json:"drive_relative_paths_rejected"`
		DeviceNamespaceAliasesRejectedOrNormalizedBeforePolicy bool `json:"device_namespace_aliases_rejected_or_normalized_before_policy"`
		FilesystemRootNeverImplicitWorkspace                   bool `json:"filesystem_root_never_implicit_workspace"`
		LegalExternalReadOnlyMustRemainUsable                  bool `json:"legal_external_read_only_must_remain_usable"`
	} `json:"canonical_path_policy"`
	ShellDestructivePolicy struct {
		PathAware                      bool     `json:"path_aware"`
		ContentFilterAllShell          bool     `json:"content_filter_all_shell"`
		Commands                       []string `json:"commands"`
		UnresolvedDestructiveTarget    string   `json:"unresolved_destructive_target"`
		NormalWorkspaceSingleTarget    string   `json:"normal_workspace_single_target"`
		RecursiveOrBulkWorkspaceTarget string   `json:"recursive_or_bulk_workspace_target"`
		WorkspaceRootTarget            string   `json:"workspace_root_target"`
		ExternalReadOnlyTarget         string   `json:"external_read_only_target"`
		ProtectedTarget                string   `json:"protected_target"`
	} `json:"shell_destructive_policy"`
	ChildPolicy struct {
		InheritsRootSandbox                           bool   `json:"inherits_root_sandbox"`
		InheritsCapabilityCeiling                     bool   `json:"inherits_capability_ceiling"`
		ChildSelfElevation                            string `json:"child_self_elevation"`
		ChildMayNarrowCapabilities                    bool   `json:"child_may_narrow_capabilities"`
		ChildMayAddWriteMount                         bool   `json:"child_may_add_write_mount"`
		RootAuthorityMayUpdateBoundedGrants           bool   `json:"root_authority_may_update_bounded_grants"`
		DescendantToolCallsUseSameEnforcementBoundary bool   `json:"descendant_tool_calls_use_same_enforcement_boundary"`
	} `json:"child_policy"`
	Truthfulness struct {
		WindowsJobObjectIsFilesystemSandbox                              bool `json:"windows_job_object_is_filesystem_sandbox"`
		OSLevelFilesystemVirtualizationClaimAllowedWithoutImplementation bool `json:"os_level_filesystem_virtualization_claim_allowed_without_implementation"`
		OpenCodeRuntimeSourceChangeAuthorized                            bool `json:"opencode_runtime_source_change_authorized"`
	} `json:"truthfulness"`
}

type d3SandboxVector struct {
	ID                           string `json:"id"`
	Kind                         string `json:"kind"`
	Operation                    string `json:"operation"`
	PathClass                    string `json:"path_class"`
	AutoApprove                  bool   `json:"auto_approve,omitempty"`
	ExpectedInitial              string `json:"expected_initial"`
	ExpectedAfterBoundedApproval string `json:"expected_after_bounded_approval,omitempty"`
}

func readD3SandboxContract(t *testing.T) d3SandboxContract {
	t.Helper()
	body, err := os.ReadFile("d3contract/global-sandbox-v1.contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract d3SandboxContract
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func TestD3GlobalSandboxV1FrozenContract(t *testing.T) {
	contract := readD3SandboxContract(t)
	if contract.ContractVersion != "GLOBAL_SANDBOX_V1" || contract.Phase != "D3-S0" {
		t.Fatalf("identity = %q/%q", contract.ContractVersion, contract.Phase)
	}
	if contract.BaseSHA != "4cf08c3f76a945b7284e8f19befdda9fb3eb9ab6" {
		t.Fatalf("base SHA = %q", contract.BaseSHA)
	}
	if contract.Status != "CONTRACT_ONLY_NOT_PRODUCTION_ENFORCEMENT" {
		t.Fatalf("status = %q", contract.Status)
	}
	for _, decision := range []string{"ALLOW", "ASK", "HIGH_RISK_ASK", "DENY"} {
		if !slices.Contains(contract.Decisions, decision) {
			t.Fatalf("missing decision %q", decision)
		}
	}
	for _, field := range []string{"read_roots", "write_roots", "delete_roots", "protected_roots"} {
		if !slices.Contains(contract.CapabilityModel.RequiredRootFields, field) {
			t.Fatalf("missing capability root field %q", field)
		}
	}

	workspace := contract.CapabilityModel.Workspace
	if workspace.Read != "ALLOW" || workspace.Write != "ALLOW" || workspace.Delete != "CONTROLLED" {
		t.Fatalf("workspace policy = %#v", workspace)
	}
	external := contract.CapabilityModel.ExternalNonProtected
	if external.Read != "ALLOW" || external.Write != "DENY_BY_DEFAULT" || external.Delete != "DENY_BY_DEFAULT" {
		t.Fatalf("external non-protected policy = %#v", external)
	}
	other := contract.CapabilityModel.OtherProject
	if other.Read != "ALLOW" || other.Write != "DENY_BY_DEFAULT" || other.Delete != "DENY_BY_DEFAULT" {
		t.Fatalf("other project policy = %#v", other)
	}
	active := contract.CapabilityModel.OtherActiveAgentDockSession
	if active.Read != "DENY_BY_DEFAULT" || active.ExplicitReadOnlyGrant != "ALLOW_READ_ONLY" || active.Write != "DENY" || active.Delete != "DENY" {
		t.Fatalf("other active session policy = %#v", active)
	}

	mount := contract.CapabilityModel.ExternalWritableMount
	if !mount.Grantable || mount.Read != "ALLOW" || mount.Write != "ALLOW" || mount.Delete != "ACCORDING_TO_BOUNDED_GRANT" || mount.Scope != "CANONICAL_ROOT_ONLY" || mount.SiblingInheritance || !mount.Revocable || !mount.Expirable || !mount.ProtectedRootsOverrideGrant || mount.OtherActiveSessionWriteOverride {
		t.Fatalf("external writable mount = %#v", mount)
	}
	protected := contract.CapabilityModel.Protected
	if protected.Read != "DENY" || protected.Write != "DENY" || protected.Delete != "DENY" || protected.WholeUserProfileIsProtected {
		t.Fatalf("protected policy = %#v", protected)
	}
	for _, category := range []string{"credential_token_password_stores", "ssh_private_keys", "opencode_agentdock_auth_secrets", "browser_secrets", "system_sensitive_paths", "explicit_secret_files"} {
		if !slices.Contains(protected.Categories, category) {
			t.Fatalf("missing protected category %q", category)
		}
	}

	deletion := contract.DeletePolicy
	if deletion.AgentOwnedDisposable != "ALLOW" || deletion.NormalWorkspaceFile != "ASK" || deletion.NormalWorkspaceDirectoryNonRecursive != "ASK" || deletion.RecursiveOrBulk != "HIGH_RISK_ASK" || deletion.WorkspaceRoot != "DENY" || deletion.ExternalReadOnly != "DENY" || deletion.ExternalWritableMount != "ACCORDING_TO_BOUNDED_GRANT" || deletion.DisposableClassificationIsNameOnly {
		t.Fatalf("delete policy = %#v", deletion)
	}
	for _, field := range []string{"canonical_targets", "operation", "scope", "expires_at"} {
		if !slices.Contains(deletion.BoundedApprovalRequiredFields, field) {
			t.Fatalf("bounded approval missing %q", field)
		}
	}

	approval := contract.ApprovalPolicy
	if !approval.AskMayAutoApprove || approval.HighRiskAskMayAutoApprove || approval.HardDenyOverridable || approval.AutoApproveOverridesDeny || !approval.ApprovalIsBounded || approval.ApprovalReuseOutsideExactScope {
		t.Fatalf("approval policy = %#v", approval)
	}
	path := contract.CanonicalPathPolicy
	if !path.CanonicalizeBeforePolicy || !path.WindowsCaseInsensitive || !path.DotDotCollapsedBeforeClassification || !path.SymlinkComponentsResolved || !path.JunctionReparseComponentsResolved || !path.WriteDeleteRecheckedAfterResolution || !path.MissingWriteTargetUsesDeepestExistingAncestor || !path.UNCSupportedAndClassified || !path.ExtendedUNCNormalized || !path.DriveRelativePathsRejected || !path.DeviceNamespaceAliasesRejectedOrNormalizedBeforePolicy || !path.FilesystemRootNeverImplicitWorkspace || !path.LegalExternalReadOnlyMustRemainUsable {
		t.Fatalf("canonical path policy = %#v", path)
	}

	shell := contract.ShellDestructivePolicy
	if !shell.PathAware || shell.ContentFilterAllShell || shell.UnresolvedDestructiveTarget != "DENY_UNTIL_RESOLVED" || shell.NormalWorkspaceSingleTarget != "ASK" || shell.RecursiveOrBulkWorkspaceTarget != "HIGH_RISK_ASK" || shell.WorkspaceRootTarget != "DENY" || shell.ExternalReadOnlyTarget != "DENY" || shell.ProtectedTarget != "DENY" {
		t.Fatalf("shell destructive policy = %#v", shell)
	}
	for _, command := range []string{"rm", "del", "Remove-Item", "rmdir", "git clean", "git reset --hard", "git restore", "git rm", "Clear-Content", "truncate"} {
		if !slices.Contains(shell.Commands, command) {
			t.Fatalf("missing destructive command %q", command)
		}
	}

	child := contract.ChildPolicy
	if !child.InheritsRootSandbox || !child.InheritsCapabilityCeiling || child.ChildSelfElevation != "DENY" || !child.ChildMayNarrowCapabilities || child.ChildMayAddWriteMount || !child.RootAuthorityMayUpdateBoundedGrants || !child.DescendantToolCallsUseSameEnforcementBoundary {
		t.Fatalf("child policy = %#v", child)
	}
	truth := contract.Truthfulness
	if truth.WindowsJobObjectIsFilesystemSandbox || truth.OSLevelFilesystemVirtualizationClaimAllowedWithoutImplementation || truth.OpenCodeRuntimeSourceChangeAuthorized {
		t.Fatalf("truthfulness guard = %#v", truth)
	}
}

func TestD3GlobalSandboxV1TestMatrix(t *testing.T) {
	body, err := os.ReadFile("d3contract/global-sandbox-v1.test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []d3SandboxVector
	if err := json.Unmarshal(body, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 40 {
		t.Fatalf("test vector count = %d, want >= 40", len(vectors))
	}
	validDecision := map[string]bool{"ALLOW": true, "ASK": true, "HIGH_RISK_ASK": true, "DENY": true}
	byID := make(map[string]d3SandboxVector, len(vectors))
	for _, vector := range vectors {
		if vector.ID == "" || vector.Kind == "" || vector.Operation == "" || vector.PathClass == "" {
			t.Fatalf("incomplete vector = %#v", vector)
		}
		if !validDecision[vector.ExpectedInitial] {
			t.Fatalf("vector %q initial decision = %q", vector.ID, vector.ExpectedInitial)
		}
		if vector.ExpectedAfterBoundedApproval != "" && !validDecision[vector.ExpectedAfterBoundedApproval] {
			t.Fatalf("vector %q approval decision = %q", vector.ID, vector.ExpectedAfterBoundedApproval)
		}
		if _, duplicate := byID[vector.ID]; duplicate {
			t.Fatalf("duplicate vector id %q", vector.ID)
		}
		byID[vector.ID] = vector
	}

	required := map[string]struct {
		initial string
		after   string
	}{
		"workspace_read":                         {"ALLOW", ""},
		"workspace_write":                        {"ALLOW", ""},
		"external_read":                          {"ALLOW", ""},
		"external_grep":                          {"ALLOW", ""},
		"external_log_read":                      {"ALLOW", ""},
		"external_write":                         {"DENY", ""},
		"external_edit":                          {"DENY", ""},
		"external_delete":                        {"DENY", ""},
		"writable_mount_inside_write":            {"ALLOW", ""},
		"writable_mount_sibling_write":           {"DENY", ""},
		"writable_mount_revoked_write":           {"DENY", ""},
		"writable_mount_expired_write":           {"DENY", ""},
		"designated_temp_delete":                 {"ALLOW", ""},
		"normal_workspace_file_delete":           {"ASK", "ALLOW"},
		"workspace_bulk_delete":                  {"HIGH_RISK_ASK", "ALLOW"},
		"workspace_root_delete":                  {"DENY", "DENY"},
		"protected_secret_read":                  {"DENY", "DENY"},
		"protected_secret_write":                 {"DENY", "DENY"},
		"other_project_read":                     {"ALLOW", ""},
		"other_active_session_read":              {"DENY", ""},
		"other_active_session_explicit_ro_read":  {"ALLOW", ""},
		"other_active_session_explicit_ro_write": {"DENY", ""},
		"dotdot_escape_write":                    {"DENY", ""},
		"windows_casefold_protected":             {"DENY", ""},
		"symlink_write_escape":                   {"DENY", ""},
		"junction_write_escape":                  {"DENY", ""},
		"unc_external_read":                      {"ALLOW", ""},
		"unc_external_write":                     {"DENY", ""},
		"drive_relative_write":                   {"DENY", ""},
		"shell_git_reset_hard":                   {"HIGH_RISK_ASK", "ALLOW"},
		"shell_git_restore_all":                  {"HIGH_RISK_ASK", "ALLOW"},
		"shell_external_delete":                  {"DENY", "DENY"},
		"autoapprove_hard_deny":                  {"DENY", "DENY"},
		"child_inherits_root":                    {"ALLOW", ""},
		"child_self_elevate":                     {"DENY", ""},
		"child_external_research_read":           {"ALLOW", ""},
	}
	for id, want := range required {
		vector, ok := byID[id]
		if !ok {
			t.Fatalf("missing required vector %q", id)
		}
		if vector.ExpectedInitial != want.initial || vector.ExpectedAfterBoundedApproval != want.after {
			t.Fatalf("vector %q = %q/%q, want %q/%q", id, vector.ExpectedInitial, vector.ExpectedAfterBoundedApproval, want.initial, want.after)
		}
	}
}
