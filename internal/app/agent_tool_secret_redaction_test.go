package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	jobruntime "github.com/uvwt/agentdock/internal/job"
)

type knownSecretAgentJobExecutor struct {
	secret string
}

func (e *knownSecretAgentJobExecutor) Execute(_ context.Context, request jobruntime.RuntimeExecutionRequest) (jobruntime.RuntimeExecutionHandle, error) {
	_ = request.OnOutput(jobruntime.RuntimeOutputEvent{Stream: "stdout", Text: "executor stdout " + e.secret + "\n"})
	completion := make(chan jobruntime.RuntimeExecutionResult, 1)
	completion <- jobruntime.RuntimeExecutionResult{OK: true, ExitCode: 0, Summary: "executor summary " + e.secret}
	close(completion)
	return jobruntime.RuntimeExecutionHandle{RuntimeID: "known-secret-" + request.JobID, Completion: completion}, nil
}

func (e *knownSecretAgentJobExecutor) Cancel(context.Context, jobruntime.RuntimeCancelRequest) error {
	return nil
}

func TestAgentToolBridgePassesKnownSecretOnlyToEphemeralJobRedactor(t *testing.T) {
	const secret = "D2_TEST_SECRET_agent_bridge_job"
	rt, root := newSessionToolsRuntime(t)
	workspace := filepath.Join(root, "known-secret-project")
	if err := ensureDir(workspace); err != nil {
		t.Fatal(err)
	}
	session := mustOpenSession(t, rt, map[string]any{"project_id": "known-secret-project", "repo_root": workspace})
	jobRoot := filepath.Join(root, "known-secret-jobs")
	manager, err := jobruntime.NewManager(context.Background(), &knownSecretAgentJobExecutor{secret: secret}, jobruntime.Config{StorageRoot: jobRoot, Limits: jobruntime.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	rt.jobs = manager
	bridge := newAgentToolBridge(rt)
	request := agentToolRequest("agent-secret", session.SessionID, session.Workspace(), "project", "opencode_read", map[string]any{})
	request.KnownSecrets = [][]byte{[]byte(secret)}
	result, err := bridge.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, secret) {
		t.Fatalf("Agent Tool Bridge returned known credential plaintext: %q", result.Text)
	}
	if !strings.Contains(result.Text, "[REDACTED]") {
		t.Fatalf("Agent Tool Bridge output did not redact known credential: %q", result.Text)
	}
	status, err := manager.Status(result.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{status.ResultSummary, status.Error, status.CancelReason} {
		if strings.Contains(value, secret) {
			t.Fatalf("Agent-owned Job status leaked known credential: %+v", status)
		}
	}
	for _, name := range []string{"metadata.json", "events.ndjson"} {
		body, err := os.ReadFile(filepath.Join(jobRoot, result.JobID, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), secret) {
			t.Fatalf("Agent-owned Job %s persisted known credential", name)
		}
	}
}

var _ agentruntime.ToolBridge = (*appAgentToolBridge)(nil)
