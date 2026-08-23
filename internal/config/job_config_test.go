package config

import (
	"path/filepath"
	"testing"
)

func TestJobConfigDefaultsStorageUnderAgentDockHome(t *testing.T) {
	home := t.TempDir()
	cfg := Config{AgentDockHome: filepath.Join(home, "state"), AgentDockDefaultDir: filepath.Join(home, "work")}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cfg.AgentDockHome, "jobs")
	if cfg.JobStorageRoot != want {
		t.Fatalf("JobStorageRoot=%q want %q", cfg.JobStorageRoot, want)
	}
}

func TestJobConfigReadsCentralizedLimitOverrides(t *testing.T) {
	t.Setenv("AGENTDOCK_JOB_STORAGE_ROOT", filepath.Join(t.TempDir(), "jobs"))
	t.Setenv("AGENTDOCK_JOB_MAX_CONCURRENT", "6")
	t.Setenv("AGENTDOCK_JOB_MAX_CONCURRENT_PER_WORKSPACE", "3")
	t.Setenv("AGENTDOCK_JOB_MAX_CONCURRENT_WRITE_PER_WORKSPACE", "1")
	t.Setenv("AGENTDOCK_JOB_MEMORY_OUTPUT_BYTES", "2097152")
	t.Setenv("AGENTDOCK_JOB_RESPONSE_OUTPUT_BYTES", "131072")
	t.Setenv("AGENTDOCK_JOB_DISK_SPOOL_BYTES", "33554432")
	t.Setenv("AGENTDOCK_JOB_RETENTION_DAYS", "5")
	t.Setenv("AGENTDOCK_JOB_RETENTION_MAX_COMPLETED", "100")
	t.Setenv("AGENTDOCK_JOB_MAX_TIMEOUT_MS", "43200000")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JobMaxConcurrent != 6 || cfg.JobMaxConcurrentPerWorkspace != 3 || cfg.JobMaxConcurrentWritePerWorkspace != 1 {
		t.Fatalf("unexpected concurrency config: %+v", cfg)
	}
	if cfg.JobMemoryOutputBytes != 2097152 || cfg.JobResponseOutputBytes != 131072 || cfg.JobDiskSpoolBytes != 33554432 {
		t.Fatalf("unexpected output config: %+v", cfg)
	}
	if cfg.JobRetentionDays != 5 || cfg.JobRetentionMaxCompleted != 100 || cfg.JobMaxTimeoutMS != 43200000 {
		t.Fatalf("unexpected retention/timeout config: %+v", cfg)
	}
}
