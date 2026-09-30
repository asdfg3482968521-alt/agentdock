package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/uvwt/agentdock/internal/agent"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

type publicTestProvider struct {
	kind       string
	calls      atomic.Int32
	started    chan string
	mu         sync.Mutex
	release    map[string]chan struct{}
	cancelHold chan struct{}
}

func (p *publicTestProvider) ResolveModel(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "default" {
		return p.kind + "-deterministic-v1", nil
	}
	if raw == p.kind+"-deterministic-v1" || raw == p.kind+"-alt-v1" {
		return raw, nil
	}
	return "", agentruntime.ErrModelUnavailable
}

func (p *publicTestProvider) Run(ctx context.Context, request agentruntime.ProviderRequest, emit func(agentruntime.ProviderEvent) error) (agentruntime.ProviderResult, error) {
	p.calls.Add(1)
	if p.started != nil {
		select {
		case p.started <- request.AgentID:
		default:
		}
	}
	if strings.Contains(request.Prompt, "block") {
		p.mu.Lock()
		ch := p.release[request.AgentID]
		if ch == nil {
			ch = make(chan struct{})
			p.release[request.AgentID] = ch
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			p.mu.Lock()
			hold := p.cancelHold
			p.mu.Unlock()
			if hold != nil {
				<-hold
			}
			return agentruntime.ProviderResult{}, ctx.Err()
		case <-ch:
		}
	}
	_ = emit(agentruntime.ProviderEvent{Kind: agentruntime.ProviderEventText, Text: "public-output-" + request.AgentID})
	return agentruntime.ProviderResult{
		Summary: "public-result-" + request.AgentID, ResolvedModel: request.Model, StopReason: "stop",
		Usage: agentruntime.Usage{InputTokens: 7, OutputTokens: 5, TotalTokens: 12, Known: true},
	}, nil
}

func newPublicAgentRuntime(t *testing.T) (*Runtime, string, *publicTestProvider, *publicTestProvider, sessionruntime.Record, sessionruntime.Record) {
	t.Helper()
	rt, root := newSessionToolsRuntime(t)
	if rt.agents != nil {
		_ = rt.agents.Close()
	}
	store := newTestCredentialStore()
	if _, err := store.Put(context.Background(), "cred_fake_default", "fake", []byte("D2_TEST_SECRET_public_default")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "cred_fake2_default", "fake2", []byte("D2_TEST_SECRET_public_second")); err != nil {
		t.Fatal(err)
	}
	rt.credentialStore = store
	fake := &publicTestProvider{kind: "fake", started: make(chan string, 32), release: map[string]chan struct{}{}}
	fake2 := &publicTestProvider{kind: "fake2", started: make(chan string, 32), release: map[string]chan struct{}{}}
	ids := atomic.Int32{}
	manager, err := agentruntime.NewManager(context.Background(), agentruntime.ManagerOptions{
		StorageRoot: filepath.Join(root, "public-agents"), Sessions: newAgentSessionResolver(rt), Credentials: store,
		Providers: map[string]agentruntime.ProviderAdapter{"fake": fake, "fake2": fake2}, Tools: newAgentToolBridge(rt), Limits: agentruntime.DefaultAgentLimits(),
		IDFactory: func() string {
			return "agent_public_" + time.Now().UTC().Format("150405.000000000") + "_" + string(rune('a'+ids.Add(1)))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.agents = manager
	aDir := filepath.Join(root, "public-a")
	bDir := filepath.Join(root, "public-b")
	if err := ensureDir(aDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(bDir); err != nil {
		t.Fatal(err)
	}
	a := mustOpenSession(t, rt, map[string]any{"project_id": "public-a", "repo_root": aDir})
	b := mustOpenSession(t, rt, map[string]any{"project_id": "public-b", "repo_root": bDir})
	return rt, root, fake, fake2, a, b
}

func validPublicAgentStart(sessionID string) map[string]any {
	return map[string]any{
		"session_id": sessionID, "provider": "fake", "model": nil, "role": "implementation",
		"credential_ref": "cred_fake_default", "prompt": "complete", "permission": "project",
		"timeout_ms": 30000, "budget": map[string]any{"max_requests": 2},
	}
}

func publicAgentFromResult(t *testing.T, result Result) map[string]any {
	t.Helper()
	agent, ok := result["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent result=%#v", result)
	}
	return agent
}

func waitPublicAgentState(t *testing.T, rt *Runtime, sessionID, agentID string, states ...agentruntime.AgentState) agentruntime.Record {
	t.Helper()
	want := map[agentruntime.AgentState]bool{}
	for _, s := range states {
		want[s] = true
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		record, err := rt.agents.Get(sessionID, agentID)
		if err == nil && want[record.State] {
			return record
		}
		time.Sleep(5 * time.Millisecond)
	}
	record, _ := rt.agents.Get(sessionID, agentID)
	t.Fatalf("agent %s state=%s want=%v", agentID, record.State, states)
	return agentruntime.Record{}
}

func TestD2AgentStartPublicHandler(t *testing.T) {
	rt, root, fake, _, a, _ := newPublicAgentRuntime(t)
	beforeCalls := fake.calls.Load()
	result, err := rt.Call(context.Background(), "agent_start", validPublicAgentStart(a.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	agent := publicAgentFromResult(t, result)
	if result["ok"] != true || agent["session_id"] != a.SessionID || agent["model"] != "fake-deterministic-v1" || agent["role"] != "implementation" {
		t.Fatalf("start=%#v", result)
	}
	if strings.Contains(strings.ToLower(strings.Join(mapKeys(agent), " ")), "workspace") {
		t.Fatalf("private workspace leaked: %#v", agent)
	}
	if fake.calls.Load() < beforeCalls {
		t.Fatal("provider call counter regressed")
	}
	waitPublicAgentState(t, rt, a.SessionID, agent["agent_id"].(string), agentruntime.AgentStateSucceeded)
	data, err := filepath.Glob(filepath.Join(root, "public-agents", "records", "*.json"))
	if err != nil || len(data) == 0 {
		t.Fatalf("records=%v err=%v", data, err)
	}
	for _, path := range data {
		body := readPublicAgentRecordFile(t, path)
		if strings.Contains(string(body), "D2_TEST_SECRET_public_default") {
			t.Fatalf("public agent metadata leaked credential plaintext: %s", path)
		}
	}

	missing := validPublicAgentStart(a.SessionID)
	delete(missing, "budget")
	if _, err := rt.Call(context.Background(), "agent_start", missing); toolErrorCode(err) != "INVALID_ARGUMENTS" {
		t.Fatalf("missing code=%s err=%v", toolErrorCode(err), err)
	}
	badBudget := validPublicAgentStart(a.SessionID)
	badBudget["budget"] = map[string]any{"max_requests": 0}
	if _, err := rt.Call(context.Background(), "agent_start", badBudget); toolErrorCode(err) != "INVALID_ARGUMENTS" {
		t.Fatalf("budget code=%s err=%v", toolErrorCode(err), err)
	}
	badRef := validPublicAgentStart(a.SessionID)
	badRef["credential_ref"] = "bad"
	if _, err := rt.Call(context.Background(), "agent_start", badRef); toolErrorCode(err) != "INVALID_ARGUMENTS" {
		t.Fatalf("ref schema code=%s err=%v", toolErrorCode(err), err)
	}
	secret := validPublicAgentStart(a.SessionID)
	secret["api_key"] = "D2_TEST_SECRET_never_accept"
	if _, err := rt.Call(context.Background(), "agent_start", secret); toolErrorCode(err) != "INVALID_ARGUMENTS" {
		t.Fatalf("secret code=%s err=%v", toolErrorCode(err), err)
	}
	missingCred := validPublicAgentStart(a.SessionID)
	missingCred["credential_ref"] = "cred_missing_value"
	missingCredCalls := fake.calls.Load()
	if _, err := rt.Call(context.Background(), "agent_start", missingCred); toolErrorCode(err) != "CREDENTIAL_NOT_FOUND" {
		t.Fatalf("missing credential code=%s err=%v", toolErrorCode(err), err)
	}
	if fake.calls.Load() != missingCredCalls {
		t.Fatal("credential existence check reached provider execution")
	}
	unknownSession := validPublicAgentStart("session_missing")
	if _, err := rt.Call(context.Background(), "agent_start", unknownSession); toolErrorCode(err) != "AGENT_SESSION_CLOSED" {
		t.Fatalf("unknown session code=%s err=%v", toolErrorCode(err), err)
	}
	unsupported := validPublicAgentStart(a.SessionID)
	unsupported["model"] = "fake-unsupported"
	calls := fake.calls.Load()
	beforeAgents := len(rt.agents.List(agentruntime.ListFilter{SessionID: a.SessionID, Limit: 200}))
	if _, err := rt.Call(context.Background(), "agent_start", unsupported); toolErrorCode(err) != "MODEL_UNAVAILABLE" {
		t.Fatalf("model code=%s err=%v", toolErrorCode(err), err)
	}
	if fake.calls.Load() != calls {
		t.Fatalf("unsupported model reached provider network/run path")
	}
	if afterAgents := len(rt.agents.List(agentruntime.ListFilter{SessionID: a.SessionID, Limit: 200})); afterAgents != beforeAgents {
		t.Fatalf("unsupported model created durable agent: before=%d after=%d", beforeAgents, afterAgents)
	}
	omitted := validPublicAgentStart(a.SessionID)
	delete(omitted, "model")
	if got, err := rt.Call(context.Background(), "agent_start", omitted); err != nil || publicAgentFromResult(t, got)["model"] != "fake-deterministic-v1" {
		t.Fatalf("omitted model got=%#v err=%v", got, err)
	}
	explicit := validPublicAgentStart(a.SessionID)
	explicit["model"] = "fake-alt-v1"
	if got, err := rt.Call(context.Background(), "agent_start", explicit); err != nil || publicAgentFromResult(t, got)["model"] != "fake-alt-v1" {
		t.Fatalf("explicit model got=%#v err=%v", got, err)
	}
}

func TestD3AgentStartReplaysSameLogicalStart(t *testing.T) {
	rt, _, _, _, a, _ := newPublicAgentRuntime(t)
	args := validPublicAgentStart(a.SessionID)
	args["client_request_id"] = "logical-start-public-001"
	first, err := rt.Call(context.Background(), "agent_start", args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rt.Call(context.Background(), "agent_start", args)
	if err != nil {
		t.Fatal(err)
	}
	if first["created"] != true || first["replayed"] != false || second["created"] != false || second["replayed"] != true {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if publicAgentFromResult(t, first)["agent_id"] != publicAgentFromResult(t, second)["agent_id"] {
		t.Fatal("replay changed agent id")
	}
}

func TestD2AgentStartRejectsClosedSessionAndPermissionEscalation(t *testing.T) {
	rt, root, _, _, a, _ := newPublicAgentRuntime(t)
	if _, err := rt.sessions.Close(context.Background(), a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Call(context.Background(), "agent_start", validPublicAgentStart(a.SessionID)); toolErrorCode(err) != "AGENT_SESSION_CLOSED" {
		t.Fatalf("closed code=%s err=%v", toolErrorCode(err), err)
	}

	roDir := filepath.Join(root, "public-ro")
	if err := ensureDir(roDir); err != nil {
		t.Fatal(err)
	}
	ro := mustOpenSession(t, rt, map[string]any{"project_id": "public-ro", "repo_root": roDir, "isolation_mode": "shared-readonly"})
	args := validPublicAgentStart(ro.SessionID)
	args["permission"] = "project"
	if _, err := rt.Call(context.Background(), "agent_start", args); toolErrorCode(err) != "AGENT_WRITE_DENIED" {
		t.Fatalf("escalation code=%s err=%v", toolErrorCode(err), err)
	}
}

func TestD2AgentStatusOutputResultCancelOwnershipAndCancellation(t *testing.T) {
	rt, _, _, _, a, b := newPublicAgentRuntime(t)
	args := validPublicAgentStart(a.SessionID)
	args["prompt"] = "block until cancelled"
	started, err := rt.Call(context.Background(), "agent_start", args)
	if err != nil {
		t.Fatal(err)
	}
	agent := publicAgentFromResult(t, started)
	agentID := agent["agent_id"].(string)
	waitPublicAgentState(t, rt, a.SessionID, agentID, agentruntime.AgentStateRunning)

	status, err := rt.Call(context.Background(), "agent_status", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if publicAgentFromResult(t, status)["session_id"] != a.SessionID {
		t.Fatalf("status=%#v", status)
	}
	if _, err := rt.Call(context.Background(), "agent_status", map[string]any{"session_id": b.SessionID, "agent_id": agentID}); toolErrorCode(err) != "AGENT_SESSION_OWNERSHIP_MISMATCH" {
		t.Fatalf("cross status code=%s err=%v", toolErrorCode(err), err)
	}
	if _, err := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": b.SessionID, "agent_id": agentID}); toolErrorCode(err) != "AGENT_SESSION_OWNERSHIP_MISMATCH" {
		t.Fatalf("cross output code=%s err=%v", toolErrorCode(err), err)
	}
	if _, err := rt.Call(context.Background(), "agent_result", map[string]any{"session_id": a.SessionID, "agent_id": agentID}); toolErrorCode(err) != "AGENT_NOT_TERMINAL" {
		t.Fatalf("nonterminal result code=%s err=%v", toolErrorCode(err), err)
	}
	if _, err := rt.Call(context.Background(), "agent_cancel", map[string]any{"session_id": b.SessionID, "agent_id": agentID}); toolErrorCode(err) != "AGENT_SESSION_OWNERSHIP_MISMATCH" {
		t.Fatalf("cross cancel code=%s err=%v", toolErrorCode(err), err)
	}
	cancelled, err := rt.Call(context.Background(), "agent_cancel", map[string]any{"session_id": a.SessionID, "agent_id": agentID, "reason": "test cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled["state"] != string(agentruntime.AgentStateCancelled) || cancelled["already_terminal"] != false {
		t.Fatalf("cancel=%#v", cancelled)
	}
	terminal := waitPublicAgentState(t, rt, a.SessionID, agentID, agentruntime.AgentStateCancelled)
	if !terminal.State.Terminal() {
		t.Fatalf("cancel not terminal: %s", terminal.State)
	}
	again, err := rt.Call(context.Background(), "agent_cancel", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if again["already_terminal"] != true || again["state"] != string(agentruntime.AgentStateCancelled) {
		t.Fatalf("duplicate cancel=%#v", again)
	}
	result, err := rt.Call(context.Background(), "agent_result", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if result["state"] != string(agentruntime.AgentStateCancelled) || result["session_id"] != a.SessionID {
		t.Fatalf("result=%#v", result)
	}
}

func TestD2AgentCancelDuplicateCancelRequestedIsIdempotent(t *testing.T) {
	rt, _, fake, _, a, _ := newPublicAgentRuntime(t)
	fake.mu.Lock()
	fake.cancelHold = make(chan struct{})
	hold := fake.cancelHold
	fake.mu.Unlock()
	args := validPublicAgentStart(a.SessionID)
	args["prompt"] = "block for duplicate cancel"
	started, err := rt.Call(context.Background(), "agent_start", args)
	if err != nil {
		t.Fatal(err)
	}
	agentID := publicAgentFromResult(t, started)["agent_id"].(string)
	waitPublicAgentState(t, rt, a.SessionID, agentID, agentruntime.AgentStateRunning)
	select {
	case startedID := <-fake.started:
		if startedID != agentID {
			t.Fatalf("provider started unexpected agent %s", startedID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not enter Run")
	}
	type cancelResult struct {
		result Result
		err    error
	}
	firstDone := make(chan cancelResult, 1)
	go func() {
		result, err := rt.Call(context.Background(), "agent_cancel", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
		firstDone <- cancelResult{result: result, err: err}
	}()
	waitPublicAgentState(t, rt, a.SessionID, agentID, agentruntime.AgentStateCancelRequested)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	duplicate, err := rt.Call(ctx, "agent_cancel", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		close(hold)
		t.Fatalf("duplicate cancel error=%v", err)
	}
	if duplicate["state"] != string(agentruntime.AgentStateCancelRequested) || duplicate["already_terminal"] != false {
		close(hold)
		t.Fatalf("duplicate cancel=%#v", duplicate)
	}
	close(hold)
	first := <-firstDone
	if first.err != nil || first.result["state"] != string(agentruntime.AgentStateCancelled) {
		t.Fatalf("first cancel result=%#v err=%v", first.result, first.err)
	}
}

func TestD2AgentOutputCursorIsIncremental(t *testing.T) {
	rt, _, _, _, a, _ := newPublicAgentRuntime(t)
	started, err := rt.Call(context.Background(), "agent_start", validPublicAgentStart(a.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	agentID := publicAgentFromResult(t, started)["agent_id"].(string)
	waitPublicAgentState(t, rt, a.SessionID, agentID, agentruntime.AgentStateSucceeded)
	first, err := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": a.SessionID, "agent_id": agentID, "cursor": 0, "limit": 8})
	if err != nil {
		t.Fatal(err)
	}
	next := publicTestInt64(first["next_cursor"])
	if next <= 0 || publicTestInt64(first["cursor"]) != 0 {
		t.Fatalf("first=%#v", first)
	}
	second, err := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": a.SessionID, "agent_id": agentID, "cursor": next, "limit": 65536})
	if err != nil {
		t.Fatal(err)
	}
	if publicTestInt64(second["next_cursor"]) < next {
		t.Fatalf("cursor regressed: %#v", second)
	}
	combined := first["output"].(string) + second["output"].(string)
	if !strings.Contains(combined, "public-output-") || strings.Contains(combined, "D2_TEST_SECRET") {
		t.Fatalf("output=%q", combined)
	}
	result, err := rt.Call(context.Background(), "agent_result", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if result["state"] != string(agentruntime.AgentStateSucceeded) || !strings.Contains(result["result"].(string), "public-result-") {
		t.Fatalf("success result=%#v", result)
	}
}

func TestD2AgentListFiltersAndStableCursor(t *testing.T) {
	rt, _, _, _, a, b := newPublicAgentRuntime(t)
	start := func(provider, cred string) string {
		args := validPublicAgentStart(a.SessionID)
		args["provider"] = provider
		args["credential_ref"] = cred
		args["model"] = nil
		result, err := rt.Call(context.Background(), "agent_start", args)
		if err != nil {
			t.Fatal(err)
		}
		id := publicAgentFromResult(t, result)["agent_id"].(string)
		waitPublicAgentState(t, rt, a.SessionID, id, agentruntime.AgentStateSucceeded)
		return id
	}
	ids := []string{start("fake", "cred_fake_default"), start("fake2", "cred_fake2_default"), start("fake", "cred_fake_default")}
	bArgs := validPublicAgentStart(b.SessionID)
	bResult, err := rt.Call(context.Background(), "agent_start", bArgs)
	if err != nil {
		t.Fatal(err)
	}
	bID := publicAgentFromResult(t, bResult)["agent_id"].(string)
	waitPublicAgentState(t, rt, b.SessionID, bID, agentruntime.AgentStateSucceeded)
	page1, err := rt.Call(context.Background(), "agent_list", map[string]any{"session_id": a.SessionID, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	agents1 := page1["agents"].([]any)
	if len(agents1) != 1 || page1["next_cursor"] == nil {
		t.Fatalf("page1=%#v", page1)
	}
	cursor1, ok := page1["next_cursor"].(string)
	if !ok || len(cursor1) == 0 || len(cursor1) > 1024 {
		t.Fatalf("cursor is not consumable by frozen schema: %#v", page1["next_cursor"])
	}
	if _, err := rt.Call(context.Background(), "agent_list", map[string]any{"session_id": b.SessionID, "cursor": cursor1}); toolErrorCode(err) != "INVALID_ARGUMENTS" {
		t.Fatalf("cross-session cursor code=%s err=%v", toolErrorCode(err), err)
	}
	page2, err := rt.Call(context.Background(), "agent_list", map[string]any{"session_id": a.SessionID, "limit": 2, "cursor": page1["next_cursor"]})
	if err != nil {
		t.Fatal(err)
	}
	agents2 := page2["agents"].([]any)
	if len(agents2) != 2 {
		t.Fatalf("page2=%#v", page2)
	}
	seen := map[string]bool{}
	for _, item := range append(agents1, agents2...) {
		id := item.(map[string]any)["agent_id"].(string)
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("missing %s from pages", id)
		}
	}
	if seen[bID] {
		t.Fatalf("session B agent leaked into session A listing: %s", bID)
	}
	filtered, err := rt.Call(context.Background(), "agent_list", map[string]any{"session_id": a.SessionID, "provider": "fake2", "state": "SUCCEEDED", "limit": 100})
	if err != nil {
		t.Fatal(err)
	}
	items := filtered["agents"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["provider"] != "fake2" {
		t.Fatalf("filtered=%#v", filtered)
	}
	if _, err := rt.Call(context.Background(), "agent_list", map[string]any{"session_id": a.SessionID, "cursor": "not-a-valid-cursor"}); toolErrorCode(err) != "INVALID_ARGUMENTS" {
		t.Fatalf("bad cursor code=%s err=%v", toolErrorCode(err), err)
	}
}

func TestD2AgentPublicSuccessOutputsMatchFrozenSchemas(t *testing.T) {
	rt, _, _, _, a, _ := newPublicAgentRuntime(t)
	args := validPublicAgentStart(a.SessionID)
	args["prompt"] = "block for output-schema validation"
	start, err := rt.Call(context.Background(), "agent_start", args)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrozenAgentOutput("agent_start", start); err != nil {
		t.Fatalf("agent_start output: %v %#v", err, start)
	}
	agentID := publicAgentFromResult(t, start)["agent_id"].(string)
	waitPublicAgentState(t, rt, a.SessionID, agentID, agentruntime.AgentStateRunning)
	status, err := rt.Call(context.Background(), "agent_status", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrozenAgentOutput("agent_status", status); err != nil {
		t.Fatalf("agent_status output: %v %#v", err, status)
	}
	output, err := rt.Call(context.Background(), "agent_output", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrozenAgentOutput("agent_output", output); err != nil {
		t.Fatalf("agent_output output: %v %#v", err, output)
	}
	cancelled, err := rt.Call(context.Background(), "agent_cancel", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrozenAgentOutput("agent_cancel", cancelled); err != nil {
		t.Fatalf("agent_cancel output: %v %#v", err, cancelled)
	}
	result, err := rt.Call(context.Background(), "agent_result", map[string]any{"session_id": a.SessionID, "agent_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrozenAgentOutput("agent_result", result); err != nil {
		t.Fatalf("agent_result output: %v %#v", err, result)
	}
	list, err := rt.Call(context.Background(), "agent_list", map[string]any{"session_id": a.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrozenAgentOutput("agent_list", list); err != nil {
		t.Fatalf("agent_list output: %v %#v", err, list)
	}
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readPublicAgentRecordFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			return body
		}
		lastErr = err
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("read durable Agent record %s: %v", path, lastErr)
	return nil
}

func publicTestInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		panic("unexpected numeric type")
	}
}

var _ = errors.Is
