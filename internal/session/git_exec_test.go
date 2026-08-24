package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecGitResolveBaseRefContract(t *testing.T) {
	repo := createRealGitRepo(t)
	git := execGit{}
	ctx := context.Background()
	sha := gitTestOutput(t, repo, "rev-parse", "HEAD")

	for _, ref := range []string{"main", "refs/heads/main", "v1", "refs/tags/v1", sha} {
		_, commit, err := git.ResolveBaseRef(ctx, repo, ref)
		if err != nil {
			t.Fatalf("ResolveBaseRef(%q) error=%v", ref, err)
		}
		if commit != strings.ToLower(sha) {
			t.Fatalf("ResolveBaseRef(%q) commit=%q want=%q", ref, commit, sha)
		}
	}

	for _, ref := range []string{"HEAD~1", "../main", "main@{1}", "refs/remotes/origin/main", `C:\repo`, `main\evil`, "main..evil", "main:path"} {
		if _, _, err := git.ResolveBaseRef(ctx, repo, ref); ErrorCode(err) != CodeInvalidBaseRef {
			t.Fatalf("ResolveBaseRef(%q) expected %s, got %v", ref, CodeInvalidBaseRef, err)
		}
	}

	gitTestRun(t, repo, "branch", "release")
	gitTestRun(t, repo, "tag", "release")
	if _, _, err := git.ResolveBaseRef(ctx, repo, "release"); ErrorCode(err) != CodeInvalidBaseRef {
		t.Fatalf("ambiguous branch/tag expected %s, got %v", CodeInvalidBaseRef, err)
	}
}

func TestRealGitSessionsIsolateAndRecoverWorktrees(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	initRealGitRepo(t, repo)
	ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	config := Config{
		StorageRoot: filepath.Join(root, "state"), WorktreeRoot: filepath.Join(root, "worktrees"),
		IDFactory: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	manager, err := NewManager(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	a, err := manager.Open(context.Background(), OpenRequest{ProjectID: "demo", RepoRoot: repo, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.Open(context.Background(), OpenRequest{ProjectID: "demo", RepoRoot: repo, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Worktree == nil || b.Worktree == nil || a.Branch == nil || b.Branch == nil || pathEqual(*a.Worktree, *b.Worktree) || *a.Branch == *b.Branch {
		t.Fatalf("worktree isolation missing: A=%+v B=%+v", a, b)
	}
	if err := os.WriteFile(filepath.Join(*a.Worktree, "foo.txt"), []byte("A_ONLY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*b.Worktree, "foo.txt"), []byte("B_ONLY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertFileText(t, filepath.Join(*a.Worktree, "foo.txt"), "A_ONLY\n")
	assertFileText(t, filepath.Join(*b.Worktree, "foo.txt"), "B_ONLY\n")
	assertFileText(t, filepath.Join(repo, "foo.txt"), "BASE\n")
	if status := gitTestOutput(t, *a.Worktree, "status", "--porcelain"); !strings.Contains(status, "foo.txt") {
		t.Fatalf("A status missing its own change: %q", status)
	}
	if status := gitTestOutput(t, *b.Worktree, "status", "--porcelain"); !strings.Contains(status, "foo.txt") {
		t.Fatalf("B status missing its own change: %q", status)
	}

	restarted, err := NewManager(context.Background(), Config{StorageRoot: config.StorageRoot, WorktreeRoot: config.WorktreeRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Record{a, b} {
		got, err := restarted.Get(want.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != StateActive || got.Worktree == nil || *got.Worktree != *want.Worktree || got.Branch == nil || *got.Branch != *want.Branch || got.RuntimeID != nil {
			t.Fatalf("restart recovery mismatch: want=%+v got=%+v", want, got)
		}
	}

	closedA, err := restarted.Close(context.Background(), a.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if closedA.State != StateClosed {
		t.Fatalf("A close state=%s", closedA.State)
	}
	if _, err := os.Stat(*a.Worktree); err != nil {
		t.Fatalf("A worktree removed on close: %v", err)
	}
	gotB, err := restarted.Get(b.SessionID)
	if err != nil || gotB.State != StateActive {
		t.Fatalf("B affected by A close: %+v err=%v", gotB, err)
	}
}

func createRealGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	initRealGitRepo(t, repo)
	return repo
}

func initRealGitRepo(t *testing.T, repo string) {
	t.Helper()
	gitTestRun(t, repo, "init", "-b", "main")
	gitTestRun(t, repo, "config", "user.email", "agentdock-test@example.invalid")
	gitTestRun(t, repo, "config", "user.name", "AgentDock Test")
	if err := os.WriteFile(filepath.Join(repo, "foo.txt"), []byte("BASE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "add", "foo.txt")
	gitTestRun(t, repo, "commit", "-m", "base")
	gitTestRun(t, repo, "tag", "v1")
}

func gitTestRun(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", cmdArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(cmdArgs, " "), err, output)
	}
}

func gitTestOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(cmdArgs, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertFileText(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != want {
		t.Fatalf("%s=%q want=%q", path, body, want)
	}
}
