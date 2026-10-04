package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/config"
)

// fakeDashboardState serves the live dashboard 2.3.0 contract: /api/overview
// returns sessions/active_tasks arrays and task entries carrying target_url,
// target_title, state, running, pid_alive and checkpoint/resumable flags;
// POST /api/action requires X-Watchdog-Token and replies {"ok":true,...}.
type fakeDashboardState struct {
	mu               sync.Mutex
	token            string
	html             string
	tasks            []map[string]any
	active           []map[string]any
	actions          []string
	posts            []map[string]any
	okOverride       func(action string) (ok bool, message string)
	redirectTo       string
	skipMutation     bool
	mutate           func(action, taskID string, task map[string]any)
	overviewCalls    int
	failOverviewFrom int
}

func (s *fakeDashboardState) task(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskLocked(id)
}

func (s *fakeDashboardState) taskLocked(id string) map[string]any {
	for _, task := range append(append([]map[string]any{}, s.active...), s.tasks...) {
		if task["task_id"] == id {
			return task
		}
	}
	return nil
}

func (s *fakeDashboardState) setTask(id string, patch map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range []*[]map[string]any{&s.active, &s.tasks} {
		for _, task := range *list {
			if task["task_id"] == id {
				for key, value := range patch {
					task[key] = value
				}
				return
			}
		}
	}
	entry := map[string]any{"task_id": id}
	for key, value := range patch {
		entry[key] = value
	}
	s.tasks = append(s.tasks, entry)
}

func (s *fakeDashboardState) addActive(task map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = append(s.active, task)
}

func newFakeDashboard(t *testing.T) (*httptest.Server, *fakeDashboardState) {
	t.Helper()
	state := &fakeDashboardState{
		token: "wd-token-abc123",
		html:  "<!doctype html><html><head></head><body><script>var TOKEN='wd-token-abc123';</script></body></html>",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		redirectTo := state.redirectTo
		html := state.html
		state.mu.Unlock()
		if redirectTo != "" {
			http.Redirect(w, r, redirectTo, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/api/overview", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.overviewCalls++
		if state.failOverviewFrom > 0 && state.overviewCalls >= state.failOverviewFrom {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false,"error":"overview failed"}`))
			return
		}
		payload := map[string]any{
			"sessions":     []map[string]any{},
			"active_tasks": state.active,
			"tasks":        state.tasks,
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	mux.HandleFunc("/api/action", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if r.Header.Get("X-Watchdog-Token") != state.token {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"ok":false,"error":"Invalid dashboard token"}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		action, _ := body["action"].(string)
		taskID, _ := body["task_id"].(string)
		state.actions = append(state.actions, action)
		state.posts = append(state.posts, body)
		if state.okOverride != nil {
			ok, message := state.okOverride(action)
			if !ok {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"ok":false,"error":"` + message + `"}`))
				return
			}
		}
		// 模拟 dashboard control() 的状态迁移。
		if !state.skipMutation {
			if task := state.taskLocked(taskID); task != nil {
				if state.mutate != nil {
					state.mutate(action, taskID, task)
				} else {
					switch action {
					case "pause":
						task["state"] = "PAUSED"
						task["running"] = true
						task["pid_alive"] = true
						task["has_checkpoint"] = true
						task["checkpoint_state"] = "PAUSED"
						task["resumable"] = true
					case "pause_immediate":
						task["state"] = "PAUSED_INTERRUPTED"
						task["running"] = true
						task["pid_alive"] = true
						task["has_checkpoint"] = true
						task["checkpoint_state"] = "PAUSED_INTERRUPTED"
						task["resumable"] = true
					case "resume", "resume_checkpoint":
						task["state"] = "SENT"
						task["running"] = true
						task["pid_alive"] = true
					case "abort":
						task["state"] = "ABORTED"
						task["running"] = false
						task["pid_alive"] = false
						kept := state.active[:0]
						for _, entry := range state.active {
							if entry["task_id"] != taskID {
								kept = append(kept, entry)
							}
						}
						state.active = kept
						inHistory := false
						for _, entry := range state.tasks {
							if entry["task_id"] == taskID {
								inHistory = true
								break
							}
						}
						if !inHistory {
							state.tasks = append(state.tasks, task)
						}
					}
				}
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"action":"` + action + `","task_id":"` + taskID + `"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, state
}

func newWatchdogTestRuntime(t *testing.T, dashboardURL string) *Runtime {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		AgentDockDefaultDir:  root,
		AgentDockHome:        filepath.Join(root, ".agentdock"),
		WatchdogDashboardURL: dashboardURL,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func bindTestSession(t *testing.T, rt *Runtime, sessionID, url, taskID string) Result {
	t.Helper()
	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: sessionID})
	bound, err := rt.mcpSessionBind(ctx, map[string]any{
		"conversation_url": url,
		"watchdog_task_id": taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func TestWatchdogOverviewParsesLiveContract(t *testing.T) {
	fixture := `{
	  "sessions": [],
	  "active_tasks": [],
	  "tasks": [
	    {
	      "task_id": "wd-20261004-172922-ee8012",
	      "target_url": "https://chatgpt.com/c/6ac20a9b-ab80-83e8-8497-90f0db2f02c0",
	      "target_title": "Blender缺口自动填充插件",
	      "state": "DONE",
	      "running": false,
	      "pid_alive": false,
	      "has_checkpoint": true,
	      "checkpoint_state": "DONE",
	      "resumable": false
	    }
	  ]
	}`
	var overview watchdogOverview
	if err := json.Unmarshal([]byte(fixture), &overview); err != nil {
		t.Fatalf("unmarshal live overview fixture: %v", err)
	}
	if len(overview.Tasks) != 1 {
		t.Fatalf("tasks = %#v", overview.Tasks)
	}
	task := overview.Tasks[0]
	if task.ID != "wd-20261004-172922-ee8012" {
		t.Fatalf("ID = %q", task.ID)
	}
	if task.TargetURL != "https://chatgpt.com/c/6ac20a9b-ab80-83e8-8497-90f0db2f02c0" {
		t.Fatalf("TargetURL = %q", task.TargetURL)
	}
	if task.State != watchdogTaskDone || task.Running || task.PIDAlive || !task.HasCheckpoint {
		t.Fatalf("task = %#v", task)
	}
	if task.CheckpointState != "DONE" || task.Resumable {
		t.Fatalf("checkpoint fields = %#v", task)
	}
	// 真实 overview 的 sessions 是数组：解码绝不能失败。
	if len(overview.Sessions) == 0 || string(overview.Sessions) == "null" {
		t.Fatalf("sessions raw = %s", overview.Sessions)
	}
}

func TestWatchdogNormalizeTaskStateMatchesDashboardTokens(t *testing.T) {
	cases := map[string]string{
		"READY": watchdogTaskActive, "SENT": watchdogTaskActive,
		"GENERATING": watchdogTaskActive, "WATCHING": watchdogTaskActive,
		"IDLE": watchdogTaskActive, "CHECK_RESULT": watchdogTaskActive,
		"RECOVERING": watchdogTaskActive, "RECONNECTING": watchdogTaskActive,
		"RESUMING": watchdogTaskActive, "STARTING": watchdogTaskActive,
		"PAUSED":             watchdogTaskPaused,
		"PAUSED_INTERRUPTED": watchdogTaskPausedInterrupted,
		"DONE":               watchdogTaskDone, "ABORTED": watchdogTaskAborted,
		"BLOCKED": watchdogTaskBlocked, "MAX_ROUNDS": watchdogTaskMaxRounds,
		"STOPPED": watchdogTaskStopped, "CRASHED": watchdogTaskCrashed,
		"paused": watchdogTaskPaused, "done": watchdogTaskDone,
		"Paused": watchdogTaskPaused, "": watchdogTaskUnknown,
	}
	for raw, want := range cases {
		if got := normalizeWatchdogTaskState(raw); got != want {
			t.Fatalf("normalizeWatchdogTaskState(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWatchdogClientParsesRealDashboardHTMLToken(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("..", "..", ".parent-reference", "dashboard_ui.html"))
	if err != nil {
		t.Skipf("parent dashboard_ui.html fixture unavailable: %v", err)
	}
	if len(html) < 40000 {
		t.Fatalf("fixture page unexpectedly small: %d bytes", len(html))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page := strings.ReplaceAll(string(html), "__TOKEN__", "wd-token-from-real-html")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	client, err := newWatchdogClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.fetchToken(context.Background())
	if err != nil {
		t.Fatalf("fetchToken over real %d-byte page: %v", len(html), err)
	}
	if token != "wd-token-from-real-html" {
		t.Fatalf("token = %q", token)
	}
}

func TestWatchdogClientRefusesOversizeHTMLAndRedirects(t *testing.T) {
	t.Run("oversize", func(t *testing.T) {
		big := strings.Repeat("x", watchdogTokenBodyLimit+1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<script>var TOKEN='" + big + "'</script>"))
		}))
		defer server.Close()
		client, err := newWatchdogClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.fetchToken(context.Background()); err == nil {
			t.Fatal("oversize dashboard page was accepted")
		}
	})
	t.Run("redirect not followed", func(t *testing.T) {
		redirected := false
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			redirected = true
			_, _ = w.Write([]byte("<script>var TOKEN='other';</script>"))
		}))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		defer server.Close()
		client, err := newWatchdogClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.fetchToken(context.Background()); err == nil {
			t.Fatal("redirect was followed")
		}
		if redirected {
			t.Fatal("redirect target was contacted")
		}
	})
	t.Run("non-loopback rejected", func(t *testing.T) {
		if _, err := newWatchdogClient("http://example.com:8770/"); err == nil {
			t.Fatal("non-loopback dashboard URL was accepted")
		}
		if _, err := newWatchdogClient("http://user:pass@127.0.0.1:8770/"); err == nil {
			t.Fatal("userinfo dashboard URL was accepted")
		}
	})
}

func TestWatchdogControlActionMappings(t *testing.T) {
	tests := []struct {
		name            string
		setup           map[string]any
		requested       string
		wantPostAction  string
		wantBefore      string
		wantAfter       string
		wantEffective   bool
		noStateMutation bool
	}{
		{
			name:      "pause after reply",
			setup:     map[string]any{"state": "SENT", "running": true, "pid_alive": true},
			requested: "pause", wantPostAction: "pause",
			wantBefore: watchdogTaskActive, wantAfter: watchdogTaskPaused, wantEffective: true,
		},
		{
			name:      "interrupt immediate",
			setup:     map[string]any{"state": "SENT", "running": true, "pid_alive": true},
			requested: "interrupt", wantPostAction: "pause_immediate",
			wantBefore: watchdogTaskActive, wantAfter: watchdogTaskPausedInterrupted, wantEffective: true,
		},
		{
			name:      "resume running paused uses resume",
			setup:     map[string]any{"state": "PAUSED", "running": true, "pid_alive": true, "resumable": false},
			requested: "resume", wantPostAction: "resume",
			wantBefore: watchdogTaskPaused, wantAfter: watchdogTaskActive, wantEffective: true,
		},
		{
			name:      "resume stopped resumable uses resume_checkpoint",
			setup:     map[string]any{"state": "PAUSED", "running": false, "pid_alive": false, "has_checkpoint": true, "checkpoint_state": "PAUSED", "resumable": true},
			requested: "resume", wantPostAction: "resume_checkpoint",
			wantBefore: watchdogTaskPaused, wantAfter: watchdogTaskActive, wantEffective: true,
		},
		{
			name:      "abort terminates",
			setup:     map[string]any{"state": "SENT", "running": true, "pid_alive": true},
			requested: "abort", wantPostAction: "abort",
			wantBefore: watchdogTaskActive, wantAfter: watchdogTaskAborted, wantEffective: true,
		},
		{
			name:      "pending when state unchanged",
			setup:     map[string]any{"state": "SENT", "running": true, "pid_alive": true},
			requested: "pause", wantPostAction: "pause",
			wantEffective:   false,
			noStateMutation: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, state := newFakeDashboard(t)
			state.skipMutation = tc.noStateMutation
			rt := newWatchdogTestRuntime(t, server.URL)
			state.addActive(map[string]any{
				"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc",
				"target_title": "任务一", "state": "SENT", "running": true, "pid_alive": true,
				"has_checkpoint": false, "checkpoint_state": "", "resumable": false,
			})
			state.setTask("wd-1", tc.setup)
			bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

			ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
			result, err := rt.mcpSessionControl(ctx, map[string]any{"action": tc.requested})
			if err != nil {
				t.Fatalf("mcpSessionControl(%s) error = %v", tc.requested, err)
			}
			watchdog, _ := result["watchdog"].(Result)
			if len(state.actions) != 1 || state.actions[0] != tc.wantPostAction {
				t.Fatalf("posted actions = %v, want [%s]", state.actions, tc.wantPostAction)
			}
			if watchdog["accepted"] != true {
				t.Fatalf("accepted = %#v", watchdog["accepted"])
			}
			if watchdog["effective"] != tc.wantEffective {
				t.Fatalf("effective = %#v, want %v (watchdog=%#v)", watchdog["effective"], tc.wantEffective, watchdog)
			}
			if tc.wantBefore != "" && watchdog["task_state_before"] != tc.wantBefore {
				t.Fatalf("task_state_before = %#v, want %q", watchdog["task_state_before"], tc.wantBefore)
			}
			if tc.wantAfter != "" && watchdog["task_state_after"] != tc.wantAfter {
				t.Fatalf("task_state_after = %#v, want %q", watchdog["task_state_after"], tc.wantAfter)
			}
			if watchdog["task_id"] != "wd-1" {
				t.Fatalf("task_id = %#v", watchdog["task_id"])
			}
			// 结构化结果绝不能泄漏 dashboard TOKEN。
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), state.token) {
				t.Fatalf("control result leaked dashboard token: %s", encoded)
			}
		})
	}
}

func TestWatchdogResumeRefusesNonResumableAndTerminal(t *testing.T) {
	tests := []struct {
		name  string
		setup map[string]any
	}{
		{"stopped without checkpoint", map[string]any{"state": "PAUSED", "running": false, "pid_alive": false, "has_checkpoint": false, "resumable": false}},
		{"terminal done", map[string]any{"state": "DONE", "running": false, "pid_alive": false, "has_checkpoint": true, "checkpoint_state": "DONE", "resumable": false}},
		{"terminal aborted", map[string]any{"state": "ABORTED", "running": false, "pid_alive": false, "resumable": false}},
		{"already running unpaused", map[string]any{"state": "SENT", "running": true, "pid_alive": true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, state := newFakeDashboard(t)
			rt := newWatchdogTestRuntime(t, server.URL)
			state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true})
			state.setTask("wd-1", tc.setup)
			bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

			ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
			_, err := rt.mcpSessionControl(ctx, map[string]any{"action": "resume"})
			toolErr, ok := err.(*ToolError)
			if !ok || toolErr.Code != "WATCHDOG_ACTION_REFUSED" {
				t.Fatalf("resume error = %#v, want WATCHDOG_ACTION_REFUSED", err)
			}
			if len(state.actions) != 0 {
				t.Fatalf("resume must not POST for refused target, actions = %v", state.actions)
			}
		})
	}
}

func TestWatchdogControlAcceptedRequiresBodyOK(t *testing.T) {
	server, state := newFakeDashboard(t)
	state.okOverride = func(action string) (bool, string) { return false, "这个任务当前没有运行" }
	rt := newWatchdogTestRuntime(t, server.URL)
	state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true, "pid_alive": true})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	_, err := rt.mcpSessionControl(ctx, map[string]any{"action": "abort"})
	toolErr, ok := err.(*ToolError)
	if !ok || toolErr.Code != "WATCHDOG_ACTION_FAILED" {
		t.Fatalf("abort error = %#v, want WATCHDOG_ACTION_FAILED", err)
	}
	if !strings.Contains(toolErr.Message, "这个任务当前没有运行") {
		t.Fatalf("error message = %q", toolErr.Message)
	}
}

func TestWatchdogControlScopeRejection(t *testing.T) {
	t.Run("no task for conversation", func(t *testing.T) {
		server, _ := newFakeDashboard(t)
		rt := newWatchdogTestRuntime(t, server.URL)
		bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")
		ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
		_, err := rt.mcpSessionControl(ctx, map[string]any{"action": "pause"})
		if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "WATCHDOG_TARGET_UNAVAILABLE" {
			t.Fatalf("error = %#v, want WATCHDOG_TARGET_UNAVAILABLE", err)
		}
	})
	t.Run("bound task belongs to another conversation", func(t *testing.T) {
		server, state := newFakeDashboard(t)
		rt := newWatchdogTestRuntime(t, server.URL)
		state.addActive(map[string]any{"task_id": "wd-other", "target_url": "https://chatgpt.com/c/other", "state": "SENT", "running": true, "pid_alive": true})
		bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "wd-other")
		ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
		_, err := rt.mcpSessionControl(ctx, map[string]any{"action": "pause"})
		if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "WATCHDOG_TARGET_UNAVAILABLE" {
			t.Fatalf("error = %#v, want WATCHDOG_TARGET_UNAVAILABLE", err)
		}
		if len(state.actions) != 0 {
			t.Fatalf("unrelated task was touched: %v", state.actions)
		}
	})
	t.Run("ambiguous selection refused", func(t *testing.T) {
		server, state := newFakeDashboard(t)
		rt := newWatchdogTestRuntime(t, server.URL)
		// 两个同 URL 的停止可恢复任务 → 无法确定目标。
		state.setTask("wd-a", map[string]any{"task_id": "wd-a", "target_url": "https://chatgpt.com/c/abc", "state": "PAUSED", "running": false, "pid_alive": false, "resumable": true})
		state.setTask("wd-b", map[string]any{"task_id": "wd-b", "target_url": "https://chatgpt.com/c/abc", "state": "STOPPED", "running": false, "pid_alive": false, "resumable": true})
		bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")
		ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
		_, err := rt.mcpSessionControl(ctx, map[string]any{"action": "pause"})
		if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "WATCHDOG_TARGET_UNAVAILABLE" {
			t.Fatalf("error = %#v, want WATCHDOG_TARGET_UNAVAILABLE", err)
		}
	})
	t.Run("prefers running task over stopped history", func(t *testing.T) {
		server, state := newFakeDashboard(t)
		rt := newWatchdogTestRuntime(t, server.URL)
		state.setTask("wd-stopped", map[string]any{"task_id": "wd-stopped", "target_url": "https://chatgpt.com/c/abc", "state": "PAUSED", "running": false, "resumable": true})
		state.addActive(map[string]any{"task_id": "wd-running", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true, "pid_alive": true})
		bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")
		ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
		result, err := rt.mcpSessionControl(ctx, map[string]any{"action": "pause"})
		if err != nil {
			t.Fatal(err)
		}
		watchdog, _ := result["watchdog"].(Result)
		if watchdog["task_id"] != "wd-running" {
			t.Fatalf("selected task = %#v, want wd-running", watchdog["task_id"])
		}
	})
}

func TestWatchdogPauseInterruptRefusedOnStoppedTasks(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	state.setTask("wd-1", map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "PAUSED", "running": false, "pid_alive": false, "resumable": true})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	for _, action := range []string{"pause", "interrupt"} {
		_, err := rt.mcpSessionControl(ctx, map[string]any{"action": action})
		if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "WATCHDOG_ACTION_REFUSED" {
			t.Fatalf("%s error = %#v, want WATCHDOG_ACTION_REFUSED", action, err)
		}
	}
	if len(state.actions) != 0 {
		t.Fatalf("stopped task was mutated: %v", state.actions)
	}
}

func TestWatchdogUnboundSelectionIgnoresCompletedHistory(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	// 完成/中止历史不参与选择：唯一可恢复停止任务胜出。
	state.setTask("wd-done", map[string]any{"task_id": "wd-done", "target_url": "https://chatgpt.com/c/abc", "state": "DONE", "running": false, "pid_alive": false, "resumable": false})
	state.setTask("wd-aborted", map[string]any{"task_id": "wd-aborted", "target_url": "https://chatgpt.com/c/abc", "state": "ABORTED", "running": false, "pid_alive": false, "resumable": false})
	state.setTask("wd-resume", map[string]any{"task_id": "wd-resume", "target_url": "https://chatgpt.com/c/abc", "state": "STOPPED", "running": false, "pid_alive": false, "resumable": true})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	result, err := rt.mcpSessionControl(ctx, map[string]any{"action": "resume"})
	if err != nil {
		t.Fatal(err)
	}
	watchdog, _ := result["watchdog"].(Result)
	if watchdog["task_id"] != "wd-resume" {
		t.Fatalf("selected task = %#v, want wd-resume", watchdog["task_id"])
	}
	if len(state.actions) != 1 || state.actions[0] != "resume_checkpoint" {
		t.Fatalf("actions = %v, want [resume_checkpoint]", state.actions)
	}
}

func TestWatchdogTokenRotationAfterDashboardRestart(t *testing.T) {
	var mu sync.Mutex
	currentToken := "wd-token-old"
	servedHTML := "<script>var TOKEN='wd-token-old';</script>"
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(servedHTML))
		case "/api/action":
			posts++
			if r.Header.Get("X-Watchdog-Token") != currentToken {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"ok":false,"error":"Invalid dashboard token"}`))
				return
			}
			// 首次成功后再“重启”dashboard：轮换 token。
			if posts == 2 {
				currentToken = "wd-token-new"
				servedHTML = "<script>var TOKEN='wd-token-new';</script>"
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	client, err := newWatchdogClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.postAction(context.Background(), "pause", "wd-1"); err != nil {
		t.Fatalf("first post: %v", err)
	}
	if err := client.postAction(context.Background(), "pause", "wd-1"); err != nil {
		t.Fatalf("post after token rotation: %v", err)
	}
	if err := client.postAction(context.Background(), "pause", "wd-1"); err != nil {
		t.Fatalf("post with rotated token: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 4 {
		t.Fatalf("posts = %d, want 4 (1 ok + 1 rejected + 1 retried ok + 1 ok)", posts)
	}
}

func TestWatchdogActionOversizeBodyNotAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte("<script>var TOKEN='wd-token';</script>"))
		case "/api/action":
			// 超过限制的响应体即使以 ok:true 开头也不能被接受。
			_, _ = w.Write([]byte(`{"ok":true}` + strings.Repeat("x", watchdogTokenBodyLimit+1)))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	client, err := newWatchdogClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.postAction(context.Background(), "pause", "wd-1"); err == nil {
		t.Fatal("oversize action response with ok:true prefix was accepted")
	}
}

func TestWatchdogSelectionRefusesMultipleRunningTasks(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	// 两个同 URL 活动任务 + 一个停止可恢复任务：立即拒绝，绝不回退到
	// 唯一可恢复断点，也不发出任何 POST。
	state.addActive(map[string]any{"task_id": "wd-a", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true, "pid_alive": true})
	state.addActive(map[string]any{"task_id": "wd-b", "target_url": "https://chatgpt.com/c/abc", "state": "GENERATING", "running": true, "pid_alive": true})
	state.setTask("wd-c", map[string]any{"task_id": "wd-c", "target_url": "https://chatgpt.com/c/abc", "state": "PAUSED", "running": false, "pid_alive": false, "resumable": true})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	for _, action := range []string{"pause", "resume"} {
		_, err := rt.mcpSessionControl(ctx, map[string]any{"action": action})
		if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "WATCHDOG_TARGET_UNAVAILABLE" {
			t.Fatalf("%s error = %#v, want WATCHDOG_TARGET_UNAVAILABLE", action, err)
		}
	}
	if len(state.actions) != 0 {
		t.Fatalf("ambiguous selection posted actions: %v", state.actions)
	}
}

func TestWatchdogResumeEffectiveRequiresRunningFlag(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "PAUSED", "running": true, "pid_alive": true, "resumable": false})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	// 模拟 dashboard：resume 后仅更新 state 为 READY，进程未存活
	// （running=false）。过期的 READY 不能宣称恢复生效。
	state.mutate = func(action, taskID string, task map[string]any) {
		task["state"] = "READY"
		task["running"] = false
		task["pid_alive"] = false
	}

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	result, err := rt.mcpSessionControl(ctx, map[string]any{"action": "resume"})
	if err != nil {
		t.Fatal(err)
	}
	watchdog, _ := result["watchdog"].(Result)
	if watchdog["accepted"] != true || watchdog["effective"] != false {
		t.Fatalf("watchdog = %#v, want accepted=true effective=false", watchdog)
	}
	if watchdog["task_state_after"] != watchdogTaskActive {
		t.Fatalf("task_state_after = %#v", watchdog["task_state_after"])
	}
}

func TestWatchdogControlKeepsAdmissionWhenObservationFails(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true, "pid_alive": true})
	// 第一次 overview（动作前）正常，POST 受理后第二次 overview 失败。
	state.failOverviewFrom = 2
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	result, err := rt.mcpSessionControl(ctx, map[string]any{"action": "pause"})
	if err != nil {
		t.Fatalf("admitted action must not fail when observation is unavailable: %v", err)
	}
	watchdog, _ := result["watchdog"].(Result)
	if watchdog["accepted"] != true || watchdog["effective"] != false {
		t.Fatalf("watchdog = %#v, want accepted=true effective=false", watchdog)
	}
	note, _ := watchdog["note"].(string)
	if !strings.Contains(note, "observation unavailable") {
		t.Fatalf("note = %q", note)
	}
	if len(state.actions) != 1 || state.actions[0] != "pause" {
		t.Fatalf("actions = %v", state.actions)
	}
}

func TestWatchdogControlRequiresBindingAndIdentity(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true, "pid_alive": true})

	if _, err := rt.mcpSessionControl(context.Background(), map[string]any{"action": "pause"}); err == nil {
		t.Fatal("control without protocol identity unexpectedly succeeded")
	}
	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-unbound"}, "server_info", "req-1"); err != nil {
		t.Fatal(err)
	}
	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-unbound"})
	_, err := rt.mcpSessionControl(ctx, map[string]any{"action": "pause"})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "MCP_SESSION_NOT_BOUND" {
		t.Fatalf("error = %#v, want MCP_SESSION_NOT_BOUND", err)
	}
	if len(state.actions) != 0 {
		t.Fatalf("unbound control posted actions: %v", state.actions)
	}
}

func TestWatchdogControlRejectsArbitraryTaskIDArguments(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true, "pid_alive": true})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	_, err := rt.Call(ctx, "mcp_session_control", map[string]any{"action": "pause", "task_id": "wd-1", "conversation_url": "https://chatgpt.com/c/evil"})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %#v, want INVALID_ARGUMENT", err)
	}
	if len(state.actions) != 0 {
		t.Fatalf("forged task params must not reach the dashboard: %v", state.actions)
	}
}

func TestWatchdogStatusReadback(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc?q=1#frag", "state": "PAUSED", "running": true, "pid_alive": true, "resumable": true})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc?x=1", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	result, err := rt.mcpSessionControl(ctx, map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	watchdog, _ := result["watchdog"].(Result)
	if watchdog["task_id"] != "wd-1" || watchdog["task_state"] != watchdogTaskPaused || watchdog["running"] != true {
		t.Fatalf("watchdog = %#v", watchdog)
	}
	if watchdog["conversation_url"] != "https://chatgpt.com/c/abc" {
		t.Fatalf("canonical conversation_url = %#v", watchdog["conversation_url"])
	}
	if len(state.actions) != 0 {
		t.Fatalf("status must not POST: %v", state.actions)
	}

	// 无任务时返回诚实状态而不是错误。
	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-2"}, "server_info", "req-2"); err != nil {
		t.Fatal(err)
	}
	unbound := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-2"})
	statusResult, err := rt.mcpSessionControl(unbound, map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	watchdog, _ = statusResult["watchdog"].(Result)
	if watchdog["note"] != "no watchdog task observed" {
		t.Fatalf("unbound status watchdog = %#v", watchdog)
	}
}

func TestWatchdogStatusDegradesWhenDashboardDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	rt := newWatchdogTestRuntime(t, server.URL)
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	result, err := rt.mcpSessionControl(ctx, map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("status must not fail when dashboard is down: %v", err)
	}
	watchdog, _ := result["watchdog"].(Result)
	if watchdog["note"] != "watchdog dashboard unavailable" {
		t.Fatalf("watchdog = %#v", watchdog)
	}

	_, err = rt.mcpSessionControl(ctx, map[string]any{"action": "pause"})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "WATCHDOG_DASHBOARD_UNAVAILABLE" {
		t.Fatalf("pause error = %#v, want WATCHDOG_DASHBOARD_UNAVAILABLE", err)
	}
}

func TestWatchdogControlContextCancellation(t *testing.T) {
	server, state := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)
	state.addActive(map[string]any{"task_id": "wd-1", "target_url": "https://chatgpt.com/c/abc", "state": "SENT", "running": true, "pid_alive": true})
	bindTestSession(t, rt, "session-1", "https://chatgpt.com/c/abc", "")

	ctx, cancel := context.WithCancel(WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"}))
	cancel()
	if _, err := rt.mcpSessionControl(ctx, map[string]any{"action": "pause"}); err == nil {
		t.Fatal("canceled control unexpectedly succeeded")
	}
}

func TestWatchdogBootstrapAtomicMetadataArgsAgreement(t *testing.T) {
	server, _ := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)

	// 与 MCP 入口一致：先记录协议身份，再进入工具处理。
	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-1"}, "server_info", "req-1"); err != nil {
		t.Fatal(err)
	}
	ctx := WithMCPClientContext(context.Background(), MCPClientContext{
		SessionID:       "session-1",
		ConversationURL: "https://chatgpt.com/c/abc?meta=1",
	})
	result, err := rt.Call(ctx, "agentdock_context", map[string]any{
		"action":           "bootstrap",
		"conversation_url": "https://chatgpt.com/c/abc",
		"watchdog_task_id": "wd-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["context"] == "" || result["session"] == nil {
		t.Fatalf("bootstrap result = %#v", result)
	}
	session := result["session"].(Result)
	if session["conversation_url"] != "https://chatgpt.com/c/abc" || session["watchdog_task_id"] != "wd-1" {
		t.Fatalf("session = %#v", session)
	}
	record, ok := rt.mcpSessions.current("session-1")
	if !ok || record.BoundAt == "" {
		t.Fatalf("record = %#v, ok=%v", record, ok)
	}
	// 第二次 bootstrap 幂等：BoundAt 保持稳定。
	firstBoundAt := record.BoundAt
	time.Sleep(5 * time.Millisecond)
	if _, err := rt.Call(ctx, "agentdock_context", map[string]any{"action": "bootstrap"}); err != nil {
		t.Fatal(err)
	}
	record, _ = rt.mcpSessions.current("session-1")
	if record.BoundAt != firstBoundAt {
		t.Fatalf("bound_at changed across idempotent bootstrap: %q -> %q", firstBoundAt, record.BoundAt)
	}
}

func TestWatchdogBootstrapMetadataArgsDisagreementFailsWithoutBinding(t *testing.T) {
	server, _ := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)

	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-1"}, "server_info", "req-1"); err != nil {
		t.Fatal(err)
	}
	ctx := WithMCPClientContext(context.Background(), MCPClientContext{
		SessionID:       "session-1",
		ConversationURL: "https://chatgpt.com/c/aaa",
	})
	_, err := rt.Call(ctx, "agentdock_context", map[string]any{
		"action":           "bootstrap",
		"conversation_url": "https://chatgpt.com/c/bbb",
	})
	toolErr, ok := err.(*ToolError)
	if !ok || toolErr.Code != "MCP_SESSION_BINDING_CONFLICT" {
		t.Fatalf("error = %#v, want MCP_SESSION_BINDING_CONFLICT", err)
	}
	// 冲突必须不留下任何新绑定。
	record, ok := rt.mcpSessions.current("session-1")
	if ok && record.ConversationURL != "" {
		t.Fatalf("disagreement left a binding behind: %#v", record)
	}

	// 任务 ID 冲突同样拒绝。
	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-2"}, "server_info", "req-2"); err != nil {
		t.Fatal(err)
	}
	ctx2 := WithMCPClientContext(context.Background(), MCPClientContext{
		SessionID:       "session-2",
		ConversationURL: "https://chatgpt.com/c/ccc",
		WatchdogTaskID:  "wd-meta",
	})
	_, err = rt.Call(ctx2, "agentdock_context", map[string]any{
		"action":           "bootstrap",
		"conversation_url": "https://chatgpt.com/c/ccc",
		"watchdog_task_id": "wd-args",
	})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "MCP_SESSION_BINDING_CONFLICT" {
		t.Fatalf("task disagreement error = %#v", err)
	}
	record, ok = rt.mcpSessions.current("session-2")
	if ok && record.ConversationURL != "" {
		t.Fatalf("task disagreement left a binding behind: %#v", record)
	}
}

func TestWatchdogBootstrapRejectsInvalidFallbackValues(t *testing.T) {
	server, _ := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)

	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-1"}, "server_info", "req-1"); err != nil {
		t.Fatal(err)
	}
	// 显式 URL 无效 → INVALID_ARGUMENT，不能静默忽略。
	ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: "session-1"})
	_, err := rt.Call(ctx, "agentdock_context", map[string]any{
		"action":           "bootstrap",
		"conversation_url": "https://chatgpt.com/c/",
	})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("invalid fallback URL error = %#v, want INVALID_ARGUMENT", err)
	}
	record, ok := rt.mcpSessions.current("session-1")
	if ok && record.ConversationURL != "" {
		t.Fatalf("invalid fallback URL left a binding: %#v", record)
	}

	// 显式 URL 缺少协议身份 → MCP_CLIENT_SESSION_UNAVAILABLE。
	_, err = rt.Call(context.Background(), "agentdock_context", map[string]any{
		"action":           "bootstrap",
		"conversation_url": "https://chatgpt.com/c/abc",
	})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "MCP_CLIENT_SESSION_UNAVAILABLE" {
		t.Fatalf("fallback without identity error = %#v, want MCP_CLIENT_SESSION_UNAVAILABLE", err)
	}

	// 超限的可选回退值 → INVALID_ARGUMENT。
	_, err = rt.Call(ctx, "agentdock_context", map[string]any{
		"action":             "bootstrap",
		"conversation_url":   "https://chatgpt.com/c/abc",
		"conversation_title": strings.Repeat("t", 513),
	})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("oversize title error = %#v, want INVALID_ARGUMENT", err)
	}
	_, err = rt.Call(ctx, "agentdock_context", map[string]any{
		"action":           "bootstrap",
		"conversation_url": "https://chatgpt.com/c/abc",
		"watchdog_task_id": strings.Repeat("t", 129),
	})
	if toolErr, ok := err.(*ToolError); !ok || toolErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("oversize task id error = %#v, want INVALID_ARGUMENT", err)
	}

	// 无效元数据 URL 仍保持匿名且不失败（契约）。
	if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: "session-2"}, "server_info", "req-2"); err != nil {
		t.Fatal(err)
	}
	metaCtx := WithMCPClientContext(context.Background(), MCPClientContext{
		SessionID: "session-2", ConversationURL: "http://evil.example/c/x",
	})
	if _, err := rt.Call(metaCtx, "agentdock_context", map[string]any{"action": "bootstrap"}); err != nil {
		t.Fatalf("invalid meta URL must stay anonymous without failure: %v", err)
	}
	record, ok = rt.mcpSessions.current("session-2")
	if ok && record.ConversationURL != "" {
		t.Fatalf("invalid meta URL left a binding: %#v", record)
	}
}

func TestWatchdogConcurrentBootstrapIsolation(t *testing.T) {
	server, _ := newFakeDashboard(t)
	rt := newWatchdogTestRuntime(t, server.URL)

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func(n int) {
			<-start
			sessionID := "session-" + string(rune('A'+n))
			url := "https://chatgpt.com/c/" + string(rune('a'+n))
			if _, err := rt.RecordMCPClientCall(MCPClientContext{SessionID: sessionID}, "server_info", "req-"+sessionID); err != nil {
				errs <- err
				return
			}
			ctx := WithMCPClientContext(context.Background(), MCPClientContext{SessionID: sessionID, ConversationURL: url})
			_, err := rt.Call(ctx, "agentdock_context", map[string]any{"action": "bootstrap"})
			errs <- err
		}(i)
	}
	close(start)
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent bootstrap error: %v", err)
		}
	}
	for i := 0; i < callers; i++ {
		sessionID := "session-" + string(rune('A'+i))
		url := "https://chatgpt.com/c/" + string(rune('a'+i))
		record, ok := rt.mcpSessions.current(sessionID)
		if !ok || record.ConversationURL != url {
			t.Fatalf("session %s record = %#v, ok=%v", sessionID, record, ok)
		}
	}
}
