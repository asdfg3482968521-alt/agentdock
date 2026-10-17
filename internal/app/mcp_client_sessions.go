package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

const (
	mcpClientSessionStateVersion = 1
	mcpClientSessionFileName     = "mcp-client-sessions.json"
	mcpClientActivityLogFileName = "mcp-call.log"
)

type mcpClientContextKey struct{}

// MCPClientContext is the small, non-secret subset of MCP caller metadata that
// AgentDock propagates from the protocol boundary into Runtime tool handlers.
// SessionID is an opaque client-provided identifier such as OpenAI's
// _meta["openai/session"]. It is never accepted as a normal tool argument.
// ConversationURL, ConversationTitle and WatchdogTaskID are protocol-owned
// _meta["agentdock/*"] hints captured the same way; they are used only for
// first-call auto binding and are never accepted as identity arguments.
type MCPClientContext struct {
	SessionID         string
	ClientName        string
	ClientVersion     string
	ProtocolVersion   string
	ConversationURL   string
	ConversationTitle string
	WatchdogTaskID    string
}

// WithMCPClientContext attaches protocol-owned caller metadata to a tool
// invocation. This prevents callers from spoofing session identity through
// ordinary tool arguments.
func WithMCPClientContext(ctx context.Context, info MCPClientContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	info.SessionID = strings.TrimSpace(info.SessionID)
	info.ClientName = strings.TrimSpace(info.ClientName)
	info.ClientVersion = strings.TrimSpace(info.ClientVersion)
	info.ProtocolVersion = strings.TrimSpace(info.ProtocolVersion)
	info.ConversationURL = strings.TrimSpace(info.ConversationURL)
	info.ConversationTitle = strings.TrimSpace(info.ConversationTitle)
	info.WatchdogTaskID = strings.TrimSpace(info.WatchdogTaskID)
	if info.SessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, mcpClientContextKey{}, info)
}

// MCPClientContextFromContext returns MCP caller metadata captured at the
// protocol boundary.
func MCPClientContextFromContext(ctx context.Context) (MCPClientContext, bool) {
	if ctx == nil {
		return MCPClientContext{}, false
	}
	info, ok := ctx.Value(mcpClientContextKey{}).(MCPClientContext)
	if !ok || strings.TrimSpace(info.SessionID) == "" {
		return MCPClientContext{}, false
	}
	return info, true
}

type MCPClientSessionRecord struct {
	SessionKey        string `json:"session_key"`
	SessionIDMasked   string `json:"session_id_masked"`
	ClientName        string `json:"client_name,omitempty"`
	ClientVersion     string `json:"client_version,omitempty"`
	ProtocolVersion   string `json:"protocol_version,omitempty"`
	FirstSeenAt       string `json:"first_seen_at"`
	LastSeenAt        string `json:"last_seen_at"`
	LastTool          string `json:"last_tool,omitempty"`
	LastRequestID     string `json:"last_request_id,omitempty"`
	ToolCalls         uint64 `json:"tool_calls"`
	ActiveCalls       int    `json:"active_calls"`
	LastStartedAt     string `json:"last_started_at,omitempty"`
	LastFinishedAt    string `json:"last_finished_at,omitempty"`
	LastDurationMS    int64  `json:"last_duration_ms,omitempty"`
	LastOK            bool   `json:"last_ok"`
	ConversationURL   string `json:"conversation_url,omitempty"`
	ConversationTitle string `json:"conversation_title,omitempty"`
	WatchdogTaskID    string `json:"watchdog_task_id,omitempty"`
	BoundAt           string `json:"bound_at,omitempty"`
}

type mcpClientSessionState struct {
	Version   int                      `json:"version"`
	UpdatedAt string                   `json:"updated_at"`
	Sessions  []MCPClientSessionRecord `json:"sessions"`
}

type MCPClientSessionStore struct {
	mu       sync.Mutex
	path     string
	logPath  string
	sessions map[string]MCPClientSessionRecord
}

func newMCPClientSessionStore(agentDockHome string) (*MCPClientSessionStore, error) {
	home := strings.TrimSpace(agentDockHome)
	if home == "" {
		return nil, errors.New("AgentDock home is required for MCP session awareness")
	}
	store := &MCPClientSessionStore{
		path:     filepath.Join(home, mcpClientSessionFileName),
		logPath:  filepath.Join(home, mcpClientActivityLogFileName),
		sessions: map[string]MCPClientSessionRecord{},
	}
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return store, fmt.Errorf("read MCP client session state: %w", err)
	}
	if len(data) == 0 {
		return store, nil
	}
	var persisted mcpClientSessionState
	if err := json.Unmarshal(data, &persisted); err != nil {
		return store, fmt.Errorf("decode MCP client session state: %w", err)
	}
	for _, record := range persisted.Sessions {
		record.SessionKey = strings.TrimSpace(record.SessionKey)
		if record.SessionKey == "" {
			continue
		}
		// In-flight calls cannot survive an AgentDock process restart.
		record.ActiveCalls = 0
		store.sessions[record.SessionKey] = record
	}
	return store, nil
}

func mcpSessionKey(sessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return "mcp_" + hex.EncodeToString(sum[:10])
}

func maskMCPSessionID(sessionID string) string {
	value := strings.TrimSpace(sessionID)
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "***"
	}
	if len(value) <= 10 {
		return value[:2] + "…" + value[len(value)-2:]
	}
	return value[:5] + "…" + value[len(value)-4:]
}

func (s *MCPClientSessionStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *MCPClientSessionStore) ActivityLogPath() string {
	if s == nil {
		return ""
	}
	return s.logPath
}

func (s *MCPClientSessionStore) record(info MCPClientContext, tool, requestID string) (MCPClientSessionRecord, error) {
	if s == nil {
		return MCPClientSessionRecord{}, errors.New("MCP client session store is unavailable")
	}
	sessionID := strings.TrimSpace(info.SessionID)
	if sessionID == "" {
		return MCPClientSessionRecord{}, errors.New("MCP client session id is unavailable")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	sessionKey := mcpSessionKey(sessionID)
	record := s.sessions[sessionKey]
	if record.SessionKey == "" {
		record.SessionKey = sessionKey
		record.SessionIDMasked = maskMCPSessionID(sessionID)
		record.FirstSeenAt = now
	}
	record.LastSeenAt = now
	record.LastStartedAt = now
	record.LastTool = strings.TrimSpace(tool)
	record.LastRequestID = strings.TrimSpace(requestID)
	record.ToolCalls++
	record.ActiveCalls++
	if strings.TrimSpace(info.ClientName) != "" {
		record.ClientName = strings.TrimSpace(info.ClientName)
	}
	if strings.TrimSpace(info.ClientVersion) != "" {
		record.ClientVersion = strings.TrimSpace(info.ClientVersion)
	}
	if strings.TrimSpace(info.ProtocolVersion) != "" {
		record.ProtocolVersion = strings.TrimSpace(info.ProtocolVersion)
	}
	s.sessions[sessionKey] = record
	if err := s.persistLocked(); err != nil {
		return record, err
	}
	return record, nil
}

func (s *MCPClientSessionStore) finish(sessionID string, duration time.Duration, ok bool) (MCPClientSessionRecord, error) {
	if s == nil {
		return MCPClientSessionRecord{}, errors.New("MCP client session store is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return MCPClientSessionRecord{}, errors.New("MCP client session id is unavailable")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessionKey := mcpSessionKey(sessionID)
	record, exists := s.sessions[sessionKey]
	if !exists {
		return MCPClientSessionRecord{}, errors.New("MCP client session was not recorded before finish")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if record.ActiveCalls > 0 {
		record.ActiveCalls--
	}
	record.LastSeenAt = now
	record.LastFinishedAt = now
	record.LastDurationMS = duration.Milliseconds()
	record.LastOK = ok
	s.sessions[sessionKey] = record
	if err := s.persistLocked(); err != nil {
		return record, err
	}
	if err := s.appendActivityLogLocked(record); err != nil {
		return record, err
	}
	return record, nil
}

func (s *MCPClientSessionStore) appendActivityLogLocked(record MCPClientSessionRecord) error {
	status := "OK"
	if !record.LastOK {
		status = "ERR"
	}
	label := strings.TrimSpace(record.ConversationTitle)
	if label == "" {
		label = strings.TrimSpace(record.WatchdogTaskID)
	}
	if label == "" {
		label = "-"
	}
	line := fmt.Sprintf(
		"%s | %s | %s | %s | %dms | %s\n",
		time.Now().UTC().Format(time.RFC3339),
		record.SessionKey,
		record.LastTool,
		status,
		record.LastDurationMS,
		label,
	)
	file, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open MCP activity log: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(line); err != nil {
		return fmt.Errorf("append MCP activity log: %w", err)
	}
	return nil
}

func (s *MCPClientSessionStore) bind(sessionID, conversationURL, conversationTitle, watchdogTaskID string) (MCPClientSessionRecord, error) {
	if s == nil {
		return MCPClientSessionRecord{}, errors.New("MCP client session store is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return MCPClientSessionRecord{}, errors.New("MCP client session id is unavailable")
	}
	conversationURL = strings.TrimSpace(conversationURL)
	conversationTitle = strings.TrimSpace(conversationTitle)
	watchdogTaskID = strings.TrimSpace(watchdogTaskID)

	if err := validateChatGPTConversationURL(conversationURL); err != nil {
		return MCPClientSessionRecord{}, err
	}
	if len(conversationTitle) > 512 {
		return MCPClientSessionRecord{}, errors.New("conversation_title exceeds 512 characters")
	}
	if len(watchdogTaskID) > 128 {
		return MCPClientSessionRecord{}, errors.New("watchdog_task_id exceeds 128 characters")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	sessionKey := mcpSessionKey(sessionID)
	record := s.sessions[sessionKey]
	if record.SessionKey == "" {
		record.SessionKey = sessionKey
		record.SessionIDMasked = maskMCPSessionID(sessionID)
		record.FirstSeenAt = now
		record.LastSeenAt = now
	}
	record.ConversationURL = canonicalConversationURL(conversationURL)
	record.ConversationTitle = conversationTitle
	record.WatchdogTaskID = watchdogTaskID
	record.BoundAt = now
	s.sessions[sessionKey] = record
	if err := s.persistLocked(); err != nil {
		return record, err
	}
	return record, nil
}

// autoBind applies the protocol-owned first-call binding in one atomic step:
// an unbound session is bound deterministically from the caller metadata; an
// already bound session is idempotent when the metadata agrees (stable
// bound_at) and returns a conflict error otherwise. Invalid or absent
// conversation URLs never bind and never fail the business call.
func (s *MCPClientSessionStore) autoBind(info MCPClientContext) (MCPClientSessionRecord, error) {
	if s == nil {
		return MCPClientSessionRecord{}, errors.New("MCP client session store is unavailable")
	}
	sessionID := strings.TrimSpace(info.SessionID)
	if sessionID == "" {
		return MCPClientSessionRecord{}, errors.New("MCP client session id is unavailable")
	}
	conversationURL := strings.TrimSpace(info.ConversationURL)
	conversationTitle := strings.TrimSpace(info.ConversationTitle)
	watchdogTaskID := strings.TrimSpace(info.WatchdogTaskID)
	if conversationURL == "" {
		return MCPClientSessionRecord{}, nil
	}
	if err := validateChatGPTConversationURL(conversationURL); err != nil {
		// 无效的协议元数据 URL 视同缺席：保持匿名跟踪，不阻断业务调用。
		return MCPClientSessionRecord{}, nil
	}
	if len(conversationTitle) > 512 || len(watchdogTaskID) > 128 {
		return MCPClientSessionRecord{}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	sessionKey := mcpSessionKey(sessionID)
	record := s.sessions[sessionKey]
	if record.SessionKey == "" {
		record.SessionKey = sessionKey
		record.SessionIDMasked = maskMCPSessionID(sessionID)
		record.FirstSeenAt = now
		record.LastSeenAt = now
	}
	canonical := canonicalConversationURL(conversationURL)
	if record.ConversationURL == "" {
		record.ConversationURL = canonical
		record.ConversationTitle = conversationTitle
		record.WatchdogTaskID = watchdogTaskID
		record.BoundAt = now
		s.sessions[sessionKey] = record
		if err := s.persistLocked(); err != nil {
			return record, err
		}
		return record, nil
	}
	if record.ConversationURL != canonical {
		return record, errors.New("binding conflict: protocol conversation metadata disagrees with the existing binding")
	}
	if watchdogTaskID != "" && record.WatchdogTaskID != "" && record.WatchdogTaskID != watchdogTaskID {
		return record, errors.New("binding conflict: protocol watchdog task metadata disagrees with the existing binding")
	}
	changed := false
	if conversationTitle != "" && conversationTitle != record.ConversationTitle {
		record.ConversationTitle = conversationTitle
		changed = true
	}
	if watchdogTaskID != "" && record.WatchdogTaskID == "" {
		record.WatchdogTaskID = watchdogTaskID
		changed = true
	}
	if !changed {
		return record, nil
	}
	s.sessions[sessionKey] = record
	if err := s.persistLocked(); err != nil {
		return record, err
	}
	return record, nil
}

// validateChatGPTConversationURL accepts only exact ChatGPT conversation URLs:
// https://chatgpt.com/c/<non-empty id> or
// https://chatgpt.com/g/<project id>/c/<conversation id>, default https port
// only, no user info, no extra path segments, and no escaped delimiters.
// Simple fixture ids such as "abc" are allowed; only the path shape is strict.
func validateChatGPTConversationURL(raw string) error {
	if raw == "" {
		return errors.New("conversation_url is required")
	}
	if len(raw) > 2048 {
		return errors.New("conversation_url exceeds 2048 characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("conversation_url is invalid")
	}
	if parsed.User != nil {
		return errors.New("conversation_url must not contain user info")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "chatgpt.com") {
		return errors.New("conversation_url must be an https://chatgpt.com conversation URL")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return errors.New("conversation_url must use the default https port")
	}
	path := parsed.EscapedPath()
	if strings.Contains(path, "%2f") || strings.Contains(path, "%2F") || strings.Contains(path, "%5c") || strings.Contains(path, "%5C") {
		return errors.New("conversation_url path must not contain escaped delimiters")
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	switch len(segments) {
	case 2: // /c/<id>
		if strings.EqualFold(segments[0], "c") && validChatGPTConversationSegment(segments[1]) {
			return nil
		}
	case 4: // /g/<project>/c/<id>
		if strings.EqualFold(segments[0], "g") && strings.EqualFold(segments[2], "c") &&
			validChatGPTConversationSegment(segments[1]) && validChatGPTConversationSegment(segments[3]) {
			return nil
		}
	}
	return errors.New("conversation_url must identify a ChatGPT conversation")
}

func validChatGPTConversationSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, char := range segment {
		if char <= 0x20 || char == '/' || char == '\\' || char == '%' {
			return false
		}
	}
	return true
}

func canonicalConversationURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if hostname := parsed.Hostname(); hostname != "" {
		// 与 dashboard canonical_chat_url 一致：默认 443 端口不保留，
		// host/scheme 大小写归一。
		host := strings.ToLower(hostname)
		if port := parsed.Port(); port != "" && port != "443" {
			host = host + ":" + port
		}
		parsed.Host = host
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed.String()
}

func (s *MCPClientSessionStore) current(sessionID string) (MCPClientSessionRecord, bool) {
	if s == nil {
		return MCPClientSessionRecord{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return MCPClientSessionRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[mcpSessionKey(sessionID)]
	return record, ok
}

func (s *MCPClientSessionStore) list(limit int, activeWithin time.Duration) []MCPClientSessionRecord {
	if s == nil {
		return []MCPClientSessionRecord{}
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	out := make([]MCPClientSessionRecord, 0, len(s.sessions))
	for _, record := range s.sessions {
		if activeWithin > 0 {
			lastSeen, err := time.Parse(time.RFC3339Nano, record.LastSeenAt)
			if err == nil && now.Sub(lastSeen) > activeWithin {
				continue
			}
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeenAt > out[j].LastSeenAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *MCPClientSessionStore) persistLocked() error {
	state := mcpClientSessionState{
		Version:   mcpClientSessionStateVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Sessions:  make([]MCPClientSessionRecord, 0, len(s.sessions)),
	}
	for _, record := range s.sessions {
		state.Sessions = append(state.Sessions, record)
	}
	sort.Slice(state.Sessions, func(i, j int) bool {
		return state.Sessions[i].LastSeenAt > state.Sessions[j].LastSeenAt
	})
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MCP client session state: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Write(s.path, data, 0o600); err != nil {
		return fmt.Errorf("persist MCP client session state: %w", err)
	}
	return nil
}

func publicMCPClientSession(record MCPClientSessionRecord) Result {
	return Result{
		"session_key":        record.SessionKey,
		"session_id_masked":  record.SessionIDMasked,
		"client_name":        record.ClientName,
		"client_version":     record.ClientVersion,
		"protocol_version":   record.ProtocolVersion,
		"first_seen_at":      record.FirstSeenAt,
		"last_seen_at":       record.LastSeenAt,
		"last_tool":          record.LastTool,
		"last_request_id":    record.LastRequestID,
		"tool_calls":         record.ToolCalls,
		"active_calls":       record.ActiveCalls,
		"last_started_at":    record.LastStartedAt,
		"last_finished_at":   record.LastFinishedAt,
		"last_duration_ms":   record.LastDurationMS,
		"last_ok":            record.LastOK,
		"conversation_url":   record.ConversationURL,
		"conversation_title": record.ConversationTitle,
		"watchdog_task_id":   record.WatchdogTaskID,
		"bound_at":           record.BoundAt,
	}
}

func (r *Runtime) RecordMCPClientCall(info MCPClientContext, tool, requestID string) (Result, error) {
	if r == nil || r.mcpSessions == nil {
		return nil, errors.New("MCP session awareness is unavailable")
	}
	record, err := r.mcpSessions.record(info, tool, requestID)
	if err != nil {
		return publicMCPClientSession(record), err
	}
	return publicMCPClientSession(record), nil
}

func (r *Runtime) FinishMCPClientCall(info MCPClientContext, duration time.Duration, ok bool) (Result, error) {
	if r == nil || r.mcpSessions == nil {
		return nil, errors.New("MCP session awareness is unavailable")
	}
	record, err := r.mcpSessions.finish(info.SessionID, duration, ok)
	if err != nil {
		return publicMCPClientSession(record), err
	}
	return publicMCPClientSession(record), nil
}

// AutoBindMCPClientSession applies protocol-owned first-call auto binding from
// MCP request metadata. It is idempotent for agreeing metadata (stable
// bound_at) and rejects conflicting URL/task metadata before any business tool
// side effect. Anonymous sessions without a valid conversation URL are never
// bound and never fail the call.
func (r *Runtime) AutoBindMCPClientSession(info MCPClientContext) error {
	if r == nil || r.mcpSessions == nil {
		return toolError(
			"MCP_SESSION_AWARENESS_UNAVAILABLE",
			"MCP session awareness is unavailable",
			"runtime",
		)
	}
	if _, err := r.mcpSessions.autoBind(info); err != nil {
		if strings.HasPrefix(err.Error(), "binding conflict") {
			return toolError("MCP_SESSION_BINDING_CONFLICT", err.Error(), "validation")
		}
		return toolError("MCP_SESSION_BINDING_FAILED", err.Error(), "runtime")
	}
	return nil
}

func (r *Runtime) mcpSessionCurrent(ctx context.Context, _ map[string]any) (Result, error) {
	info, ok := MCPClientContextFromContext(ctx)
	if !ok {
		return nil, toolError(
			"MCP_CLIENT_SESSION_UNAVAILABLE",
			"this request did not include an MCP client session identity",
			"runtime",
		)
	}
	if r == nil || r.mcpSessions == nil {
		return nil, toolError(
			"MCP_SESSION_AWARENESS_UNAVAILABLE",
			"MCP session awareness is unavailable",
			"runtime",
		)
	}
	record, ok := r.mcpSessions.current(info.SessionID)
	if !ok {
		return nil, toolError(
			"MCP_CLIENT_SESSION_UNAVAILABLE",
			"current MCP client session has not been observed",
			"runtime",
		)
	}
	return Result{
		"session":    publicMCPClientSession(record),
		"state_file": r.mcpSessions.Path(),
	}, nil
}

func (r *Runtime) mcpSessionBind(ctx context.Context, args map[string]any) (Result, error) {
	info, ok := MCPClientContextFromContext(ctx)
	if !ok {
		return nil, toolError(
			"MCP_CLIENT_SESSION_UNAVAILABLE",
			"this request did not include an MCP client session identity",
			"runtime",
		)
	}
	if r == nil || r.mcpSessions == nil {
		return nil, toolError(
			"MCP_SESSION_AWARENESS_UNAVAILABLE",
			"MCP session awareness is unavailable",
			"runtime",
		)
	}
	record, err := r.mcpSessions.bind(
		info.SessionID,
		stringArg(args, "conversation_url"),
		stringArg(args, "conversation_title"),
		stringArg(args, "watchdog_task_id"),
	)
	if err != nil {
		return nil, toolError("INVALID_ARGUMENT", err.Error(), "validation")
	}
	return Result{
		"session":    publicMCPClientSession(record),
		"state_file": r.mcpSessions.Path(),
	}, nil
}

func (r *Runtime) mcpSessionList(_ context.Context, args map[string]any) (Result, error) {
	if r == nil || r.mcpSessions == nil {
		return nil, toolError(
			"MCP_SESSION_AWARENESS_UNAVAILABLE",
			"MCP session awareness is unavailable",
			"runtime",
		)
	}
	limit := intArg(args, "limit", 100)
	if limit < 1 || limit > 200 {
		return nil, toolError("INVALID_ARGUMENT", "limit must be between 1 and 200", "validation")
	}
	activeSeconds := intArg(args, "active_within_seconds", 0)
	if activeSeconds < 0 || activeSeconds > 86400 {
		return nil, toolError("INVALID_ARGUMENT", "active_within_seconds must be between 0 and 86400", "validation")
	}
	records := r.mcpSessions.list(limit, time.Duration(activeSeconds)*time.Second)
	sessions := make([]Result, 0, len(records))
	for _, record := range records {
		sessions = append(sessions, publicMCPClientSession(record))
	}
	return Result{
		"sessions":   sessions,
		"count":      len(sessions),
		"state_file": r.mcpSessions.Path(),
	}, nil
}

func stringArg(args map[string]any, name string) string {
	if args == nil {
		return ""
	}
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}

func intArg(args map[string]any, name string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch value := args[name].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			return int(parsed)
		}
	}
	return fallback
}
