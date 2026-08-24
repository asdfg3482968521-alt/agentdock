package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeGit struct {
	repos    map[string]string
	branches map[string]string
	dirty    map[string]bool
	addErr   error
}

func (g *fakeGit) DetectRepository(_ context.Context, path string) (string, bool, error) {
	if root, ok := g.repos[cleanKey(path)]; ok {
		return root, true, nil
	}
	return path, false, nil
}

func (g *fakeGit) ResolveBaseRef(_ context.Context, _ string, ref string) (string, string, error) {
	if ref == "bad" {
		return "", "", newError(CodeInvalidBaseRef, "bad ref", nil)
	}
	if ref == "" {
		ref = "main"
	}
	return ref, "0123456789abcdef0123456789abcdef01234567", nil
}

func (g *fakeGit) AddWorktree(_ context.Context, _ string, worktree, branch, _ string) error {
	if g.addErr != nil {
		return g.addErr
	}
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		return err
	}
	g.branches[cleanKey(worktree)] = branch
	return nil
}

func (g *fakeGit) CurrentBranch(_ context.Context, worktree string) (string, error) {
	branch, ok := g.branches[cleanKey(worktree)]
	if !ok {
		return "", errors.New("branch missing")
	}
	return branch, nil
}

func (g *fakeGit) IsDirty(_ context.Context, worktree string) (bool, error) {
	return g.dirty[cleanKey(worktree)], nil
}

func TestStateMachineContract(t *testing.T) {
	allowed := [][2]State{
		{StateOpening, StateActive},
		{StateOpening, StateFailed},
		{StateActive, StateClosing},
		{StateActive, StateFailed},
		{StateClosing, StateClosed},
		{StateClosing, StateFailed},
	}
	for _, pair := range allowed {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("expected %s -> %s to be legal: %v", pair[0], pair[1], err)
		}
	}
	for _, pair := range [][2]State{{StateClosed, StateClosed}, {StateFailed, StateClosing}, {StateActive, StateClosed}} {
		if err := ValidateTransition(pair[0], pair[1]); err == nil {
			t.Fatalf("expected %s -> %s to be rejected", pair[0], pair[1])
		}
	}
}

func TestOpenGitSessionsCreatesUniqueManagedWorktrees(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{cleanKey(repo): repo}, branches: map[string]string{}, dirty: map[string]bool{}}
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	}
	manager := newTestManager(t, root, git, func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	})

	a, err := manager.Open(context.Background(), OpenRequest{ProjectID: "demo", RepoRoot: repo, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.Open(context.Background(), OpenRequest{ProjectID: "demo", RepoRoot: repo, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if a.State != StateActive || b.State != StateActive {
		t.Fatalf("sessions must be ACTIVE: %s %s", a.State, b.State)
	}
	if a.SessionID == b.SessionID || a.Branch == nil || b.Branch == nil || *a.Branch == *b.Branch || a.Worktree == nil || b.Worktree == nil || cleanKey(*a.Worktree) == cleanKey(*b.Worktree) {
		t.Fatalf("git isolation not unique: A=%+v B=%+v", a, b)
	}
	managedRoot := filepath.Join(root, "worktrees")
	if !pathWithin(managedRoot, *a.Worktree) || !pathWithin(managedRoot, *b.Worktree) {
		t.Fatalf("worktree escaped managed root: %q %q", *a.Worktree, *b.Worktree)
	}
	if a.MetadataVersion != ContractVersion || a.WorkspaceType != WorkspaceGit || a.IsolationMode != IsolationWorktree {
		t.Fatalf("contract metadata mismatch: %+v", a)
	}
}

func TestOpenNonGitExclusiveConflict(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	ids := []string{"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"}
	manager := newTestManager(t, root, git, func() string { id := ids[0]; ids = ids[1:]; return id })
	if _, err := manager.Open(context.Background(), OpenRequest{ProjectID: "plain", RepoRoot: dir}); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Open(context.Background(), OpenRequest{ProjectID: "plain", RepoRoot: dir})
	if ErrorCode(err) != CodeConflict {
		t.Fatalf("expected %s, got %v", CodeConflict, err)
	}
}

func TestSharedReadonlyAllowsPeersAndDeniesMutation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	ids := []string{"55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"}
	manager := newTestManager(t, root, git, func() string { id := ids[0]; ids = ids[1:]; return id })
	for range 2 {
		if _, err := manager.Open(context.Background(), OpenRequest{ProjectID: "plain", RepoRoot: dir, IsolationMode: IsolationSharedReadonly}); err != nil {
			t.Fatal(err)
		}
	}
	items := manager.List(ListFilter{ActiveOnly: true})
	if len(items) != 2 {
		t.Fatalf("expected two readonly sessions, got %d", len(items))
	}
	if err := AuthorizeMutation(items[0]); ErrorCode(err) != CodeWriteDenied {
		t.Fatalf("expected mutation denial, got %v", err)
	}
}

func TestResolveRejectsWorkspaceMismatchAndClosedSession(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	other := filepath.Join(root, "other")
	for _, path := range []string{dir, other} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	manager := newTestManager(t, root, git, func() string { return "77777777-7777-4777-8777-777777777777" })
	record, err := manager.Open(context.Background(), OpenRequest{ProjectID: "plain", RepoRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(record.SessionID, other); ErrorCode(err) != CodeWorkspaceMismatch {
		t.Fatalf("expected workspace mismatch, got %v", err)
	}
	if _, err := manager.Close(context.Background(), record.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(record.SessionID, ""); ErrorCode(err) != CodeClosed {
		t.Fatalf("expected closed error, got %v", err)
	}
}

func TestPersistenceRecoversActiveSessionWithoutRuntimeID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	manager := newTestManager(t, root, git, func() string { return "88888888-8888-4888-8888-888888888888" })
	record, err := manager.Open(context.Background(), OpenRequest{ProjectID: "plain", RepoRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetRuntime(record.SessionID, "runtime-before-restart"); err != nil {
		t.Fatal(err)
	}
	recovered := newTestManager(t, root, git, func() string { return "99999999-9999-4999-8999-999999999999" })
	got, err := recovered.Get(record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateActive || got.RuntimeID != nil {
		t.Fatalf("restart recovery mismatch: %+v", got)
	}
}

func TestCloseIsIdempotentAndPreservesGitWorktree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{cleanKey(repo): repo}, branches: map[string]string{}, dirty: map[string]bool{}}
	manager := newTestManager(t, root, git, func() string { return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" })
	record, err := manager.Open(context.Background(), OpenRequest{ProjectID: "demo", RepoRoot: repo, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	git.dirty[cleanKey(*record.Worktree)] = true
	first, err := manager.Close(context.Background(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Close(context.Background(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != StateClosed || second.State != StateClosed || first.Worktree == nil || second.Worktree == nil || *first.Worktree != *second.Worktree {
		t.Fatalf("idempotent close mismatch: first=%+v second=%+v", first, second)
	}
	if _, err := os.Stat(*first.Worktree); err != nil {
		t.Fatalf("worktree was not preserved: %v", err)
	}
}

func TestFailedSessionCannotBeClosed(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{repos: map[string]string{cleanKey(repo): repo}, branches: map[string]string{}, dirty: map[string]bool{}, addErr: errors.New("boom")}
	manager := newTestManager(t, root, git, func() string { return "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" })
	record, err := manager.Open(context.Background(), OpenRequest{ProjectID: "demo", RepoRoot: repo, BaseRef: "main"})
	if err == nil || record.State != StateFailed {
		t.Fatalf("expected failed open, record=%+v err=%v", record, err)
	}
	if _, err := manager.Close(context.Background(), record.SessionID); ErrorCode(err) != CodeFailed {
		t.Fatalf("expected %s, got %v", CodeFailed, err)
	}
}

func TestUnknownSessionReturnsStableError(t *testing.T) {
	root := t.TempDir()
	manager := newTestManager(t, root, &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}, func() string { return "cccccccc-cccc-4ccc-8ccc-cccccccccccc" })
	_, err := manager.Get("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	if ErrorCode(err) != CodeNotFound {
		t.Fatalf("expected %s, got %v", CodeNotFound, err)
	}
}

func TestListFiltersProjectClientAndActiveDefinition(t *testing.T) {
	root := t.TempDir()
	dirs := []string{filepath.Join(root, "a"), filepath.Join(root, "b")}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := &fakeGit{repos: map[string]string{}, branches: map[string]string{}, dirty: map[string]bool{}}
	ids := []string{"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "ffffffff-ffff-4fff-8fff-ffffffffffff"}
	manager := newTestManager(t, root, git, func() string { id := ids[0]; ids = ids[1:]; return id })
	a, err := manager.Open(context.Background(), OpenRequest{ProjectID: "p1", RepoRoot: dirs[0], ClientTag: "chat-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Open(context.Background(), OpenRequest{ProjectID: "p2", RepoRoot: dirs[1], ClientTag: "chat-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Close(context.Background(), a.SessionID); err != nil {
		t.Fatal(err)
	}
	if got := manager.List(ListFilter{ActiveOnly: true}); len(got) != 1 || got[0].ProjectID != "p2" {
		t.Fatalf("active filter mismatch: %+v", got)
	}
	if got := manager.List(ListFilter{ProjectID: "p1", ClientTag: "chat-a"}); len(got) != 1 || got[0].SessionID != a.SessionID {
		t.Fatalf("project/client filter mismatch: %+v", got)
	}
}

func newTestManager(t *testing.T, root string, git Git, idFactory func() string) *Manager {
	t.Helper()
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	manager, err := NewManager(context.Background(), Config{
		StorageRoot:  filepath.Join(root, "sessions"),
		WorktreeRoot: filepath.Join(root, "worktrees"),
		Git:          git,
		IDFactory:    idFactory,
		Now: func() time.Time {
			now = now.Add(time.Millisecond)
			return now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
