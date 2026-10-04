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
