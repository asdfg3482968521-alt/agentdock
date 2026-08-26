package nativebridge

import (
	"context"
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

type multiturnFixture struct {
	root    string
	bridge  *Manager
	store   *session.Manager
	client  *opencodenative.Client
	harness *nativeHarness
	record  session.Record
	binding session.NativeSessionBinding
}

func newMultiturnFixture(t *testing.T) multiturnFixture {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(root, "sessions"), WorktreeRoot: filepath.Join(root, "worktrees"), Git: testGit{},
		IDFactory: func() string { return "agentdock-session-f1" },
		Now:       func() time.Time { return time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Open(context.Background(), session.OpenRequest{ProjectID: "d3-f1", RepoRoot: workspace, PermissionMode: "project"})
	if err != nil {
		t.Fatal(err)
	}
	harness := newNativeHarness()
	server := httptest.NewServer(http.HandlerFunc(harness.handler))
	t.Cleanup(server.Close)
	client, err := opencodenative.NewClient(opencodenative.Config{
		BaseURL: server.URL, ReadinessTimeout: time.Second, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := New(Options{
		Sessions: store, Native: client, SubagentIDFactory: func() string { return "subagent-f1" },
		NativeRuntimeSHA: "runtime-f1", NativeServerIdentity: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bridge.Start(context.Background(), StartRequest{
		AgentDockSessionID: record.SessionID, ClientRequestID: "logical-start-f1", Workspace: record.Workspace(),
		Provider: "opencode", Model: "test-model", ExecutionMode: "native-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	return multiturnFixture{root: root, bridge: bridge, store: store, client: client, harness: harness, record: record, binding: binding}
}

func (f multiturnFixture) restartedBridge(t *testing.T) (*session.Manager, *Manager) {
	t.Helper()
	store, err := session.NewManager(context.Background(), session.Config{
		StorageRoot: filepath.Join(f.root, "sessions"), WorktreeRoot: filepath.Join(f.root, "worktrees"), Git: testGit{},
		IDFactory: func() string { return "unused-after-restart" },
		Now:       func() time.Time { return time.Date(2026, 8, 26, 4, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := New(Options{
		Sessions: store, Native: f.client, SubagentIDFactory: func() string { return "must-not-create-replacement" },
		NativeRuntimeSHA: "runtime-f1", NativeServerIdentity: "restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, bridge
}

func (f multiturnFixture) request(clientID, kind, text string) SupervisorMessageRequest {
	return SupervisorMessageRequest{
		AgentDockSessionID: f.record.SessionID,
		SubagentID:         f.binding.SubagentID,
		Workspace:          f.record.Workspace(),
		ClientMessageID:    clientID,
		Kind:               kind,
		Prompt:             opencodenative.TextPrompt(text),
	}
}

func TestSupervisorMessageSequentialDuplicate(t *testing.T) {
	f := newMultiturnFixture(t)
	request := f.request("client-dup", "TASK", "task A")
	first, err := f.bridge.SubmitSupervisorMessage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || !first.Dispatched || first.Message.MessageSeq != 1 || first.Message.SupervisorMessageID == "" {
		t.Fatalf("unexpected first result: %+v", first)
	}
	for i := 0; i < 10; i++ {
		duplicate, err := f.bridge.SubmitSupervisorMessage(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if duplicate.Created || duplicate.Dispatched || duplicate.Message.SupervisorMessageID != first.Message.SupervisorMessageID || duplicate.Message.MessageSeq != first.Message.MessageSeq {
			t.Fatalf("duplicate %d changed identity or redispatched: first=%+v duplicate=%+v", i, first, duplicate)
		}
	}
	_, _, async, _ := f.harness.counts()
	_, _, prompts := f.harness.supervisorTrace()
	if async != 1 || len(prompts) != 1 || prompts[0].MessageID != first.Message.SupervisorMessageID {
		t.Fatalf("duplicate protection/native MessageID mismatch: async=%d prompts=%+v first=%+v", async, prompts, first)
	}
}

func TestSupervisorMessageConcurrentDuplicate(t *testing.T) {
	f := newMultiturnFixture(t)
	request := f.request("client-concurrent-dup", "TASK", "task concurrent")
	const n = 32
	results := make(chan SupervisorMessageResult, n)
	errs := make(chan error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			result, err := f.bridge.SubmitSupervisorMessage(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first SupervisorMessageResult
	createdCount := 0
	dispatchedCount := 0
	count := 0
	for result := range results {
		count++
		if result.Created {
			createdCount++
		}
		if result.Dispatched {
			dispatchedCount++
		}
		if first.Message.SupervisorMessageID == "" {
			first = result
			continue
		}
		if result.Message.SupervisorMessageID != first.Message.SupervisorMessageID || result.Message.MessageSeq != first.Message.MessageSeq {
			t.Fatalf("concurrent duplicate diverged: first=%+v got=%+v", first, result)
		}
	}
	_, _, async, _ := f.harness.counts()
	if count != n || createdCount != 1 || dispatchedCount != 1 || async != 1 {
		t.Fatalf("concurrent duplicate exactly-once failed: count=%d created=%d dispatched=%d async=%d", count, createdCount, dispatchedCount, async)
	}
}

func TestSupervisorMessageConcurrentDistinct(t *testing.T) {
	f := newMultiturnFixture(t)
	requests := []SupervisorMessageRequest{
		f.request("client-alpha", "FOLLOWUP", "alpha"),
		f.request("client-beta", "CORRECTION", "beta"),
	}
	results := make(chan SupervisorMessageResult, len(requests))
	errs := make(chan error, len(requests))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, request := range requests {
		request := request
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := f.bridge.SubmitSupervisorMessage(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ordered := make([]SupervisorMessageResult, 0, len(requests))
	for result := range results {
		ordered = append(ordered, result)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Message.MessageSeq < ordered[j].Message.MessageSeq })
	if len(ordered) != 2 || ordered[0].Message.MessageSeq != 1 || ordered[1].Message.MessageSeq != 2 {
		t.Fatalf("distinct messages did not linearize: %+v", ordered)
	}
	_, _, prompts := f.harness.supervisorTrace()
	if len(prompts) != 2 || prompts[0].MessageID != ordered[0].Message.SupervisorMessageID || prompts[1].MessageID != ordered[1].Message.SupervisorMessageID {
		t.Fatalf("native dispatch order does not match durable order: ordered=%+v prompts=%+v", ordered, prompts)
	}
	if ordered[0].OpenCodeSessionID != f.binding.OpenCodeSessionID || ordered[1].OpenCodeSessionID != f.binding.OpenCodeSessionID {
		t.Fatalf("distinct messages changed root session: root=%s ordered=%+v", f.binding.OpenCodeSessionID, ordered)
	}
}
