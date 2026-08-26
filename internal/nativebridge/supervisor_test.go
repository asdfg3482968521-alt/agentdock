package nativebridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

func TestSupervisorObserveGracefulForcePermissionAndSameSessionGate(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{}, IDFactory: func() string { return "agentdock-supervisor" },
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3-d", RepoRoot: repo, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	harness := newNativeHarness()
	server := httptest.NewServer(http.HandlerFunc(harness.handler))
	defer server.Close()
	client, err := opencodenative.NewClient(opencodenative.Config{BaseURL: server.URL, ReadinessTimeout: time.Second, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := New(Options{Sessions: manager, Native: client, SubagentIDFactory: func() string { return "subagent-supervisor" }, NativeRuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bridge.Start(context.Background(), StartRequest{AgentDockSessionID: record.SessionID, ClientRequestID: "supervisor-start", Workspace: record.Workspace(), Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SubmitMessage(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), opencodenative.TextPrompt("initial")); err != nil {
		t.Fatal(err)
	}

	stop := errors.New("stop after durable public event")
	err = bridge.ObserveEvents(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), func(_ opencodenative.NativeEvent, _ int64) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("observe event: %v", err)
	}
	observation, err := bridge.ObserveSupervisor(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), 0, 32)
	if err != nil {
		t.Fatal(err)
	}
	if observation.RootOpenCodeSessionID != binding.OpenCodeSessionID || len(observation.Messages) == 0 || len(observation.Statuses) == 0 || len(observation.PublicEvents.Events) == 0 {
		t.Fatalf("incomplete observation: %+v", observation)
	}

	beforeCreate, _, _, beforeAbort := harness.counts()
	graceful, err := bridge.Correct(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), CorrectionRequest{Mode: CorrectionGraceful, Prompt: opencodenative.TextPrompt("stop the wrong direction and do SAFE_GRACEFUL")})
	if err != nil {
		t.Fatal(err)
	}
	if graceful.OpenCodeSessionIDBefore != binding.OpenCodeSessionID || graceful.OpenCodeSessionIDAfter != binding.OpenCodeSessionID || graceful.Aborted || graceful.PreemptiveClaimed {
		t.Fatalf("graceful same-session gate failed: %+v", graceful)
	}
	_, _, _, afterGracefulAbort := harness.counts()
	if afterGracefulAbort != beforeAbort {
		t.Fatalf("graceful correction must not abort: before=%d after=%d", beforeAbort, afterGracefulAbort)
	}

	force, err := bridge.Correct(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), CorrectionRequest{Mode: CorrectionForce, Prompt: opencodenative.TextPrompt("abort current work and do SAFE_FORCE")})
	if err != nil {
		t.Fatal(err)
	}
	if force.OpenCodeSessionIDBefore != binding.OpenCodeSessionID || force.OpenCodeSessionIDAfter != binding.OpenCodeSessionID || !force.Aborted || force.PreemptiveClaimed {
		t.Fatalf("force same-session gate failed: %+v", force)
	}
	afterCreate, _, _, afterForceAbort := harness.counts()
	if afterCreate != beforeCreate || afterForceAbort != beforeAbort+1 {
		t.Fatalf("replacement/abort mismatch create %d->%d abort %d->%d", beforeCreate, afterCreate, beforeAbort, afterForceAbort)
	}
	order, _, prompts := harness.supervisorTrace()
	if len(order) < 3 || order[len(order)-2] != "abort:"+binding.OpenCodeSessionID || order[len(order)-1] != "async:"+binding.OpenCodeSessionID {
		t.Fatalf("force ordering not abort->same-session correction: %v", order)
	}
	if len(prompts) < 2 {
		t.Fatalf("correction prompts not delivered: %d", len(prompts))
	}

	controlBeforeForeign, _, _ := harness.supervisorTrace()
	_, _, _, abortBeforeForeign := harness.counts()
	if _, err := bridge.Correct(context.Background(), record.SessionID, "subagent-not-owned", record.Workspace(), CorrectionRequest{Mode: CorrectionForce, Prompt: opencodenative.TextPrompt("must not run")}); ErrorCode(err) != ErrorOwnership {
		t.Fatalf("foreign correction should fail ownership before native control: code=%s err=%v", ErrorCode(err), err)
	}
	controlAfterForeign, _, _ := harness.supervisorTrace()
	_, _, _, abortAfterForeign := harness.counts()
	if len(controlAfterForeign) != len(controlBeforeForeign) || abortAfterForeign != abortBeforeForeign {
		t.Fatalf("foreign correction reached native control plane: order %d->%d abort %d->%d", len(controlBeforeForeign), len(controlAfterForeign), abortBeforeForeign, abortAfterForeign)
	}

	cancelResult, err := bridge.CancelSupervisor(context.Background(), record.SessionID, binding.SubagentID, record.Workspace())
	if err != nil {
		t.Fatal(err)
	}
	if !cancelResult.Aborted || cancelResult.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("cancel did not target exact native root: %+v", cancelResult)
	}
	createAfterCancel, _, _, abortAfterCancel := harness.counts()
	if createAfterCancel != beforeCreate || abortAfterCancel != abortBeforeForeign+1 {
		t.Fatalf("cancel replacement/abort mismatch create=%d want=%d abort=%d want=%d", createAfterCancel, beforeCreate, abortAfterCancel, abortBeforeForeign+1)
	}

	foreign := opencodenative.SessionInfo{ID: "ses-foreign-root", Directory: record.Workspace(), Metadata: map[string]any{}}
	harness.put(foreign)
	harness.setPermissions(
		opencodenative.PermissionRequest{ID: "per_owned", SessionID: binding.OpenCodeSessionID, Permission: "bash", Patterns: []string{"go test ./..."}, Metadata: map[string]any{"cwd": record.Workspace()}, Always: []string{"go test ./..."}, Tool: &opencodenative.PermissionTool{MessageID: "msg-owned", CallID: "call-owned"}},
		opencodenative.PermissionRequest{ID: "per_foreign", SessionID: foreign.ID, Permission: "bash", Patterns: []string{"danger"}, Metadata: map[string]any{"cwd": record.Workspace()}},
	)
	pending, err := bridge.PendingSupervisorPermissions(context.Background(), record.SessionID, binding.SubagentID, record.Workspace())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].RequestID != "per_owned" || pending[0].NativeSessionID != binding.OpenCodeSessionID || pending[0].RootOpenCodeSessionID != binding.OpenCodeSessionID || pending[0].AgentDockSessionID != record.SessionID || pending[0].SubagentID != binding.SubagentID || pending[0].Workspace != record.Workspace() || pending[0].PermissionMode != record.PermissionMode || pending[0].ApprovalScope != "once" || len(pending[0].Patterns) != 1 || pending[0].Patterns[0] != "go test ./..." || pending[0].Tool == nil || pending[0].Tool.CallID != "call-owned" {
		t.Fatalf("permission ownership/bounded-context identity mismatch: %+v", pending)
	}
	if _, err := bridge.RespondSupervisorPermission(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), "per_owned", PermissionDecisionApprove, ""); err != nil {
		t.Fatal(err)
	}
	_, replies, _ := harness.supervisorTrace()
	if len(replies) != 1 || replies[0].Reply != opencodenative.PermissionReplyOnce {
		t.Fatalf("supervisor approval must be bounded once, never always: %+v", replies)
	}
	harness.setPermissions(opencodenative.PermissionRequest{ID: "per_owned_reject", SessionID: binding.OpenCodeSessionID, Permission: "edit", Patterns: []string{"normal-workspace-file"}, Metadata: map[string]any{"cwd": record.Workspace()}})
	if _, err := bridge.RespondSupervisorPermission(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), "per_owned_reject", PermissionDecisionReject, "supervisor rejected"); err != nil {
		t.Fatal(err)
	}
	_, replies, _ = harness.supervisorTrace()
	if len(replies) != 2 || replies[1].Reply != opencodenative.PermissionReplyReject || replies[1].Message != "supervisor rejected" {
		t.Fatalf("owned supervisor rejection did not reach exact request: %+v", replies)
	}
	harness.setPermissions(
		opencodenative.PermissionRequest{ID: "per_foreign", SessionID: foreign.ID, Permission: "bash", Patterns: []string{"danger"}, Metadata: map[string]any{"cwd": record.Workspace()}},
	)
	if _, err := bridge.RespondSupervisorPermission(context.Background(), record.SessionID, binding.SubagentID, record.Workspace(), "per_foreign", PermissionDecisionReject, "no"); ErrorCode(err) != ErrorPermissionOwnership {
		t.Fatalf("foreign permission should be ownership denied before native reply: code=%s err=%v", ErrorCode(err), err)
	}
	_, repliesAfter, _ := harness.supervisorTrace()
	if len(repliesAfter) != len(replies) {
		t.Fatalf("foreign permission reached native control plane: before=%d after=%d", len(replies), len(repliesAfter))
	}

	persisted, err := manager.NativeSessionBinding(record.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.PendingSupervisorMessageID != "" || persisted.LastEventCursor < 5 {
		t.Fatalf("durable supervisor order/cursor not finalized: %+v", persisted)
	}
}

func TestNativePublicEventClassificationCoversSupervisorEvidence(t *testing.T) {
	cases := map[string]string{
		"session.status":            "activity",
		"message.updated":           "message",
		"session.next.tool.called":  "tool_call",
		"session.next.tool.success": "tool_result",
		"session.error":             "error",
		"test.completed":            "test",
		"file.edited":               "file",
		"session.completed":         "result",
		"permission.asked":          "permission",
	}
	for nativeType, want := range cases {
		properties, _ := json.Marshal(map[string]any{"sessionID": "ses-1"})
		got := classifyNativePublicEvent(opencodenative.NativeEvent{ID: "evt-1", Type: nativeType, Properties: properties})
		if got.Kind != want || got.NativeSessionID != "ses-1" {
			t.Fatalf("type=%s got=%+v wantKind=%s", nativeType, got, want)
		}
	}
}
