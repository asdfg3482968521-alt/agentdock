package opencodeautonomous

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/nativebridge"
	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

type liveTestGit struct{}

func (liveTestGit) DetectRepository(_ context.Context, path string) (string, bool, error) {
	return path, false, nil
}
func (liveTestGit) ResolveBaseRef(context.Context, string, string) (string, string, error) {
	return "", "", nil
}
func (liveTestGit) AddWorktree(context.Context, string, string, string, string) error { return nil }
func (liveTestGit) CurrentBranch(context.Context, string) (string, error)             { return "", nil }
func (liveTestGit) IsDirty(context.Context, string) (bool, error)                     { return false, nil }

func TestRealRuntimeWindowsAutonomousBackendGate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows autonomous runtime gate")
	}
	baseURL := strings.TrimSpace(os.Getenv("AGENTDOCK_D3_NATIVE_URL"))
	workspace := strings.TrimSpace(os.Getenv("AGENTDOCK_D3C_LIVE_WORKSPACE"))
	if baseURL == "" || workspace == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL and AGENTDOCK_D3C_LIVE_WORKSPACE for live gate")
	}

	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: t.TempDir() + `\sessions`, WorktreeRoot: t.TempDir() + `\worktrees`, Git: liveTestGit{},
		IDFactory: func() string { return "agentdock-d3-c-live-session" },
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Open(context.Background(), session.OpenRequest{
		ProjectID: "d3-c-live", RepoRoot: workspace, PermissionMode: "project",
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := opencodenative.NewClient(
		opencodenative.Config{BaseURL: baseURL, ReadinessTimeout: 30 * time.Second, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 500 * time.Millisecond},
		&http.Client{Timeout: 180 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := nativebridge.New(nativebridge.Options{
		Sessions: manager, Native: client, SubagentIDFactory: func() string { return "subagent-d3-c-live" },
		NativeRuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", NativeServerIdentity: baseURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := New(bridge)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()
	var telemetry []TelemetryEvent
	result, err := backend.Run(ctx, Request{
		AgentID: "agent-d3-c-live", AgentDockSessionID: record.SessionID, Workspace: record.Workspace(),
		PermissionMode: "project", Provider: "opencode", Model: "mimo-v2.5-free", NativeAgent: "build", Timeout: 160 * time.Second,
		Task: "Investigate this small Windows fixture as a software engineer. A PowerShell verification test currently fails. Preserve the public parameters of math.ps1, determine the defect from the workspace files, fix the implementation, and finish only when the fixture is verified.",
		Constraints: []string{
			"Use the exact current workspace only for writes.",
			"The task is complete only after verify.ps1 succeeds and creates verified.txt containing D3C_TEST_OK.",
			"Also run verify-env.ps1 so PowerShell, PATH, and git availability are checked and env-ok.txt contains D3C_ENV_OK.",
			"Inspect git status before finishing so the resulting workspace change is visible.",
			"In the final answer include exactly the marker D3C_AUTONOMOUS_GATE_OK once the checks pass.",
		},
	}, func(event TelemetryEvent) error {
		telemetry = append(telemetry, event)
		return nil
	})
	if err != nil {
		t.Fatalf("autonomous live run: %v", err)
	}
	defer deleteLiveAutonomousSession(t, baseURL, workspace, result.RootOpenCodeSessionID)

	if result.RootOpenCodeSessionID == "" || result.SubagentID != "subagent-d3-c-live" {
		t.Fatalf("native root binding=%+v", result)
	}
	if result.Provider != "opencode" || result.Model != "mimo-v2.5-free" {
		t.Fatalf("provider/model propagation=%+v", result)
	}
	if result.AutonomousToolSteps < 4 {
		t.Fatalf("AUTONOMOUS_TOOL_STEPS=%d want>=4 telemetry=%+v", result.AutonomousToolSteps, result.Telemetry)
	}
	if !strings.Contains(result.FinalText, "D3C_AUTONOMOUS_GATE_OK") {
		t.Fatalf("final result missing gate marker: %q", result.FinalText)
	}
	verified, err := os.ReadFile(workspace + `\verified.txt`)
	if err != nil || strings.TrimSpace(string(verified)) != "D3C_TEST_OK" {
		t.Fatalf("verify.ps1 evidence missing: data=%q err=%v", verified, err)
	}
	envOK, err := os.ReadFile(workspace + `\env-ok.txt`)
	if err != nil || strings.TrimSpace(string(envOK)) != "D3C_ENV_OK" {
		t.Fatalf("verify-env.ps1 evidence missing: data=%q err=%v", envOK, err)
	}

	var sawRead, sawMutation, sawCommand, sawVerify, sawEnv, sawGitStatus bool
	for _, event := range telemetry {
		switch event.Tool {
		case "read", "glob", "grep":
			sawRead = true
		case "edit", "write", "apply_patch":
			sawMutation = true
		case "bash":
			sawCommand = true
		}
		input := strings.ToLower(string(event.Input))
		if strings.Contains(input, "verify.ps1") {
			sawVerify = true
		}
		if strings.Contains(input, "verify-env.ps1") {
			sawEnv = true
		}
		if strings.Contains(input, "git status") {
			sawGitStatus = true
		}
	}
	if !sawRead || !sawMutation || !sawCommand || !sawVerify || !sawEnv || !sawGitStatus {
		t.Fatalf("Windows autonomous sequence incomplete read=%v mutation=%v command=%v verify=%v env=%v git_status=%v telemetry=%+v", sawRead, sawMutation, sawCommand, sawVerify, sawEnv, sawGitStatus, telemetry)
	}
	t.Logf("D3C_WINDOWS_GATE root=%s subagent=%s AUTONOMOUS_TOOL_STEPS=%d final=%q", result.RootOpenCodeSessionID, result.SubagentID, result.AutonomousToolSteps, result.FinalText)
}

func deleteLiveAutonomousSession(t *testing.T, baseURL, workspace, sessionID string) {
	t.Helper()
	if sessionID == "" {
		return
	}
	endpoint := fmt.Sprintf("%s/session/%s?directory=%s", strings.TrimRight(baseURL, "/"), url.PathEscape(sessionID), url.QueryEscape(workspace))
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		t.Logf("live cleanup request: %v", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("live cleanup failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Logf("live cleanup returned HTTP %d", resp.StatusCode)
	}
}
