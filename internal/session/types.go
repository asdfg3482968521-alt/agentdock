package session

import (
	"context"
	"time"
)

const ContractVersion = "D1-v1"

type State string

const (
	StateOpening State = "OPENING"
	StateActive  State = "ACTIVE"
	StateClosing State = "CLOSING"
	StateClosed  State = "CLOSED"
	StateFailed  State = "FAILED"
)

type WorkspaceType string

const (
	WorkspaceGit    WorkspaceType = "git"
	WorkspaceNonGit WorkspaceType = "non_git"
)

type IsolationMode string

const (
	IsolationWorktree       IsolationMode = "worktree"
	IsolationExclusive      IsolationMode = "exclusive"
	IsolationSharedReadonly IsolationMode = "shared-readonly"
	IsolationSharedExplicit IsolationMode = "shared-explicit"
)

type Record struct {
	SessionID             string                          `json:"session_id"`
	ProjectID             string                          `json:"project_id"`
	RepoRoot              string                          `json:"repo_root"`
	WorkspaceType         WorkspaceType                   `json:"workspace_type"`
	IsolationMode         IsolationMode                   `json:"isolation_mode"`
	BaseRef               *string                         `json:"base_ref"`
	Branch                *string                         `json:"branch"`
	Worktree              *string                         `json:"worktree"`
	PermissionMode        string                          `json:"permission_mode"`
	RuntimeID             *string                         `json:"runtime_id"`
	JobIDs                []string                        `json:"job_ids"`
	ClientTag             *string                         `json:"client_tag"`
	CreatedAt             time.Time                       `json:"created_at"`
	LastActiveAt          time.Time                       `json:"last_active_at"`
	State                 State                           `json:"state"`
	FailureReason         *string                         `json:"failure_reason"`
	MetadataVersion       string                          `json:"metadata_version"`
	NativeSessionBindings map[string]NativeSessionBinding `json:"native_session_bindings,omitempty"`
}

func (r Record) Workspace() string {
	if r.WorkspaceType == WorkspaceGit && r.Worktree != nil {
		return *r.Worktree
	}
	return r.RepoRoot
}

type OpenRequest struct {
	ProjectID      string
	RepoRoot       string
	BaseRef        string
	PermissionMode string
	ClientTag      string
	IsolationMode  IsolationMode
}

type ListFilter struct {
	ActiveOnly bool
	ProjectID  string
	ClientTag  string
}

type Config struct {
	StorageRoot  string
	WorktreeRoot string
	Now          func() time.Time
	IDFactory    func() string
	Git          Git
}

type Git interface {
	DetectRepository(context.Context, string) (string, bool, error)
	ResolveBaseRef(context.Context, string, string) (string, string, error)
	AddWorktree(context.Context, string, string, string, string) error
	CurrentBranch(context.Context, string) (string, error)
	IsDirty(context.Context, string) (bool, error)
}
