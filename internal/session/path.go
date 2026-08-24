package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func canonicalExistingDir(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", newError(CodeInvalidRepoRoot, "repo_root is required", nil)
	}
	if strings.ContainsRune(value, 0) {
		return "", newError(CodeInvalidRepoRoot, "repo_root contains an invalid byte", nil)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", newError(CodeInvalidRepoRoot, "resolve repo_root", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", newError(CodeInvalidRepoRoot, "resolve repo_root symlinks", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", newError(CodeInvalidRepoRoot, "stat repo_root", err)
	}
	if !info.IsDir() {
		return "", newError(CodeInvalidRepoRoot, "repo_root must be an existing directory", nil)
	}
	if filepath.Dir(real) == real {
		return "", newError(CodeInvalidRepoRoot, "refusing filesystem root as repo_root", nil)
	}
	return filepath.Clean(real), nil
}

func canonicalManagedRoot(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || !filepath.IsAbs(value) {
		return "", errors.New("session worktree root must be an absolute path")
	}
	clean := filepath.Clean(value)
	if filepath.Dir(clean) == clean {
		return "", errors.New("refusing filesystem root as session worktree root")
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return "", fmt.Errorf("create session worktree root: %w", err)
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve session worktree root: %w", err)
	}
	return filepath.Clean(real), nil
}

func managedWorktreePath(root, sessionID string) (string, error) {
	candidate := filepath.Clean(filepath.Join(root, sessionID))
	if !pathWithin(root, candidate) || pathEqual(root, candidate) {
		return "", newError(CodeInvalidRepoRoot, "managed worktree escaped worktree root", nil)
	}
	if _, err := os.Lstat(candidate); err == nil {
		return "", newError(CodeConflict, "managed worktree path already exists", nil)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return candidate, nil
}

func pathWithin(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if pathEqual(root, candidate) {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathEqual(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func cleanKey(value string) string {
	value = filepath.Clean(strings.TrimSpace(value))
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}
