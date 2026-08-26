package nativebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/opencodenative"
	"github.com/uvwt/agentdock/internal/session"
)

type testGit struct{}

func (testGit) DetectRepository(_ context.Context, path string) (string, bool, error) {
	return path, false, nil
}
func (testGit) ResolveBaseRef(context.Context, string, string) (string, string, error) {
	return "", "", nil
}
func (testGit) AddWorktree(context.Context, string, string, string, string) error { return nil }
func (testGit) CurrentBranch(context.Context, string) (string, error)             { return "", nil }
func (testGit) IsDirty(context.Context, string) (bool, error)                     { return false, nil }

type failOnceStore struct {
	*session.Manager
	mu   sync.Mutex
	fail bool
}

func (s *failOnceStore) BindNativeSession(sessionID string, binding session.NativeSessionBinding) (session.NativeSessionBinding, error) {
	s.mu.Lock()
	if s.fail {
		s.fail = false
		s.mu.Unlock()
		return session.NativeSessionBinding{}, errors.New("injected AgentDock persistence failure")
	}
	s.mu.Unlock()
	return s.Manager.BindNativeSession(sessionID, binding)
}

type nativeHarness struct {
	mu                sync.Mutex
	sessions          map[string]opencodenative.SessionInfo
	messages          map[string][]opencodenative.Message
	createCalls       int
	messageCalls      int
	asyncCalls        int
	abortCalls        int
	eventOrder        []string
	controlOrder      []string
	asyncPrompts      []opencodenative.PromptRequest
	permissions       []opencodenative.PermissionRequest
	permissionReplies []opencodenative.PermissionReplyRequest
}

func newNativeHarness() *nativeHarness {
	return &nativeHarness{sessions: map[string]opencodenative.SessionInfo{}, messages: map[string][]opencodenative.Message{}}
}

func (h *nativeHarness) handler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		http.Error(w, "directory required", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	write := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}

	if r.Method == http.MethodGet && r.URL.Path == "/session/status" {
		result := map[string]any{}
		for id, info := range h.sessions {
			if samePath(info.Directory, directory) {
				result[id] = map[string]any{"type": "idle"}
			}
		}
		write(result)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/provider" {
		write(map[string]any{
			"all": []any{map[string]any{
				"id": "jiyuan", "name": "Jiyuan", "source": "config",
				"models": map[string]any{"deepseek-v4-flash-0731": map[string]any{"id": "deepseek-v4-flash-0731", "providerID": "jiyuan", "name": "DeepSeek V4 Flash", "status": "active"}},
			}},
			"default": map[string]any{"jiyuan": "deepseek-v4-flash-0731"}, "connected": []string{"jiyuan"},
		})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/config" {
		write(map[string]any{"model": "jiyuan/deepseek-v4-flash-0731"})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/permission" {
		write(h.permissions)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/permission/") && strings.HasSuffix(r.URL.Path, "/reply") {
		var reply opencodenative.PermissionReplyRequest
		if err := json.NewDecoder(r.Body).Decode(&reply); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.permissionReplies = append(h.permissionReplies, reply)
		write(true)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/event" {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"evt-other\",\"type\":\"message.updated\",\"properties\":{\"sessionID\":\"ses-other\"}}\n\n")
		ids := append([]string(nil), h.eventOrder...)
		if len(ids) == 0 {
			for id, info := range h.sessions {
				if samePath(info.Directory, directory) {
					ids = append(ids, id)
					break
				}
			}
		}
		for index, id := range ids {
			info, ok := h.sessions[id]
			if !ok || !samePath(info.Directory, directory) {
				continue
			}
			fmt.Fprintf(w, "data: {\"id\":\"evt-owned-%d\",\"type\":\"message.updated\",\"properties\":{\"sessionID\":%q}}\n\n", index+1, id)
		}
		return
	}
	if r.URL.Path == "/session" {
		switch r.Method {
		case http.MethodGet:
			items := []opencodenative.SessionInfo{}
			for _, info := range h.sessions {
				if samePath(info.Directory, directory) {
					items = append(items, info)
				}
			}
			write(items)
			return
		case http.MethodPost:
			var request opencodenative.CreateSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			h.createCalls++
			id := fmt.Sprintf("ses-native-%d", h.createCalls)
			info := opencodenative.SessionInfo{ID: id, Directory: directory, Title: request.Title, Metadata: request.Metadata}
			h.sessions[id] = info
			write(info)
			return
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "session" {
		id := parts[1]
		info, ok := h.sessions[id]
		if !ok || !samePath(info.Directory, directory) {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			write(info)
			return
		}
		if len(parts) == 3 && parts[2] == "message" {
			switch r.Method {
			case http.MethodGet:
				write(h.messages[id])
				return
			case http.MethodPost:
				h.messageCalls++
				msg := opencodenative.Message{Info: opencodenative.MessageInfo{ID: fmt.Sprintf("msg-%d", h.messageCalls), SessionID: id, Role: "assistant"}, Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"bridge-ok"}`)}}
				h.messages[id] = append(h.messages[id], msg)
				write(msg)
				return
			}
		}
		if len(parts) == 3 && parts[2] == "prompt_async" && r.Method == http.MethodPost {
			h.asyncCalls++
			var prompt opencodenative.PromptRequest
			if err := json.NewDecoder(r.Body).Decode(&prompt); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			h.asyncPrompts = append(h.asyncPrompts, prompt)
			h.controlOrder = append(h.controlOrder, "async:"+id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(parts) == 3 && parts[2] == "abort" && r.Method == http.MethodPost {
			h.abortCalls++
			h.controlOrder = append(h.controlOrder, "abort:"+id)
			write(true)
			return
		}
	}
	http.NotFound(w, r)
}

func (h *nativeHarness) counts() (create, message, async, abort int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.createCalls, h.messageCalls, h.asyncCalls, h.abortCalls
}
func (h *nativeHarness) remove(id string) { h.mu.Lock(); defer h.mu.Unlock(); delete(h.sessions, id) }
func (h *nativeHarness) put(info opencodenative.SessionInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[info.ID] = info
}
func (h *nativeHarness) setEventOrder(ids ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.eventOrder = append([]string(nil), ids...)
}
func (h *nativeHarness) setPermissions(items ...opencodenative.PermissionRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.permissions = append([]opencodenative.PermissionRequest(nil), items...)
}
func (h *nativeHarness) supervisorTrace() (order []string, replies []opencodenative.PermissionReplyRequest, prompts []opencodenative.PromptRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.controlOrder...), append([]opencodenative.PermissionReplyRequest(nil), h.permissionReplies...), append([]opencodenative.PromptRequest(nil), h.asyncPrompts...)
}

func TestBridgeDurableReconciliationRestartIsolationAndLost(t *testing.T) {
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	mismatch := filepath.Join(root, "mismatch")
	for _, path := range []string{repoA, repoB, mismatch} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"agentdock-session-a", "agentdock-session-b"}
	idx := 0
	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{},
		IDFactory: func() string { value := ids[idx]; idx++; return value }, Now: func() time.Time { return time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3a", RepoRoot: repoA, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3b", RepoRoot: repoB, PermissionMode: "full"})
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
	store := &failOnceStore{Manager: manager, fail: true}
	bridge, err := New(Options{Sessions: store, Native: client, SubagentIDFactory: func() string { return "subagent-root-a" }, NativeRuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{AgentDockSessionID: a.SessionID, ClientRequestID: "logical-start-001", Workspace: a.Workspace(), Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session"}

	if _, err := bridge.Start(context.Background(), request); ErrorCode(err) != ErrorReconciliation {
		t.Fatalf("injected bind failure should surface reconciliation error: %v", err)
	}
	create, _, _, _ := harness.counts()
	if create != 1 {
		t.Fatalf("first logical start should create exactly once, got %d", create)
	}
	if _, _, err := manager.NativeSessionBindingByRequest(a.SessionID, request.ClientRequestID); err != nil {
		t.Fatal(err)
	}

	binding, err := bridge.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if binding.OpenCodeSessionID != "ses-native-1" || binding.SubagentID != "subagent-root-a" {
		t.Fatalf("reconciliation did not preserve exact native identity: %+v", binding)
	}
	create, _, _, _ = harness.counts()
	if create != 1 {
		t.Fatalf("retry created duplicate native session: %d", create)
	}
	again, err := bridge.Start(context.Background(), request)
	if err != nil || again.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("idempotent repeat failed: %+v %v", again, err)
	}
	create, _, _, _ = harness.counts()
	if create != 1 {
		t.Fatalf("idempotent repeat created duplicate: %d", create)
	}

	restarted, err := session.NewManager(context.Background(), session.Config{StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{}, IDFactory: func() string { return "unused" }, Now: func() time.Time { return time.Date(2026, 8, 26, 1, 3, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	bridge2, err := New(Options{Sessions: restarted, Native: client, SubagentIDFactory: func() string { return "must-not-be-used" }, NativeRuntimeSHA: binding.NativeRuntimeSHA, NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := bridge2.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("restart mapping changed native session id: before=%s after=%s", binding.OpenCodeSessionID, recovered.OpenCodeSessionID)
	}
	create, _, _, _ = harness.counts()
	if create != 1 {
		t.Fatalf("restart created replacement native session: %d", create)
	}
	attached, info, err := bridge2.Attach(context.Background(), a.SessionID, binding.SubagentID, a.Workspace())
	if err != nil || info.ID != binding.OpenCodeSessionID || attached.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("restart attach failed: %+v %+v %v", attached, info, err)
	}

	message, err := bridge2.SubmitMessage(context.Background(), a.SessionID, binding.SubagentID, a.Workspace(), opencodenative.TextPrompt("hello"))
	if err != nil || message.Info.ID == "" {
		t.Fatalf("message bridge failed: %+v %v", message, err)
	}
	messages, err := bridge2.ListMessages(context.Background(), a.SessionID, binding.SubagentID, a.Workspace())
	if err != nil || len(messages) != 1 {
		t.Fatalf("message readback failed: %+v %v", messages, err)
	}
	status, err := bridge2.ObserveStatus(context.Background(), a.SessionID, binding.SubagentID, a.Workspace())
	if err != nil || !status.Present {
		t.Fatalf("status bridge failed: %+v %v", status, err)
	}
	if err := bridge2.Abort(context.Background(), a.SessionID, binding.SubagentID, a.Workspace()); err != nil {
		t.Fatalf("abort failed: %v", err)
	}
	if err := bridge2.ContinueConversation(context.Background(), a.SessionID, binding.SubagentID, a.Workspace(), opencodenative.TextPrompt("same session follow-up")); err != nil {
		t.Fatalf("continue failed: %v", err)
	}
	if _, err := bridge2.ProviderModelMetadata(context.Background(), a.SessionID, binding.SubagentID, a.Workspace()); err != nil {
		t.Fatalf("provider metadata failed: %v", err)
	}
	discovery, err := bridge2.ModelDiscovery(context.Background(), a.SessionID, binding.SubagentID, a.Workspace())
	if err != nil || discovery.Configured != (opencodenative.ModelRef{ProviderID: "jiyuan", ModelID: "deepseek-v4-flash-0731"}) || !discovery.ConfiguredAvailable {
		t.Fatalf("model discovery bridge failed: %+v err=%v", discovery, err)
	}
	_, messageCalls, asyncCalls, abortCalls := harness.counts()
	if messageCalls != 1 || asyncCalls != 1 || abortCalls != 1 {
		t.Fatalf("primitive counts message=%d async=%d abort=%d", messageCalls, asyncCalls, abortCalls)
	}

	stop := errors.New("owned event observed")
	var cursor int64
	var eventID string
	err = bridge2.ObserveEvents(context.Background(), a.SessionID, binding.SubagentID, a.Workspace(), func(event opencodenative.NativeEvent, next int64) error {
		cursor = next
		eventID = event.ID
		return stop
	})
	if !errors.Is(err, stop) || cursor != 1 || eventID != "evt-owned-1" {
		t.Fatalf("event filter/cursor mismatch cursor=%d id=%q err=%v", cursor, eventID, err)
	}
	cursorBinding, err := restarted.NativeSessionBinding(a.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if cursorBinding.LastEventCursor != 1 || cursorBinding.LastNativeEventID != "evt-owned-1" {
		t.Fatalf("durable AgentDock cursor not updated: %+v", cursorBinding)
	}

	for name, fn := range map[string]func() error{
		"attach": func() error {
			_, _, err := bridge2.Attach(context.Background(), b.SessionID, binding.SubagentID, b.Workspace())
			return err
		},
		"observe": func() error {
			_, err := bridge2.ObserveStatus(context.Background(), b.SessionID, binding.SubagentID, b.Workspace())
			return err
		},
		"message": func() error {
			_, err := bridge2.SubmitMessage(context.Background(), b.SessionID, binding.SubagentID, b.Workspace(), opencodenative.TextPrompt("denied"))
			return err
		},
		"abort": func() error {
			return bridge2.Abort(context.Background(), b.SessionID, binding.SubagentID, b.Workspace())
		},
	} {
		if err := fn(); ErrorCode(err) != ErrorOwnership {
			t.Fatalf("cross-session %s should be ownership denied: %v code=%s", name, err, ErrorCode(err))
		}
	}
	if _, _, err := bridge2.Attach(context.Background(), a.SessionID, binding.SubagentID, mismatch); ErrorCode(err) != ErrorWorkspace {
		t.Fatalf("workspace mismatch should be denied: %v", err)
	}

	harness.remove(binding.OpenCodeSessionID)
	createBefore, _, _, _ := harness.counts()
	lost, _, err := bridge2.Attach(context.Background(), a.SessionID, binding.SubagentID, a.Workspace())
	if ErrorCode(err) != ErrorLost || lost.State != session.NativeConversationLost {
		t.Fatalf("missing native session should become LOST: %+v err=%v", lost, err)
	}
	if _, err := bridge2.Start(context.Background(), request); ErrorCode(err) != ErrorLost {
		t.Fatalf("LOST mapping should refuse replacement start: %v", err)
	}
	createAfter, _, _, _ := harness.counts()
	if createAfter != createBefore {
		t.Fatalf("LOST mapping silently created replacement: before=%d after=%d", createBefore, createAfter)
	}
}

func TestConcurrentLogicalStartCreatesExactlyOneNativeSession(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{}, IDFactory: func() string { return "agentdock-concurrent" },
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "d3-concurrent", RepoRoot: repo, PermissionMode: "project"})
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
	bridge, err := New(Options{Sessions: manager, Native: client, SubagentIDFactory: func() string { return "subagent-concurrent" }, NativeRuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{AgentDockSessionID: record.SessionID, ClientRequestID: "same-concurrent-request", Workspace: record.Workspace(), Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session"}

	const callers = 16
	results := make(chan session.NativeSessionBinding, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			binding, err := bridge.Start(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			results <- binding
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent start failed: %v", err)
	}
	var nativeID string
	count := 0
	for binding := range results {
		count++
		if nativeID == "" {
			nativeID = binding.OpenCodeSessionID
		}
		if binding.OpenCodeSessionID != nativeID {
			t.Fatalf("concurrent logical start split native identity: first=%s got=%s", nativeID, binding.OpenCodeSessionID)
		}
	}
	if count != callers {
		t.Fatalf("results=%d want=%d", count, callers)
	}
	create, _, _, _ := harness.counts()
	if create != 1 {
		t.Fatalf("concurrent duplicate start created %d native sessions", create)
	}
}

func TestRootOnlyReconciliationIgnoresChildCandidate(t *testing.T) {
	record := session.Record{SessionID: "agentdock-root", RepoRoot: `C:\repo`, PermissionMode: "project"}
	request := StartRequest{AgentDockSessionID: record.SessionID, ClientRequestID: "request-1"}
	metadata := map[string]any{
		metadataSubagentID: "subagent-root", metadataSessionID: record.SessionID,
		metadataClientRequestID: request.ClientRequestID, metadataRuntimeScope: runtimeScope(record.SessionID),
		metadataPermissionMode: record.PermissionMode,
	}
	root := opencodenative.SessionInfo{ID: "ses-root", Directory: record.RepoRoot, Metadata: metadata}
	child := opencodenative.SessionInfo{ID: "ses-child", ParentID: root.ID, Directory: record.RepoRoot, Metadata: metadata}

	got, found, err := reconcileNativeSession([]opencodenative.SessionInfo{child, root}, record, request)
	if err != nil || !found || got.ID != root.ID {
		t.Fatalf("root-only reconciliation selected wrong identity: got=%+v found=%v err=%v", got, found, err)
	}
	if _, found, err := reconcileNativeSession([]opencodenative.SessionInfo{child}, record, request); err != nil || found {
		t.Fatalf("child-only reconciliation must not recover or promote child: found=%v err=%v", found, err)
	}
}

func TestDescendantOwnershipClosureEventsRestartAndIsolation(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	foreignRepo := filepath.Join(root, "foreign")
	for _, path := range []string{repo, foreignRepo} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"agentdock-owner", "agentdock-foreign"}
	index := 0
	manager, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{},
		IDFactory: func() string { value := ids[index]; index++; return value },
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "owner", RepoRoot: repo, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := manager.Open(context.Background(), session.OpenRequest{ProjectID: "foreign", RepoRoot: foreignRepo, PermissionMode: "project"})
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
	bridge, err := New(Options{Sessions: manager, Native: client, SubagentIDFactory: func() string { return "subagent-owner" }, NativeRuntimeSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247", NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bridge.Start(context.Background(), StartRequest{AgentDockSessionID: owner.SessionID, ClientRequestID: "owner-start", Workspace: owner.Workspace(), Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session"})
	if err != nil {
		t.Fatal(err)
	}

	child := opencodenative.SessionInfo{ID: "ses-child", ParentID: binding.OpenCodeSessionID, Directory: owner.Workspace(), Title: "native task child"}
	grandchild := opencodenative.SessionInfo{ID: "ses-grandchild", ParentID: child.ID, Directory: owner.Workspace(), Title: "native task grandchild"}
	orphan := opencodenative.SessionInfo{ID: "ses-orphan", ParentID: "ses-missing-parent", Directory: owner.Workspace(), Title: "orphan"}
	harness.put(child)
	harness.put(grandchild)
	harness.put(orphan)
	harness.setEventOrder(orphan.ID, child.ID, grandchild.ID)

	persisted, err := manager.NativeSessionBinding(owner.SessionID, binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Get(owner.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.NativeSessionBindings) != 1 || persisted.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("child must not become top-level mapping: bindings=%d root=%s persisted=%s", len(record.NativeSessionBindings), binding.OpenCodeSessionID, persisted.OpenCodeSessionID)
	}

	otherRoot, err := bridge.Start(context.Background(), StartRequest{
		AgentDockSessionID: owner.SessionID, ClientRequestID: "other-root-start", SubagentID: "subagent-other-root",
		Workspace: owner.Workspace(), Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.setEventOrder(child.ID, otherRoot.OpenCodeSessionID)
	stopOther := errors.New("other root observed")
	var otherObserved string
	err = bridge.ObserveEvents(context.Background(), owner.SessionID, otherRoot.SubagentID, owner.Workspace(), func(event opencodenative.NativeEvent, _ int64) error {
		otherObserved = nativeEventSessionID(event)
		return stopOther
	})
	if !errors.Is(err, stopOther) || otherObserved != otherRoot.OpenCodeSessionID {
		t.Fatalf("different root subagent observed foreign descendant: observed=%q want-root=%q err=%v", otherObserved, otherRoot.OpenCodeSessionID, err)
	}
	harness.setEventOrder(orphan.ID, child.ID, grandchild.ID)

	stop := errors.New("descendant observed")
	var observedSession string
	var observedEvent string
	err = bridge.ObserveEvents(context.Background(), owner.SessionID, binding.SubagentID, owner.Workspace(), func(event opencodenative.NativeEvent, _ int64) error {
		observedSession = nativeEventSessionID(event)
		observedEvent = event.ID
		return stop
	})
	if !errors.Is(err, stop) || observedSession != child.ID || observedEvent != "evt-owned-2" {
		t.Fatalf("root owner did not receive descendant telemetry or orphan was promoted: session=%q event=%q err=%v", observedSession, observedEvent, err)
	}

	beforeCreate, beforeMessage, beforeAsync, beforeAbort := harness.counts()
	for name, fn := range map[string]func() error{
		"owner-attach-child-id-as-token": func() error {
			_, _, err := bridge.Attach(context.Background(), owner.SessionID, child.ID, owner.Workspace())
			return err
		},
		"owner-observe-child-id-as-token": func() error {
			_, err := bridge.ObserveStatus(context.Background(), owner.SessionID, child.ID, owner.Workspace())
			return err
		},
		"owner-message-child-id-as-token": func() error {
			_, err := bridge.SubmitMessage(context.Background(), owner.SessionID, child.ID, owner.Workspace(), opencodenative.TextPrompt("denied"))
			return err
		},
		"owner-abort-child-id-as-token": func() error { return bridge.Abort(context.Background(), owner.SessionID, child.ID, owner.Workspace()) },
		"attach-child-id-as-subagent": func() error {
			_, _, err := bridge.Attach(context.Background(), foreign.SessionID, child.ID, foreign.Workspace())
			return err
		},
		"observe-child-id-as-subagent": func() error {
			_, err := bridge.ObserveStatus(context.Background(), foreign.SessionID, child.ID, foreign.Workspace())
			return err
		},
		"message-child-id-as-subagent": func() error {
			_, err := bridge.SubmitMessage(context.Background(), foreign.SessionID, child.ID, foreign.Workspace(), opencodenative.TextPrompt("denied"))
			return err
		},
		"abort-child-id-as-subagent": func() error {
			return bridge.Abort(context.Background(), foreign.SessionID, child.ID, foreign.Workspace())
		},
	} {
		if err := fn(); ErrorCode(err) != ErrorOwnership {
			t.Fatalf("foreign descendant %s must be denied before native control: err=%v code=%s", name, err, ErrorCode(err))
		}
	}
	afterCreate, afterMessage, afterAsync, afterAbort := harness.counts()
	if beforeCreate != afterCreate || beforeMessage != afterMessage || beforeAsync != afterAsync || beforeAbort != afterAbort {
		t.Fatalf("foreign descendant attempt reached native control plane: before=%d/%d/%d/%d after=%d/%d/%d/%d", beforeCreate, beforeMessage, beforeAsync, beforeAbort, afterCreate, afterMessage, afterAsync, afterAbort)
	}

	restarted, err := session.NewManager(context.Background(), session.Config{StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{}, IDFactory: func() string { return "unused" }})
	if err != nil {
		t.Fatal(err)
	}
	bridge2, err := New(Options{Sessions: restarted, Native: client, SubagentIDFactory: func() string { return "must-not-be-used" }, NativeRuntimeSHA: binding.NativeRuntimeSHA, NativeServerIdentity: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := bridge2.Start(context.Background(), StartRequest{AgentDockSessionID: owner.SessionID, ClientRequestID: "owner-start", Workspace: owner.Workspace(), Provider: "opencode", Model: "mimo-v2.5-free", ExecutionMode: "native-session"})
	if err != nil || recovered.OpenCodeSessionID != binding.OpenCodeSessionID {
		t.Fatalf("restart root mapping drifted/replaced by child: before=%s after=%s err=%v", binding.OpenCodeSessionID, recovered.OpenCodeSessionID, err)
	}
	createAfterRestart, _, _, _ := harness.counts()
	if createAfterRestart != beforeCreate {
		t.Fatalf("restart created replacement root in presence of descendants: before=%d after=%d", beforeCreate, createAfterRestart)
	}
	harness.setEventOrder(child.ID)
	observedSession = ""
	err = bridge2.ObserveEvents(context.Background(), owner.SessionID, binding.SubagentID, owner.Workspace(), func(event opencodenative.NativeEvent, _ int64) error {
		observedSession = nativeEventSessionID(event)
		return stop
	})
	if !errors.Is(err, stop) || observedSession != child.ID {
		t.Fatalf("restart lost descendant relationship: session=%q err=%v", observedSession, err)
	}

	harness.remove(binding.OpenCodeSessionID)
	if lost, _, err := bridge2.Attach(context.Background(), owner.SessionID, binding.SubagentID, owner.Workspace()); ErrorCode(err) != ErrorLost || lost.State != session.NativeConversationLost {
		t.Fatalf("missing root with surviving child must become LOST, not promote child: lost=%+v err=%v", lost, err)
	}
	createBeforeLostRetry, _, _, _ := harness.counts()
	if _, err := bridge2.Start(context.Background(), StartRequest{AgentDockSessionID: owner.SessionID, ClientRequestID: "owner-start", Workspace: owner.Workspace()}); ErrorCode(err) != ErrorLost {
		t.Fatalf("LOST root retry should refuse child replacement: %v", err)
	}
	createAfterLostRetry, _, _, _ := harness.counts()
	if createAfterLostRetry != createBeforeLostRetry {
		t.Fatalf("LOST root retry created replacement while child survived: before=%d after=%d", createBeforeLostRetry, createAfterLostRetry)
	}
}
