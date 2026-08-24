package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type execGit struct{}

type gitCommandError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitCommandError) Error() string {
	if strings.TrimSpace(e.stderr) != "" {
		return fmt.Sprintf("git %s: %s", strings.Join(e.args, " "), strings.TrimSpace(e.stderr))
	}
	return fmt.Sprintf("git %s: %v", strings.Join(e.args, " "), e.err)
}
func (e *gitCommandError) Unwrap() error { return e.err }

func (execGit) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &gitCommandError{args: append([]string(nil), args...), stderr: stderr.String(), err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (g execGit) DetectRepository(ctx context.Context, path string) (string, bool, error) {
	output, err := g.run(ctx, "-C", path, "rev-parse", "--show-toplevel")
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return "", false, err
		}
		var commandErr *gitCommandError
		if errors.As(err, &commandErr) && strings.Contains(strings.ToLower(commandErr.stderr), "not a git repository") {
			return path, false, nil
		}
		return "", false, err
	}
	root, err := canonicalExistingDir(output)
	if err != nil {
		return "", false, err
	}
	return root, true, nil
}

var fullCommitSHA = regexp.MustCompile(`(?i)^[0-9a-f]{40}$`)

func (g execGit) ResolveBaseRef(ctx context.Context, repoRoot, raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = "HEAD"
	}
	if err := validateBaseRefInput(value); err != nil {
		return "", "", err
	}
	if fullCommitSHA.MatchString(value) {
		commit, err := g.resolveCommit(ctx, repoRoot, value)
		if err != nil {
			return "", "", newError(CodeInvalidBaseRef, "full commit SHA does not resolve to a commit", err)
		}
		return strings.ToLower(value), commit, nil
	}
	if value == "HEAD" {
		branch, err := g.run(ctx, "-C", repoRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil || strings.TrimSpace(branch) == "" {
			return "", "", newError(CodeInvalidBaseRef, "HEAD is not attached to a local branch; provide an explicit branch, tag, or full commit SHA", err)
		}
		value = strings.TrimSpace(branch)
	}

	if strings.HasPrefix(value, "refs/heads/") {
		name := strings.TrimPrefix(value, "refs/heads/")
		if err := g.validateBranchName(ctx, name); err != nil {
			return "", "", err
		}
		commit, err := g.resolveCommit(ctx, repoRoot, "refs/heads/"+name)
		if err != nil {
			return "", "", newError(CodeInvalidBaseRef, "local branch does not resolve", err)
		}
		return name, commit, nil
	}
	if strings.HasPrefix(value, "refs/tags/") {
		name := strings.TrimPrefix(value, "refs/tags/")
		if err := g.validateTagName(ctx, name); err != nil {
			return "", "", err
		}
		commit, err := g.resolveCommit(ctx, repoRoot, "refs/tags/"+name)
		if err != nil {
			return "", "", newError(CodeInvalidBaseRef, "tag does not resolve", err)
		}
		return name, commit, nil
	}
	if strings.HasPrefix(value, "refs/") {
		return "", "", newError(CodeInvalidBaseRef, "only refs/heads and refs/tags are accepted", nil)
	}

	branchValid := g.validateBranchName(ctx, value) == nil
	tagValid := g.validateTagName(ctx, value) == nil
	branchCommit, branchErr := g.resolveCommit(ctx, repoRoot, "refs/heads/"+value)
	tagCommit, tagErr := g.resolveCommit(ctx, repoRoot, "refs/tags/"+value)
	branchExists := branchValid && branchErr == nil
	tagExists := tagValid && tagErr == nil
	if branchExists && tagExists {
		return "", "", newError(CodeInvalidBaseRef, "base_ref is ambiguous between a local branch and tag", nil)
	}
	if branchExists {
		return value, branchCommit, nil
	}
	if tagExists {
		return value, tagCommit, nil
	}
	return "", "", newError(CodeInvalidBaseRef, "base_ref must name an existing local branch, tag, or full commit SHA", nil)
}

func validateBaseRefInput(value string) error {
	if value == "" || strings.ContainsRune(value, 0) || strings.Contains(value, `\`) || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasPrefix(value, "-") || filepath.IsAbs(value) {
		return newError(CodeInvalidBaseRef, "base_ref contains forbidden revision or path syntax", nil)
	}
	if strings.Contains(value, ":") {
		return newError(CodeInvalidBaseRef, "base_ref contains path/revision syntax", nil)
	}
	return nil
}

func (g execGit) validateBranchName(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return newError(CodeInvalidBaseRef, "branch name is empty", nil)
	}
	if _, err := g.run(ctx, "check-ref-format", "--branch", name); err != nil {
		return newError(CodeInvalidBaseRef, "invalid local branch name", err)
	}
	return nil
}

func (g execGit) validateTagName(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return newError(CodeInvalidBaseRef, "tag name is empty", nil)
	}
	if _, err := g.run(ctx, "check-ref-format", "refs/tags/"+name); err != nil {
		return newError(CodeInvalidBaseRef, "invalid tag name", err)
	}
	return nil
}

func (g execGit) resolveCommit(ctx context.Context, repoRoot, ref string) (string, error) {
	commit, err := g.run(ctx, "-C", repoRoot, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if !fullCommitSHA.MatchString(commit) {
		return "", fmt.Errorf("git returned invalid commit id %q", commit)
	}
	return strings.ToLower(commit), nil
}

func (g execGit) AddWorktree(ctx context.Context, repoRoot, worktree, branch, commit string) error {
	if _, err := g.run(ctx, "check-ref-format", "--branch", branch); err != nil {
		return newError(CodeInvalidBaseRef, "generated session branch is invalid", err)
	}
	if _, err := g.run(ctx, "-C", repoRoot, "worktree", "add", "-b", branch, worktree, commit); err != nil {
		return newError(CodeConflict, "failed to create isolated branch/worktree", err)
	}
	return nil
}

func (g execGit) CurrentBranch(ctx context.Context, worktree string) (string, error) {
	return g.run(ctx, "-C", worktree, "symbolic-ref", "--quiet", "--short", "HEAD")
}

func (g execGit) IsDirty(ctx context.Context, worktree string) (bool, error) {
	status, err := g.run(ctx, "-C", worktree, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(status) != "", nil
}
