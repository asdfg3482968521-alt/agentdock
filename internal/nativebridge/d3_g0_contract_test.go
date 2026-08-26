package nativebridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

const (
	d3G0BaseSHA          = "4cf08c3f76a945b7284e8f19befdda9fb3eb9ab6"
	d3G0NativeRuntimeSHA = "d5664f38002d8326eca179cf2efb5774c8ce1247"
)

type d3G0Contract struct {
	Version                   string              `json:"version"`
	BaseSHA                   string              `json:"base_sha"`
	ProductionPoolImplemented bool                `json:"production_pool_implemented"`
	Roots                     []d3G0RootContract  `json:"roots"`
	NegativeTests             []d3G0MatrixCase    `json:"negative_tests"`
	SandboxIsolation          []d3G0MatrixCase    `json:"sandbox_isolation"`
	Dependencies              []string            `json:"g1_dependencies"`
	Restart                   d3G0RestartContract `json:"restart"`
	Cursor                    d3G0CursorContract  `json:"cursor"`
}

type d3G0RootContract struct {
	Name                  string `json:"name"`
	SubagentID            string `json:"subagent_id"`
	AgentDockSessionID    string `json:"agentdock_session_id"`
	RootOpenCodeSessionID string `json:"root_opencode_session_id"`
	Ownership             string `json:"ownership"`
	Workspace             string `json:"workspace"`
	RuntimeScope          string `json:"runtime_scope"`
	StatusEventsResult    string `json:"status_events_result"`
	SandboxProfile        string `json:"sandbox_profile"`
}

type d3G0MatrixCase struct {
	Name      string `json:"name"`
	ActorRoot string `json:"actor_root"`
	Target    string `json:"target"`
	Operation string `json:"operation"`
	Expected  string `json:"expected"`
}

type d3G0RestartContract struct {
	PreserveExactRoots bool `json:"preserve_exact_roots"`
	LostRootIsolation  bool `json:"lost_root_isolation"`
	ChildNotPromoted   bool `json:"child_not_promoted"`
}

type d3G0CursorContract struct {
	PerSubagentIndependent bool `json:"per_subagent_independent"`
}

func TestD3G0ContractMatricesFrozen(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "d3-g0-isolation-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract d3G0Contract
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Version != "D3-G0-v1" || contract.BaseSHA != d3G0BaseSHA {
		t.Fatalf("contract identity drift: version=%q base=%q", contract.Version, contract.BaseSHA)
	}
	if contract.ProductionPoolImplemented {
		t.Fatal("G0 contract must not claim a production pool implementation")
	}

	if len(contract.Roots) != 3 {
		t.Fatalf("root count=%d want=3", len(contract.Roots))
	}
	rootNames := make([]string, 0, 3)
	seenSubagent := map[string]bool{}
	seenSession := map[string]bool{}
	seenNative := map[string]bool{}
	seenWorkspace := map[string]bool{}
	seenRuntime := map[string]bool{}
	seenSandbox := map[string]bool{}
	for _, root := range contract.Roots {
		rootNames = append(rootNames, root.Name)
		for label, value := range map[string]string{
			"subagent_id": root.SubagentID, "agentdock_session_id": root.AgentDockSessionID,
			"root_opencode_session_id": root.RootOpenCodeSessionID, "workspace": root.Workspace,
			"runtime_scope": root.RuntimeScope, "sandbox_profile": root.SandboxProfile,
		} {
			if value == "" {
				t.Fatalf("root %s missing %s", root.Name, label)
			}
		}
		if root.Ownership != "EXCLUSIVE_ROOT" || root.StatusEventsResult != "ROOT_SCOPED" {
			t.Fatalf("root %s authority drift: %+v", root.Name, root)
		}
		if seenSubagent[root.SubagentID] || seenSession[root.AgentDockSessionID] || seenNative[root.RootOpenCodeSessionID] || seenWorkspace[root.Workspace] || seenRuntime[root.RuntimeScope] || seenSandbox[root.SandboxProfile] {
			t.Fatalf("root %s reuses an identity/capability boundary", root.Name)
		}
		seenSubagent[root.SubagentID] = true
		seenSession[root.AgentDockSessionID] = true
		seenNative[root.RootOpenCodeSessionID] = true
		seenWorkspace[root.Workspace] = true
		seenRuntime[root.RuntimeScope] = true
		seenSandbox[root.SandboxProfile] = true
	}
	sort.Strings(rootNames)
	if got := rootNames[0] + rootNames[1] + rootNames[2]; got != "ABC" {
		t.Fatalf("roots=%v want A/B/C", rootNames)
	}

	requireCases(t, contract.NegativeTests, []string{
		"targeted-correction-a-only",
		"targeted-cancel-b-only",
		"c-unaffected-by-a-correction-b-cancel",
		"cross-root-observe-denied",
		"cross-root-message-denied",
		"cross-root-abort-denied",
		"foreign-child-denied",
		"child-not-promoted-to-root",
		"workspace-isolation",
		"runtime-scope-isolation",
	})
	requireCases(t, contract.SandboxIsolation, []string{
		"external-read-only-not-equal-foreign-write",
		"external-writable-mount-not-inherited-by-sibling-roots",
		"child-inherits-parent-root-sandbox",
		"delete-approval-not-shared-across-roots",
		"external-read-only-root-delete-denied",
		"auto-approve-does-not-override-deny",
	})
	for _, item := range append(append([]d3G0MatrixCase(nil), contract.NegativeTests...), contract.SandboxIsolation...) {
		if item.Expected != "ALLOW" && item.Expected != "DENY" && item.Expected != "UNAFFECTED" && item.Expected != "INHERIT" {
			t.Fatalf("case %s has non-frozen expected=%q", item.Name, item.Expected)
		}
	}
	if !contract.Restart.PreserveExactRoots || !contract.Restart.LostRootIsolation || !contract.Restart.ChildNotPromoted {
		t.Fatalf("restart contract incomplete: %+v", contract.Restart)
	}
	if !contract.Cursor.PerSubagentIndependent {
		t.Fatal("per-subagent cursor independence is not frozen")
	}
	wantDeps := []string{"D3-C", "D3-D", "D3-F1", "D3-S1"}
	if len(contract.Dependencies) != len(wantDeps) {
		t.Fatalf("G1 dependencies=%v want=%v", contract.Dependencies, wantDeps)
	}
	for i := range wantDeps {
		if contract.Dependencies[i] != wantDeps[i] {
			t.Fatalf("G1 dependencies=%v want=%v", contract.Dependencies, wantDeps)
		}
	}
}

func requireCases(t *testing.T, items []d3G0MatrixCase, required []string) {
	t.Helper()
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Name == "" || item.ActorRoot == "" || item.Target == "" || item.Operation == "" || item.Expected == "" {
			t.Fatalf("incomplete matrix case: %+v", item)
		}
		if seen[item.Name] {
			t.Fatalf("duplicate matrix case %q", item.Name)
		}
		seen[item.Name] = true
	}
	for _, name := range required {
		if !seen[name] {
			t.Fatalf("required matrix case %q is missing", name)
		}
	}
}

func TestD3G0ExistingBridgeSupportsIndependentABCExactRoots(t *testing.T) {
	root := t.TempDir()
	repos := map[string]string{}
	for _, name := range []string{"A", "B", "C"} {
		path := filepath.Join(root, "repo-"+name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		repos[name] = path
	}
	ids := []string{"agentdock-session-a", "agentdock-session-b", "agentdock-session-c"}
	idx := 0
	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{},
		IDFactory: func() string { value := ids[idx]; idx++; return value },
		Now:       func() time.Time { return time.Date(2026, 8, 26, 8, 41, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]session.Record{}
	for _, name := range []string{"A", "B", "C"} {
		record, openErr := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3-g0-" + name, RepoRoot: repos[name], PermissionMode: "project"})
		if openErr != nil {
			t.Fatal(openErr)
		}
		records[name] = record
	}

	harness := newNativeHarness()
	server := httptest.NewServer(http.HandlerFunc(harness.handler))
	defer server.Close()
	client, err := opencodenative.NewClient(opencodenative.Config{BaseURL: server.URL, ReadinessTimeout: time.Second, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := New(Options{Sessions: manager, Native: client, NativeRuntimeSHA: d3G0NativeRuntimeSHA, NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	bindings := map[string]session.NativeSessionBinding{}
	requests := map[string]StartRequest{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range []string{"A", "B", "C"} {
		name := name
		record := records[name]
		request := StartRequest{
			AgentDockSessionID: record.SessionID, ClientRequestID: "d3-g0-root-" + name, SubagentID: "subagent-" + name,
			Workspace: record.Workspace(), Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session",
		}
		requests[name] = request
		wg.Add(1)
		go func() {
			defer wg.Done()
			binding, startErr := bridge.Start(context.Background(), request)
			mu.Lock()
			defer mu.Unlock()
			if startErr != nil {
				t.Errorf("parallel root %s start: %v", name, startErr)
				return
			}
			bindings[name] = binding
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	if len(bindings) != 3 {
		t.Fatalf("parallel roots=%d want=3", len(bindings))
	}
	seenNative := map[string]bool{}
	for _, name := range []string{"A", "B", "C"} {
		binding := bindings[name]
		record := records[name]
		if binding.SubagentID != "subagent-"+name || binding.AgentDockSessionID != record.SessionID || binding.Workspace != record.Workspace() || binding.RuntimeScope != runtimeScope(record.SessionID) {
			t.Fatalf("root %s identity drift: %+v record=%+v", name, binding, record)
		}
		if seenNative[binding.OpenCodeSessionID] {
			t.Fatalf("root %s reused native session %s", name, binding.OpenCodeSessionID)
		}
		seenNative[binding.OpenCodeSessionID] = true
	}

	if _, err := bridge.SubmitMessage(context.Background(), records["A"].SessionID, bindings["A"].SubagentID, records["A"].Workspace(), opencodenative.TextPrompt("targeted correction A")); err != nil {
		t.Fatalf("targeted A correction: %v", err)
	}
	if err := bridge.Abort(context.Background(), records["B"].SessionID, bindings["B"].SubagentID, records["B"].Workspace()); err != nil {
		t.Fatalf("targeted B cancel: %v", err)
	}
	_, messageCalls, _, abortCalls := harness.counts()
	if messageCalls != 1 || abortCalls != 1 {
		t.Fatalf("targeting escaped requested roots: messages=%d aborts=%d", messageCalls, abortCalls)
	}
	for _, name := range []string{"B", "C"} {
		messages, listErr := bridge.ListMessages(context.Background(), records[name].SessionID, bindings[name].SubagentID, records[name].Workspace())
		if listErr != nil || len(messages) != 0 {
			t.Fatalf("root %s received A correction: messages=%d err=%v", name, len(messages), listErr)
		}
	}
	if status, err := bridge.ObserveStatus(context.Background(), records["C"].SessionID, bindings["C"].SubagentID, records["C"].Workspace()); err != nil || !status.Present || status.OpenCodeSessionID != bindings["C"].OpenCodeSessionID {
		t.Fatalf("C affected by A correction/B cancel: status=%+v err=%v", status, err)
	}

	for name, fn := range map[string]func() error{
		"observe": func() error {
			_, err := bridge.ObserveStatus(context.Background(), records["A"].SessionID, bindings["B"].SubagentID, records["A"].Workspace())
			return err
		},
		"message": func() error {
			_, err := bridge.SubmitMessage(context.Background(), records["A"].SessionID, bindings["B"].SubagentID, records["A"].Workspace(), opencodenative.TextPrompt("deny"))
			return err
		},
		"abort": func() error {
			return bridge.Abort(context.Background(), records["A"].SessionID, bindings["B"].SubagentID, records["A"].Workspace())
		},
	} {
		if err := fn(); ErrorCode(err) != ErrorOwnership {
			t.Fatalf("cross-root %s code=%s err=%v", name, ErrorCode(err), err)
		}
	}

	childA := opencodenative.SessionInfo{ID: "ses-child-a", ParentID: bindings["A"].OpenCodeSessionID, Directory: records["A"].Workspace(), Title: "A child"}
	harness.put(childA)
	if _, _, err := bridge.Attach(context.Background(), records["B"].SessionID, childA.ID, records["B"].Workspace()); ErrorCode(err) != ErrorOwnership {
		t.Fatalf("foreign child attach code=%s err=%v", ErrorCode(err), err)
	}
	if record, err := manager.Get(records["A"].SessionID); err != nil || len(record.NativeSessionBindings) != 1 {
		t.Fatalf("child promoted to root: bindings=%d err=%v", len(record.NativeSessionBindings), err)
	}

	harness.setEventOrder(childA.ID)
	stop := context.Canceled
	if err := bridge.ObserveEvents(context.Background(), records["A"].SessionID, bindings["A"].SubagentID, records["A"].Workspace(), func(event opencodenative.NativeEvent, _ int64) error {
		if nativeEventSessionID(event) != childA.ID {
			t.Fatalf("A observed unexpected event root=%s", nativeEventSessionID(event))
		}
		return stop
	}); err != stop {
		t.Fatalf("A descendant event err=%v", err)
	}
	aCursor, err := manager.NativeSessionBinding(records["A"].SessionID, bindings["A"].SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	bCursor, err := manager.NativeSessionBinding(records["B"].SessionID, bindings["B"].SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	cCursor, err := manager.NativeSessionBinding(records["C"].SessionID, bindings["C"].SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if aCursor.LastEventCursor != 1 || bCursor.LastEventCursor != 0 || cCursor.LastEventCursor != 0 {
		t.Fatalf("per-subagent cursor leaked: A=%d B=%d C=%d", aCursor.LastEventCursor, bCursor.LastEventCursor, cCursor.LastEventCursor)
	}

	restarted, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{},
		IDFactory: func() string { return "must-not-be-used" },
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge2, err := New(Options{Sessions: restarted, Native: client, NativeRuntimeSHA: d3G0NativeRuntimeSHA, NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		recovered, startErr := bridge2.Start(context.Background(), requests[name])
		if startErr != nil || recovered.OpenCodeSessionID != bindings[name].OpenCodeSessionID || recovered.SubagentID != bindings[name].SubagentID {
			t.Fatalf("restart root %s drift: before=%+v after=%+v err=%v", name, bindings[name], recovered, startErr)
		}
	}

	harness.remove(bindings["A"].OpenCodeSessionID)
	if lost, _, err := bridge2.Attach(context.Background(), records["A"].SessionID, bindings["A"].SubagentID, records["A"].Workspace()); ErrorCode(err) != ErrorLost || lost.State != session.NativeConversationLost {
		t.Fatalf("A missing root must become LOST: %+v err=%v", lost, err)
	}
	for _, name := range []string{"B", "C"} {
		attached, info, attachErr := bridge2.Attach(context.Background(), records[name].SessionID, bindings[name].SubagentID, records[name].Workspace())
		if attachErr != nil || attached.State != session.NativeConversationActive || info.ID != bindings[name].OpenCodeSessionID {
			t.Fatalf("LOST A corrupted root %s: binding=%+v info=%+v err=%v", name, attached, info, attachErr)
		}
	}
	if _, err := bridge2.Start(context.Background(), requests["A"]); ErrorCode(err) != ErrorLost {
		t.Fatalf("LOST A must not be replaced/promoted: code=%s err=%v", ErrorCode(err), err)
	}
}
