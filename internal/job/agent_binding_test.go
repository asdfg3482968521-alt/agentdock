package job

import (
	"context"
	"testing"
	"time"
)

func TestAgentOwnedJobPersistsOptionalAgentIDWithoutBreakingLegacyJobs(t *testing.T) {
	executor := newFakeExecutor()
	config := testConfig(t)
	manager, err := NewManager(context.Background(), executor, config)
	if err != nil {
		t.Fatal(err)
	}

	agentJob, err := manager.Start(StartRequest{
		SessionID: "session-a", AgentID: "agent-a", RuntimeScope: "session:session-a",
		Workspace: t.TempDir(), Tool: "read", Arguments: map[string]any{"filePath": "README.md"}, PermissionMode: "project", ConcurrencyClass: ConcurrencyRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan(agentJob.JobID, fakePlan{duration: time.Millisecond, result: RuntimeExecutionResult{OK: true}})
	agentStatus := waitState(t, manager, agentJob.JobID, StateSucceeded)
	if agentStatus.AgentID != "agent-a" {
		t.Fatalf("AgentID=%q", agentStatus.AgentID)
	}

	legacyJob, err := manager.Start(StartRequest{Workspace: t.TempDir(), Tool: "read", Arguments: map[string]any{"filePath": "README.md"}, PermissionMode: "project", ConcurrencyClass: ConcurrencyRead})
	if err != nil {
		t.Fatal(err)
	}
	executor.setPlan(legacyJob.JobID, fakePlan{duration: time.Millisecond, result: RuntimeExecutionResult{OK: true}})
	legacyStatus := waitState(t, manager, legacyJob.JobID, StateSucceeded)
	if legacyStatus.AgentID != "" {
		t.Fatalf("legacy AgentID=%q want empty", legacyStatus.AgentID)
	}

	recovered, err := NewManager(context.Background(), newFakeExecutor(), config)
	if err != nil {
		t.Fatal(err)
	}
	recoveredStatus, err := recovered.Status(agentJob.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredStatus.AgentID != "agent-a" {
		t.Fatalf("recovered AgentID=%q", recoveredStatus.AgentID)
	}
}

func TestAgentIDRequiresSessionOwnershipTuple(t *testing.T) {
	manager, err := NewManager(context.Background(), newFakeExecutor(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Start(StartRequest{AgentID: "agent-a", Workspace: t.TempDir(), Tool: "read", Arguments: map[string]any{}, PermissionMode: "project"})
	if err == nil {
		t.Fatal("agent-owned job without session_id unexpectedly accepted")
	}
}
