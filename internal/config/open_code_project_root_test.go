package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCodeProjectRootNormalizesAndCreatesDirectory(t *testing.T) {
	home := t.TempDir()
	setTestUserHome(t, home)
	root := filepath.Join(home, "projects", "OpenCode")
	t.Setenv("AGENTDOCK_OPENCODE_PROJECT_ROOT", root)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.OpenCodeProjectRoot != root {
		t.Fatalf("OpenCodeProjectRoot=%q want %q", cfg.OpenCodeProjectRoot, root)
	}
	if _, err := os.Stat(cfg.OpenCodeProjectRoot); err != nil {
		t.Fatalf("OpenCodeProjectRoot was not created: %v", err)
	}
}

func TestOpenCodeProjectRootFallsBackToHomeOpenCode(t *testing.T) {
	home := t.TempDir()
	setTestUserHome(t, home)
	t.Setenv("AGENTDOCK_OPENCODE_PROJECT_ROOT", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "OpenCode")
	if cfg.OpenCodeProjectRoot != want {
		t.Fatalf("OpenCodeProjectRoot=%q want %q", cfg.OpenCodeProjectRoot, want)
	}
}
