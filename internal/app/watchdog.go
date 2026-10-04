package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/config"
)

const (
	watchdogOverviewBodyLimit = 1 << 20 // 1 MiB bound for /api/overview JSON.
	watchdogTokenBodyLimit    = 64 << 10
	watchdogRequestTimeout    = 10 * time.Second
	watchdogTokenRegex        = `var\s+TOKEN\s*=\s*'([^']+)'`
	watchdogTokenMaxLength    = 4096
)

// Dashboard POST /api/action verbs (dashboard 2.3.0).
const (
	watchdogDashboardActionPause            = "pause"
	watchdogDashboardActionPauseImmediate   = "pause_immediate"
	watchdogDashboardActionResume           = "resume"
	watchdogDashboardActionResumeCheckpoint = "resume_checkpoint"
	watchdogDashboardActionAbort            = "abort"
)

// Canonical normalized Watchdog task state classes mapped from the dashboard
// state tokens (dashboard.py 2.3.0). DONE/ABORTED are final for recovery; all
// other states are kept distinct so readback stays honest and a stopped task
// with an eligible checkpoint (resumable=true) can always recover.
const (
	watchdogTaskActive            = "ACTIVE"
	watchdogTaskPaused            = "PAUSED"
	watchdogTaskPausedInterrupted = "PAUSED_INTERRUPTED"
	watchdogTaskDone              = "DONE"
	watchdogTaskAborted           = "ABORTED"
	watchdogTaskStopped           = "STOPPED"
	watchdogTaskBlocked           = "BLOCKED"
	watchdogTaskMaxRounds         = "MAX_ROUNDS"
	watchdogTaskCrashed           = "CRASHED"
	watchdogTaskError             = "ERROR"
	watchdogTaskUnknown           = "UNKNOWN"
)

// watchdogFinalStates are the only states that refuse recovery, matching
// dashboard.py resume_task which rejects checkpoint states DONE/ABORTED.
var watchdogFinalStates = map[string]bool{
	watchdogTaskDone:    true,
	watchdogTaskAborted: true,
}

// watchdogClient is the loopback-only HTTP client for the ChatGPT Watchdog
// dashboard. It never follows redirects, bounds response bodies, honors
// context cancellation, and keeps the dashboard TOKEN private to this process.
type watchdogClient struct {
	baseURL string
	http    *http.Client

	mu    sync.Mutex
	token string
}

func newWatchdogClient(baseURL string) (*watchdogClient, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = config.DefaultWatchdogDashboardURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("watchdog dashboard URL is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("watchdog dashboard URL must not contain user info, a query, or a fragment")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, errors.New("watchdog dashboard URL must use http or https")
	}
	if !isLoopbackWatchdogHost(parsed.Hostname()) {
		return nil, errors.New("watchdog dashboard URL must be loopback-only")
	}
	transport := &http.Transport{
		Proxy: nil, // never route the dashboard through a proxy.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if !isLoopbackWatchdogHost(host) {
				return nil, fmt.Errorf("watchdog dashboard dial refused for non-loopback host %q", host)
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	}
	return &watchdogClient{
		baseURL: strings.TrimRight(baseURL, "/") + "/",
		http: &http.Client{
			Transport: transport,
			Timeout:   watchdogRequestTimeout,
			// Redirects are never followed: a redirecting dashboard is a
			// configuration error and must surface as an error, not a hop.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("watchdog dashboard redirect refused")
			},
		},
	}, nil
}

func isLoopbackWatchdogHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// fetchToken returns the dashboard TOKEN parsed from GET /. The token is held
// in memory only, is never logged, never persisted, and never returned to
// callers.
func (c *watchdogClient) fetchToken(ctx context.Context) (string, error) {
	if c == nil {
		return "", errors.New("watchdog dashboard client is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, watchdogRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return "", fmt.Errorf("build watchdog dashboard request: %w", err)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("watchdog dashboard unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("watchdog dashboard returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, watchdogTokenBodyLimit+1))
	if err != nil {
		return "", fmt.Errorf("read watchdog dashboard page: %w", err)
	}
	if len(body) > watchdogTokenBodyLimit {
		return "", errors.New("watchdog dashboard page exceeds the token body bound")
	}
	pattern := regexp.MustCompile(watchdogTokenRegex)
	match := pattern.FindSubmatch(body)
	if len(match) < 2 {
		return "", errors.New("watchdog dashboard page does not embed a TOKEN")
	}
	token := strings.TrimSpace(string(match[1]))
	if token == "" || len(token) > watchdogTokenMaxLength {
		return "", errors.New("watchdog dashboard TOKEN is missing or oversized")
	}
	c.token = token
	return c.token, nil
}

// watchdogTask is one dashboard task entry (dashboard.py read_run_state /
// read_tasks / active_watchdogs shape). The live contract uses target_url,
// target_title, state, running, pid_alive and the checkpoint/resumable flags;
// session and active_tasks arrays in the overview are intentionally not
// decoded into fields.
type watchdogTask struct {
	ID              string
	TargetURL       string
	TargetTitle     string
	State           string // normalized canonical state class.
	RawState        string
	Running         bool
	PIDAlive        bool
	HasCheckpoint   bool
	CheckpointState string
	Resumable       bool
}

func (t *watchdogTask) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.ID = firstJSONString(raw, "task_id", "id")
	t.TargetURL = firstJSONString(raw, "target_url", "conversation_url", "conversationUrl")
	t.TargetTitle = firstJSONString(raw, "target_title", "title", "name")
	t.RawState = firstJSONString(raw, "state", "status")
	t.State = normalizeWatchdogTaskState(t.RawState)
	t.Running = firstJSONBool(raw, "running")
	t.PIDAlive = firstJSONBool(raw, "pid_alive")
	t.HasCheckpoint = firstJSONBool(raw, "has_checkpoint")
	t.CheckpointState = firstJSONString(raw, "checkpoint_state")
	t.Resumable = firstJSONBool(raw, "resumable")
	return nil
}

// watchdogOverview decodes the live /api/overview contract. sessions and
// active_tasks are arrays; only active_tasks is needed (as task objects) and
// sessions is kept as RawMessage so the real payload never fails decoding.
type watchdogOverview struct {
	Sessions    json.RawMessage `json:"sessions"`
	ActiveTasks []watchdogTask  `json:"active_tasks"`
	Tasks       []watchdogTask  `json:"tasks"`
}

// allTasks merges the history tasks with the active task list; active entries
// carry fresher run state and win by id.
func (o *watchdogOverview) allTasks() []watchdogTask {
	if o == nil {
		return nil
	}
	byID := make(map[string]watchdogTask, len(o.Tasks)+len(o.ActiveTasks))
	for _, task := range o.Tasks {
		if task.ID != "" {
			byID[task.ID] = task
		}
	}
	for _, task := range o.ActiveTasks {
		if task.ID != "" {
			byID[task.ID] = task
		}
	}
	out := make([]watchdogTask, 0, len(byID))
	for _, task := range byID {
		out = append(out, task)
	}
	return out
}

// fetchOverview reads GET /api/overview with a bounded body.
func (c *watchdogClient) fetchOverview(ctx context.Context) (*watchdogOverview, error) {
	if c == nil {
		return nil, errors.New("watchdog dashboard client is unavailable")
	}
	requestCtx, cancel := context.WithTimeout(ctx, watchdogRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.baseURL+"api/overview", nil)
	if err != nil {
		return nil, fmt.Errorf("build watchdog overview request: %w", err)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("watchdog dashboard overview unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("watchdog dashboard overview returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, watchdogOverviewBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read watchdog overview: %w", err)
	}
	if len(body) > watchdogOverviewBodyLimit {
		return nil, errors.New("watchdog dashboard overview exceeds the response body bound")
	}
	var overview watchdogOverview
	if err := json.Unmarshal(body, &overview); err != nil {
		return nil, fmt.Errorf("decode watchdog overview: %w", err)
	}
	return &overview, nil
}

// watchdogActionResponse is the live POST /api/action body: accepted means the
// dashboard returned ok:true, regardless of the HTTP status alone.
type watchdogActionResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// postAction posts one control verb to POST /api/action authenticated with the
// dashboard TOKEN. It returns an error unless the dashboard body reports
// ok:true; a 2xx with ok:false is NOT accepted. A definitive 403 auth
// rejection (dashboard restarted and rotated its TOKEN) clears the cached
// token and retries exactly once; transport failures are never retried.
func (c *watchdogClient) postAction(ctx context.Context, action, taskID string) error {
	if c == nil {
		return errors.New("watchdog dashboard client is unavailable")
	}
	payload, err := json.Marshal(map[string]string{"action": action, "task_id": taskID})
	if err != nil {
		return fmt.Errorf("encode watchdog action: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.fetchToken(ctx)
		if err != nil {
			return err
		}
		requestCtx, cancel := context.WithTimeout(ctx, watchdogRequestTimeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.baseURL+"api/action", bytes.NewReader(payload))
		if err != nil {
			cancel()
			return fmt.Errorf("build watchdog action request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Watchdog-Token", token)
		response, err := c.http.Do(request)
		if err != nil {
			cancel()
			return fmt.Errorf("watchdog dashboard action failed: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, watchdogTokenBodyLimit+1))
		response.Body.Close()
		cancel()
		if readErr != nil {
			return fmt.Errorf("read watchdog action response: %w", readErr)
		}
		if len(body) > watchdogTokenBodyLimit {
			return errors.New("watchdog dashboard action response exceeds the body bound")
		}
		var parsed watchdogActionResponse
		if len(body) > 0 {
			_ = json.Unmarshal(body, &parsed)
		}
		if response.StatusCode == http.StatusForbidden && attempt == 0 {
			// 确定的认证拒绝（dashboard.py 仅对 token 不匹配返回 403）：
			// 清除缓存 token，重新解析 GET / 后重试一次。
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			message := strings.TrimSpace(parsed.Error)
			if message == "" {
				message = fmt.Sprintf("watchdog dashboard action returned status %d", response.StatusCode)
			}
			return errors.New(message)
		}
		if !parsed.OK {
			message := strings.TrimSpace(parsed.Error)
			if message == "" {
				message = "watchdog dashboard action was rejected"
			}
			return errors.New(message)
		}
		return nil
	}
	return errors.New("watchdog dashboard action was rejected")
}

func firstJSONString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstJSONBool(raw map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := raw[key].(type) {
		case bool:
			return value
		case string:
			parsed, err := parseWatchdogBool(value)
			if err == nil {
				return parsed
			}
		}
	}
	return false
}

func parseWatchdogBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off", "":
		return false, nil
	default:
		return false, errors.New("not a boolean")
	}
}

// normalizeWatchdogTaskState maps dashboard state tokens (case and separator
// insensitive) onto canonical classes. Non-final stopped states keep their own
// token so readback stays honest and recovery eligibility is decided by the
// dashboard resumable flag, never by the state alone.
func normalizeWatchdogTaskState(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	if normalized == "" {
		return watchdogTaskUnknown
	}
	switch normalized {
	case "ready", "sent", "generating", "watching", "idle", "checkresult",
		"recovering", "reconnecting", "resuming", "starting":
		return watchdogTaskActive
	case "paused":
		return watchdogTaskPaused
	case "pausedinterrupted", "activepaused":
		return watchdogTaskPausedInterrupted
	case "done", "completed", "complete", "succeeded", "success", "finished":
		return watchdogTaskDone
	case "aborted", "abort", "cancelled", "canceled", "terminated", "killed":
		return watchdogTaskAborted
	case "stopped", "idle_after_reply":
		return watchdogTaskStopped
	case "blocked":
		return watchdogTaskBlocked
	case "maxrounds", "max_rounds":
		return watchdogTaskMaxRounds
	case "crashed":
		return watchdogTaskCrashed
	case "error", "failed", "failure":
		return watchdogTaskError
	default:
		return strings.ToUpper(normalized)
	}
}

// resolveWatchdogTask picks the single eligible dashboard task for a canonical
// bound conversation URL. A bound task id must belong to that conversation and
// is retained even when terminal (readback after abort). Without a bound id a
// unique running task is preferred, then a unique resumable non-running task;
// completed DONE/ABORTED history is ignored for selection and remaining
// ambiguity is refused.
func resolveWatchdogTask(overview *watchdogOverview, canonicalConversationURL, boundTaskID string) (watchdogTask, error) {
	if overview == nil {
		return watchdogTask{}, errors.New("watchdog overview is unavailable")
	}
	sameURL := make([]watchdogTask, 0, len(overview.Tasks)+len(overview.ActiveTasks))
	for _, task := range overview.allTasks() {
		if canonicalConversationURL != "" && canonicalConversationURL == canonicalWatchdogURL(task.TargetURL) {
			sameURL = append(sameURL, task)
		}
	}
	if len(sameURL) == 0 {
		return watchdogTask{}, errors.New("no watchdog task is bound to this conversation")
	}
	if boundTaskID != "" {
		for _, task := range sameURL {
			if task.ID == boundTaskID {
				return task, nil
			}
		}
		return watchdogTask{}, fmt.Errorf("bound watchdog task %q is not present for this conversation", boundTaskID)
	}
	running := make([]watchdogTask, 0, len(sameURL))
	for _, task := range sameURL {
		if task.Running {
			running = append(running, task)
		}
	}
	if len(running) > 1 {
		// 多个同 URL 活动任务存在时立即拒绝，绝不回退到唯一可恢复断点。
		return watchdogTask{}, errors.New("ambiguous watchdog task selection for this conversation")
	}
	if len(running) == 1 {
		return running[0], nil
	}
	candidates := make([]watchdogTask, 0, len(sameURL))
	for _, task := range sameURL {
		if watchdogFinalStates[task.State] {
			continue // 完成/中止历史不参与无绑定选择。
		}
		candidates = append(candidates, task)
	}
	if len(candidates) == 0 {
		// 只有终态历史：返回唯一终态任务，让 action 映射给出明确的拒绝原因。
		if len(sameURL) == 1 {
			return sameURL[0], nil
		}
		return watchdogTask{}, errors.New("ambiguous watchdog task selection for this conversation")
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	resumable := make([]watchdogTask, 0, len(candidates))
	for _, task := range candidates {
		if task.Resumable && !task.Running {
			resumable = append(resumable, task)
		}
	}
	if len(resumable) == 1 {
		return resumable[0], nil
	}
	return watchdogTask{}, errors.New("ambiguous watchdog task selection for this conversation")
}

// canonicalWatchdogURL mirrors the store canonicalization so dashboard
// target_url values compare with the bound conversation URL.
func canonicalWatchdogURL(raw string) string {
	return canonicalConversationURL(raw)
}

// watchdogDashboardActionFor maps a requested control verb to the dashboard
// action and the desired observed state class used for the effective check.
// pause/interrupt only ever target running tasks. Resume is decided from the
// live task flags, never from the state alone: running paused tasks resume
// through "resume"; stopped tasks resume through "resume_checkpoint" only when
// the dashboard reports resumable=true; only final DONE/ABORTED states refuse
// recovery (matching dashboard.py resume_task).
func watchdogDashboardActionFor(requested string, task watchdogTask) (action, desired string, err error) {
	switch requested {
	case "pause":
		if !task.Running {
			return "", "", fmt.Errorf("watchdog task %q is not running and cannot be paused", task.ID)
		}
		return watchdogDashboardActionPause, watchdogTaskPaused, nil
	case "interrupt":
		if !task.Running {
			return "", "", fmt.Errorf("watchdog task %q is not running and cannot be interrupted", task.ID)
		}
		return watchdogDashboardActionPauseImmediate, watchdogTaskPausedInterrupted, nil
	case "abort":
		return watchdogDashboardActionAbort, watchdogTaskAborted, nil
	case "resume":
		if task.Running {
			if task.State == watchdogTaskPaused || task.State == watchdogTaskPausedInterrupted {
				return watchdogDashboardActionResume, watchdogTaskActive, nil
			}
			return "", "", fmt.Errorf("watchdog task %q is already running", task.ID)
		}
		if watchdogFinalStates[task.State] {
			return "", "", fmt.Errorf("watchdog task %q is in final state %s and cannot be resumed", task.ID, task.State)
		}
		if !task.Resumable {
			return "", "", fmt.Errorf("watchdog task %q has no eligible checkpoint and cannot be resumed", task.ID)
		}
		return watchdogDashboardActionResumeCheckpoint, watchdogTaskActive, nil
	default:
		return "", "", fmt.Errorf("unsupported watchdog control action %q", requested)
	}
}

// watchdogControl executes one scoped Watchdog control verb for the caller's
// bound conversation and returns the accepted + observed structured result.
// effective is true only when the state observed after the request reached the
// desired class; otherwise the request is reported as pending.
func (r *Runtime) watchdogControl(ctx context.Context, info MCPClientContext, requested string) (Result, error) {
	if r == nil || r.watchdog == nil {
		return nil, toolError("WATCHDOG_DASHBOARD_UNAVAILABLE", "Watchdog dashboard is unavailable", "runtime")
	}
	if r == nil || r.mcpSessions == nil {
		return nil, toolError("MCP_SESSION_AWARENESS_UNAVAILABLE", "MCP session awareness is unavailable", "runtime")
	}
	record, ok := r.mcpSessions.current(info.SessionID)
	if !ok {
		return nil, toolError("MCP_CLIENT_SESSION_UNAVAILABLE", "current MCP client session has not been observed", "runtime")
	}
	if record.ConversationURL == "" {
		return nil, toolError("MCP_SESSION_NOT_BOUND", "current MCP client session is not bound to a conversation", "validation")
	}
	canonicalURL := canonicalConversationURL(record.ConversationURL)
	overview, err := r.watchdog.fetchOverview(ctx)
	if err != nil {
		return nil, toolError("WATCHDOG_DASHBOARD_UNAVAILABLE", err.Error(), "runtime")
	}
	target, err := resolveWatchdogTask(overview, canonicalURL, record.WatchdogTaskID)
	if err != nil {
		return nil, toolError("WATCHDOG_TARGET_UNAVAILABLE", err.Error(), "validation")
	}
	before := target.State
	action, desired, err := watchdogDashboardActionFor(requested, target)
	if err != nil {
		return nil, toolError("WATCHDOG_ACTION_REFUSED", err.Error(), "validation")
	}
	if err := r.watchdog.postAction(ctx, action, target.ID); err != nil {
		return nil, toolError("WATCHDOG_ACTION_FAILED", err.Error(), "runtime")
	}
	// 已受理后的观测失败绝不能丢失受理事实：保留 accepted=true，
	// effective=false 并注明观测不可用，避免客户端盲目重试。
	after := ""
	afterTask := watchdogTask{}
	afterOverview, err := r.watchdog.fetchOverview(ctx)
	if err != nil {
		watchdog := Result{
			"task_id":           target.ID,
			"task_title":        target.TargetTitle,
			"conversation_url":  canonicalURL,
			"task_state_before": before,
			"task_state_after":  "",
			"desired_state":     desired,
			"accepted":          true,
			"effective":         false,
			"note":              "observation unavailable: dashboard overview could not be read after the action",
		}
		return Result{
			"action":     requested,
			"session":    publicMCPClientSession(record),
			"state_file": r.mcpSessions.Path(),
			"watchdog":   watchdog,
		}, nil
	}
	afterTask, err = resolveWatchdogTask(afterOverview, canonicalURL, target.ID)
	if err == nil {
		after = afterTask.State
	} else if afterTask.ID != "" {
		after = afterTask.State
	}
	effective := desired != "" && after == desired
	if requested == "resume" {
		// resume 生效必须是真实活动任务：过期的 READY 且 running=false
		// 不能宣称已恢复。
		effective = effective && afterTask.Running
	}
	watchdog := Result{
		"task_id":           target.ID,
		"task_title":        target.TargetTitle,
		"conversation_url":  canonicalURL,
		"task_state_before": before,
		"task_state_after":  after,
		"desired_state":     desired,
		"accepted":          true,
		"effective":         effective,
	}
	if !effective {
		watchdog["note"] = "pending: dashboard has not yet reported the desired task state"
	}
	return Result{
		"action":     requested,
		"session":    publicMCPClientSession(record),
		"state_file": r.mcpSessions.Path(),
		"watchdog":   watchdog,
	}, nil
}

// watchdogStatus reads the current binding and Watchdog task state without
// mutating anything. Dashboard failures degrade to an honest status field
// instead of failing the readback.
func (r *Runtime) watchdogStatus(ctx context.Context, info MCPClientContext) (Result, error) {
	if r == nil || r.mcpSessions == nil {
		return nil, toolError("MCP_SESSION_AWARENESS_UNAVAILABLE", "MCP session awareness is unavailable", "runtime")
	}
	record, ok := r.mcpSessions.current(info.SessionID)
	if !ok {
		return nil, toolError("MCP_CLIENT_SESSION_UNAVAILABLE", "current MCP client session has not been observed", "runtime")
	}
	watchdog := Result{
		"task_id":          record.WatchdogTaskID,
		"conversation_url": canonicalConversationURL(record.ConversationURL),
		"note":             "no watchdog task observed",
	}
	if record.ConversationURL != "" && r.watchdog != nil {
		overview, err := r.watchdog.fetchOverview(ctx)
		if err != nil {
			watchdog["note"] = "watchdog dashboard unavailable"
		} else if target, err := resolveWatchdogTask(overview, canonicalConversationURL(record.ConversationURL), record.WatchdogTaskID); err == nil {
			watchdog["task_id"] = target.ID
			watchdog["task_title"] = target.TargetTitle
			watchdog["task_state"] = target.State
			watchdog["task_raw_state"] = target.RawState
			watchdog["running"] = target.Running
			watchdog["resumable"] = target.Resumable
			delete(watchdog, "note")
		} else {
			watchdog["note"] = err.Error()
		}
	}
	return Result{
		"action":     "status",
		"session":    publicMCPClientSession(record),
		"state_file": r.mcpSessions.Path(),
		"watchdog":   watchdog,
	}, nil
}

// mcpSessionControl is the dedicated Watchdog session control entrypoint. It
// shares the exact implementation with agentdock_context control actions: the
// target is always the caller's own bound conversation, never an arbitrary
// task id argument.
func (r *Runtime) mcpSessionControl(ctx context.Context, args map[string]any) (Result, error) {
	info, ok := MCPClientContextFromContext(ctx)
	if !ok {
		return nil, toolError(
			"MCP_CLIENT_SESSION_UNAVAILABLE",
			"this request did not include an MCP client session identity",
			"runtime",
		)
	}
	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
	switch action {
	case "status":
		return r.watchdogStatus(ctx, info)
	case "pause", "interrupt", "resume", "abort":
		return r.watchdogControl(ctx, info, action)
	default:
		return nil, toolError("INVALID_ARGUMENT", "unknown mcp_session_control action: "+action, "validation")
	}
}
