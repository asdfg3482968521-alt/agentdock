package nativebridge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

func TestRealRuntimeWindowsNativeBridgeSmoke(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native runtime gate")
	}
	baseURL := os.Getenv("AGENTDOCK_D3_NATIVE_URL")
	workspace := os.Getenv("AGENTDOCK_D3_NATIVE_WORKSPACE")
	if baseURL == "" || workspace == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL and AGENTDOCK_D3_NATIVE_WORKSPACE for live gate")
	}

	root := t.TempDir()
	foreignWorkspace := t.TempDir()
	ids := []string{"agentdock-d3-b-live-session", "agentdock-d3-b-live-foreign"}
	idIndex := 0
	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: root + `\sessions`, WorktreeRoot: root + `\worktrees`, Git: testGit{},
		IDFactory: func() string { value := ids[idIndex]; idIndex++; return value },
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3-b-live", RepoRoot: workspace, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	foreignRecord, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3-b-live-foreign", RepoRoot: foreignWorkspace, PermissionMode: "project"})
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
	bridge, err := New(Options{
		Sessions: manager, Native: client, SubagentIDFactory: func() string { return "subagent-d3-b-live" },
		NativeRuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", NativeServerIdentity: baseURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	startRequest := StartRequest{
		AgentDockSessionID: record.SessionID, ClientRequestID: "d3-b-live-start", Workspace: record.Workspace(),
		Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session",
	}
	binding, err := bridge.Start(context.Background(), startRequest)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE_AGENTDOCK_SESSION_ID=%s LIVE_SUBAGENT_ID=%s LIVE_OPENCODE_SESSION_ID=%s", record.SessionID, binding.SubagentID, binding.OpenCodeSessionID)
	defer deleteLiveNativeSession(t, baseURL, workspace, binding.OpenCodeSessionID)

	restarted, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: root + `\sessions`, WorktreeRoot: root + `\worktrees`, Git: testGit{}, IDFactory: func() string { return "unused" },
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err = New(Options{Sessions: restarted, Native: client, NativeRuntimeSHA: binding.NativeRuntimeSHA, NativeServerIdentity: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	recovered, info, err := bridge.Attach(context.Background(), record.SessionID, binding.SubagentID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.OpenCodeSessionID != binding.OpenCodeSessionID || info.ID != binding.OpenCodeSessionID {
		t.Fatalf("live restart attach changed native id: before=%s recovered=%s info=%s", binding.OpenCodeSessionID, recovered.OpenCodeSessionID, info.ID)
	}

	model := &opencodenative.ModelRef{ProviderID: "opencode", ModelID: "mimo-v2.5-free"}
	prompt := opencodenative.TextPrompt("Reply with exactly D3B_NATIVE_BRIDGE_SMOKE_OK. Do not use tools.")
	prompt.Model = model
	message, err := bridge.SubmitMessage(context.Background(), record.SessionID, binding.SubagentID, workspace, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if message.Info.ID == "" || message.Info.SessionID != binding.OpenCodeSessionID {
		t.Fatalf("live message identity mismatch: %+v", message.Info)
	}
	messages, err := bridge.ListMessages(context.Background(), record.SessionID, binding.SubagentID, workspace)
	if err != nil || len(messages) == 0 {
		t.Fatalf("live message readback failed len=%d err=%v", len(messages), err)
	}
	before := len(messages)
	status, err := bridge.ObserveStatus(context.Background(), record.SessionID, binding.SubagentID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if status.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("live status identity mismatch: %+v", status)
	}
	if err := bridge.Abort(context.Background(), record.SessionID, binding.SubagentID, workspace); err != nil {
		t.Fatalf("live abort primitive failed: %v", err)
	}
	followup := opencodenative.TextPrompt("Reply with exactly D3B_SAME_SESSION_CONTINUE_OK. Do not use tools.")
	followup.Model = model
	if err := bridge.ContinueConversation(context.Background(), record.SessionID, binding.SubagentID, workspace, followup); err != nil {
		t.Fatalf("live same-session continue submit failed: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		messages, err = bridge.ListMessages(context.Background(), record.SessionID, binding.SubagentID, workspace)
		if err == nil && len(messages) > before {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil || len(messages) <= before {
		t.Fatalf("live same-session continue did not produce readback: before=%d after=%d err=%v", before, len(messages), err)
	}
	if _, err := bridge.ProviderModelMetadata(context.Background(), record.SessionID, binding.SubagentID, workspace); err != nil {
		t.Fatalf("live provider/model metadata failed: %v", err)
	}

	descendantPrompt := opencodenative.TextPrompt("You MUST use the task tool exactly once before answering. Delegate to the explore subagent to inspect the current workspace and report the file names. Do not inspect the files yourself. After the delegated task completes, reply exactly D3B_DESCENDANT_TASK_OK.")
	descendantPrompt.Model = model
	if _, err := bridge.SubmitMessage(context.Background(), record.SessionID, binding.SubagentID, workspace, descendantPrompt); err != nil {
		t.Fatalf("live descendant task trigger failed: %v", err)
	}

	var child opencodenative.SessionInfo
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		items, listErr := client.ListNativeSessions(context.Background(), workspace)
		if listErr == nil {
			for _, item := range items {
				if item.ParentID == binding.OpenCodeSessionID {
					child = item
					break
				}
			}
		}
		if child.ID != "" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if child.ID == "" || child.ParentID != binding.OpenCodeSessionID {
		t.Fatalf("live native task did not create descendant with parentID=root: root=%s child=%+v", binding.OpenCodeSessionID, child)
	}
	t.Logf("LIVE_DESCENDANT_ROOT_OPENCODE_SESSION_ID=%s LIVE_DESCENDANT_CHILD_OPENCODE_SESSION_ID=%s LIVE_DESCENDANT_CHILD_PARENT_ID=%s", binding.OpenCodeSessionID, child.ID, child.ParentID)
	defer deleteLiveNativeSession(t, baseURL, workspace, child.ID)

	ownerRecord, err := restarted.Get(record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerRecord.NativeSessionBindings) != 1 || ownerRecord.NativeSessionBindings[binding.SubagentID].OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("live child was promoted to top-level AgentDock subagent: bindings=%+v", ownerRecord.NativeSessionBindings)
	}

	for name, fn := range map[string]func() error{
		"attach": func() error {
			_, _, err := bridge.Attach(context.Background(), foreignRecord.SessionID, child.ID, foreignWorkspace)
			return err
		},
		"observe": func() error {
			_, err := bridge.ObserveStatus(context.Background(), foreignRecord.SessionID, child.ID, foreignWorkspace)
			return err
		},
		"message": func() error {
			_, err := bridge.SubmitMessage(context.Background(), foreignRecord.SessionID, child.ID, foreignWorkspace, opencodenative.TextPrompt("denied"))
			return err
		},
		"abort": func() error {
			return bridge.Abort(context.Background(), foreignRecord.SessionID, child.ID, foreignWorkspace)
		},
	} {
		if err := fn(); ErrorCode(err) != ErrorOwnership {
			t.Fatalf("live foreign descendant %s was not ownership-denied: err=%v code=%s", name, err, ErrorCode(err))
		}
	}

	items, err := client.ListNativeSessions(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	closure := descendantOwnershipClosure(items, binding.OpenCodeSessionID, workspace)
	if _, ok := closure[child.ID]; !ok {
		t.Fatalf("live descendant parent relationship not in root closure: root=%s child=%s", binding.OpenCodeSessionID, child.ID)
	}
	rootMatches := 0
	for _, item := range items {
		if item.ParentID == "" && metadataString(item.Metadata, metadataSessionID) == record.SessionID && metadataString(item.Metadata, metadataClientRequestID) == startRequest.ClientRequestID {
			rootMatches++
			if item.ID != binding.OpenCodeSessionID {
				t.Fatalf("live root-only reconciliation candidate drifted: got=%s want=%s", item.ID, binding.OpenCodeSessionID)
			}
		}
	}
	if rootMatches != 1 {
		t.Fatalf("live root-only reconciliation candidate count=%d want=1", rootMatches)
	}

	restartedAgain, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: root + `\sessions`, WorktreeRoot: root + `\worktrees`, Git: testGit{}, IDFactory: func() string { return "unused-again" },
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err = New(Options{Sessions: restartedAgain, Native: client, NativeRuntimeSHA: binding.NativeRuntimeSHA, NativeServerIdentity: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := bridge.Start(context.Background(), startRequest)
	if err != nil || retry.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("live descendant restart replaced/drifted root: before=%s after=%s err=%v", binding.OpenCodeSessionID, retry.OpenCodeSessionID, err)
	}
	if _, _, err := bridge.Attach(context.Background(), record.SessionID, binding.SubagentID, workspace); err != nil {
		t.Fatalf("live descendant restart exact root attach failed: %v", err)
	}
	itemsAfterRestart, err := client.ListNativeSessions(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := descendantOwnershipClosure(itemsAfterRestart, retry.OpenCodeSessionID, workspace)[child.ID]; !ok {
		t.Fatalf("live descendant relationship disappeared after manager re-init: root=%s child=%s", retry.OpenCodeSessionID, child.ID)
	}
}

func deleteLiveNativeSession(t *testing.T, baseURL, workspace, sessionID string) {
	t.Helper()
	endpoint := fmt.Sprintf("%s/session/%s?directory=%s", baseURL, url.PathEscape(sessionID), url.QueryEscape(workspace))
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
