package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/config"
)

func TestMCPClientSessionStoreTracksBindsAndMasksIdentity(t *testing.T) {
	root := t.TempDir()
	store, err := newMCPClientSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}

	info := MCPClientContext{
		SessionID:       "chatgpt-session-secret-123456789",
		ClientName:      "ChatGPT",
		ClientVersion:   "test",
		ProtocolVersion: "2026-08-11",
	}
	record, err := store.record(info, "server_info", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.ToolCalls != 1 || record.LastTool != "server_info" || record.ActiveCalls != 1 {
		t.Fatalf("record = %#v", record)
	}
	record, err = store.finish(info.SessionID, 125*time.Millisecond, true)
	if err != nil {
		t.Fatal(err)
	}
	if record.ActiveCalls != 0 || record.LastDurationMS < 100 || !record.LastOK || record.LastFinishedAt == "" {
		t.Fatalf("finished record = %#v", record)
	}
	logBytes, err := os.ReadFile(filepath.Join(root, mcpClientActivityLogFileName))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBytes)
	if !strings.Contains(logText, record.SessionKey+" | server_info | OK |") || strings.Contains(logText, info.SessionID) {
		t.Fatalf("activity log = %q", logText)
	}
	if record.SessionKey == "" || strings.Contains(record.SessionKey, info.SessionID) {
		t.Fatalf("session key must be stable and non-secret: %q", record.SessionKey)
	}

	record, err = store.bind(
		info.SessionID,
		"https://chatgpt.com/c/abc123?foo=bar#frag",
		"功能设置说明",
		"wd-test-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := record.ConversationURL, "https://chatgpt.com/c/abc123"; got != want {
		t.Fatalf("ConversationURL = %q, want %q", got, want)
	}
	if record.WatchdogTaskID != "wd-test-1" {
		t.Fatalf("WatchdogTaskID = %q", record.WatchdogTaskID)
	}

	public := publicMCPClientSession(record)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), info.SessionID) {
		t.Fatalf("public session leaked raw id: %s", encoded)
	}
	if public["session_id_masked"] == info.SessionID {
		t.Fatalf("session_id_masked exposed raw id")
	}

	persisted, err := os.ReadFile(filepath.Join(root, mcpClientSessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), info.SessionID) {
		t.Fatalf("persisted state leaked raw opaque session id: %s", persisted)
	}

	reloaded, err := newMCPClientSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.current(info.SessionID)
	if !ok || got.ConversationTitle != "功能设置说明" || got.ToolCalls != 1 {
		t.Fatalf("reloaded record = %#v, ok=%v", got, ok)
	}
}

func TestMCPClientSessionBindRejectsUntrustedConversationURL(t *testing.T) {
	store, err := newMCPClientSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://example.com/c/abc",
		"http://chatgpt.com/c/abc",
		"https://chatgpt.com/",
	} {
		if _, err := store.bind("session-1", raw, "", ""); err == nil {
			t.Fatalf("bind(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestValidateChatGPTConversationURLStrictPathShape(t *testing.T) {
	valid := []string{
		"https://chatgpt.com/c/abc",
		"https://chatgpt.com/c/abc123?foo=bar#frag",
		"https://chatgpt.com/g/proj-1/c/abc",
		"https://chatgpt.com:443/c/abc",
		"HTTPS://ChatGPT.COM/c/AbC",
		"https://chatgpt.com/c/6ac20a9b-ab80-83e8-8497-90f0db2f02c0",
	}
	for _, raw := range valid {
		if err := validateChatGPTConversationURL(raw); err != nil {
			t.Fatalf("validateChatGPTConversationURL(%q) error = %v", raw, err)
		}
	}
	invalid := []string{
		"",
		"https://chatgpt.com/c/",
		"https://chatgpt.com/c",
		"https://chatgpt.com/c//abc",
		"https://chatgpt.com/c/a/extra",
		"https://chatgpt.com/wrong/c/a/extra",
		"https://chatgpt.com/g/a/c",
		"https://chatgpt.com/g/a/c/b/d",
		"https://chatgpt.com/c/a%2Fb",
		"https://chatgpt.com/g/a/c/b%5Cx",
		"https://user:pass@chatgpt.com/c/abc",
		"https://chatgpt.com:8443/c/abc",
		"http://chatgpt.com/c/abc",
		"https://chatgpt.com.evil.example/c/abc",
		"https://chatgpt.com/c/abc\\x",
	}
	for _, raw := range invalid {
		if err := validateChatGPTConversationURL(raw); err == nil {
			t.Fatalf("validateChatGPTConversationURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestCanonicalConversationURLMatchesDashboardExactURLs(t *testing.T) {
	cases := map[string]string{
		"https://chatgpt.com/c/abc?foo=bar#frag":                "https://chatgpt.com/c/abc",
		"https://chatgpt.com/c/abc/":                            "https://chatgpt.com/c/abc",
		"HTTPS://ChatGPT.COM:443/C/AbC?x=1":                     "https://chatgpt.com/C/AbC",
		"https://chatgpt.com/g/proj-1/c/abc":                    "https://chatgpt.com/g/proj-1/c/abc",
		"https://chatgpt.com/c/6ac20a9b-83e8-8497-90f0db2f02c0": "https://chatgpt.com/c/6ac20a9b-83e8-8497-90f0db2f02c0",
	}
	for raw, want := range cases {
		if got := canonicalConversationURL(raw); got != want {
			t.Fatalf("canonicalConversationURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestMCPClientSessionStoreAutoBindIsAtomicAndIdempotent(t *testing.T) {
	store, err := newMCPClientSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// 首次调用：确定性绑定，BoundAt 非空。
	record, err := store.autoBind(MCPClientContext{
		SessionID: "session-1", ConversationURL: "https://chatgpt.com/c/abc?q=1#frag",
		ConversationTitle: "标题", WatchdogTaskID: "wd-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := record.ConversationURL, "https://chatgpt.com/c/abc"; got != want {
		t.Fatalf("ConversationURL = %q, want %q", got, want)
	}
	if record.BoundAt == "" || record.WatchdogTaskID != "wd-1" {
		t.Fatalf("record = %#v", record)
	}
	firstBoundAt := record.BoundAt

	// 幂等：相同 URL 的重复自动绑定保持 BoundAt 稳定。
	time.Sleep(5 * time.Millisecond)
	record, err = store.autoBind(MCPClientContext{
		SessionID: "session-1", ConversationURL: "https://chatgpt.com/c/abc",
		ConversationTitle: "新标题", WatchdogTaskID: "wd-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.BoundAt != firstBoundAt {
		t.Fatalf("BoundAt changed across idempotent autoBind: %q -> %q", firstBoundAt, record.BoundAt)
	}
	if record.ConversationTitle != "新标题" {
		t.Fatalf("title was not refreshed: %#v", record)
	}

	// 冲突 URL 拒绝且不改变既有绑定。
	if _, err := store.autoBind(MCPClientContext{SessionID: "session-1", ConversationURL: "https://chatgpt.com/c/other"}); err == nil {
		t.Fatal("conflicting URL autoBind unexpectedly succeeded")
	}
	// 冲突 task 拒绝。
	if _, err := store.autoBind(MCPClientContext{
		SessionID: "session-1", ConversationURL: "https://chatgpt.com/c/abc", WatchdogTaskID: "wd-2",
	}); err == nil {
		t.Fatal("conflicting task autoBind unexpectedly succeeded")
	}
	// 既有绑定不受冲突调用影响。
	record, _ = store.current("session-1")
	if record.ConversationURL != "https://chatgpt.com/c/abc" || record.WatchdogTaskID != "wd-1" || record.BoundAt != firstBoundAt {
		t.Fatalf("record changed after conflicts: %#v", record)
	}

	// 缺失/无效 URL 保持匿名且不失败。
	if _, err := store.autoBind(MCPClientContext{SessionID: "session-1", ConversationURL: "http://evil.example/c/x"}); err != nil {
		t.Fatalf("invalid URL autoBind failed: %v", err)
	}
	if _, err := store.autoBind(MCPClientContext{SessionID: "session-1"}); err != nil {
		t.Fatalf("empty URL autoBind failed: %v", err)
	}

	// 同一 URL 的并发自动绑定不会撕裂状态。
	store2, err := newMCPClientSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const concurrent = 16
	start := make(chan struct{})
	errs := make(chan error, concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			<-start
			_, bindErr := store2.autoBind(MCPClientContext{
				SessionID: "session-race", ConversationURL: "https://chatgpt.com/c/race",
			})
			errs <- bindErr
		}()
	}
	close(start)
	for i := 0; i < concurrent; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent autoBind error: %v", err)
		}
	}
	record, ok := store2.current("session-race")
	if !ok || record.ConversationURL != "https://chatgpt.com/c/race" || record.BoundAt == "" {
		t.Fatalf("raced record = %#v, ok=%v", record, ok)
	}
}

func TestMCPClientSessionRuntimeUsesProtocolContext(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		AgentDockDefaultDir: root,
		AgentDockHome:       filepath.Join(root, ".agentdock"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{
		SessionID:     "session-runtime-1",
		ClientName:    "ChatGPT",
		ClientVersion: "test",
	})
	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-runtime-1"}, "server_info", "req-1"); err != nil {
		t.Fatal(err)
	}

	bound, err := rt.mcpSessionBind(ctx, map[string]any{
		"conversation_url":   "https://chatgpt.com/c/runtime-1",
		"conversation_title": "Runtime test",
		"watchdog_task_id":   "wd-runtime-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := bound["session"].(Result)
	if session["watchdog_task_id"] != "wd-runtime-1" {
		t.Fatalf("bound session = %#v", session)
	}

	current, err := rt.mcpSessionCurrent(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	currentSession := current["session"].(Result)
	if currentSession["conversation_url"] != "https://chatgpt.com/c/runtime-1" {
		t.Fatalf("current session = %#v", currentSession)
	}

	listed, err := rt.mcpSessionList(context.Background(), map[string]any{
		"active_within_seconds": 60,
		"limit":                 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if listed["count"] != 1 {
		t.Fatalf("listed = %#v", listed)
	}

	if _, err := rt.mcpSessionCurrent(context.Background(), nil); err == nil {
		t.Fatal("mcpSessionCurrent without protocol context unexpectedly succeeded")
	}

	// An old record should be excluded by the active filter.
	rt.mcpSessions.mu.Lock()
	key := mcpSessionKey("session-runtime-1")
	record := rt.mcpSessions.sessions[key]
	record.LastSeenAt = time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	rt.mcpSessions.sessions[key] = record
	rt.mcpSessions.mu.Unlock()

	listed, err = rt.mcpSessionList(context.Background(), map[string]any{
		"active_within_seconds": 60,
		"limit":                 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if listed["count"] != 0 {
		t.Fatalf("active filter returned stale session: %#v", listed)
	}
}
