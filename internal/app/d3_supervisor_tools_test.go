package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/nativebridge"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

type capturingD3Supervisor struct {
	messageRequest    nativebridge.SupervisorMessageRequest
	correctionRequest nativebridge.SupervisorMessageRequest
	correctionMode    nativebridge.CorrectionMode
	observeSessionID  string
	observeSubagentID string
	observeWorkspace  string
	observeAfter      int64
	observeLimit      int
}

func (c *capturingD3Supervisor) SubmitSupervisorMessage(_ context.Context, request nativebridge.SupervisorMessageRequest) (nativebridge.SupervisorMessageResult, error) {
	c.messageRequest = request
	return nativebridge.SupervisorMessageResult{OpenCodeSessionID: "ses-root", Created: true, Dispatched: true}, nil
}

func (c *capturingD3Supervisor) SubmitSupervisorCorrection(_ context.Context, request nativebridge.SupervisorMessageRequest, mode nativebridge.CorrectionMode) (nativebridge.OrderedCorrectionResult, error) {
	c.correctionRequest = request
	c.correctionMode = mode
	return nativebridge.OrderedCorrectionResult{Mode: mode, OpenCodeSessionIDBefore: "ses-root", OpenCodeSessionIDAfter: "ses-root"}, nil
}

func (c *capturingD3Supervisor) ObserveSupervisor(_ context.Context, sessionID, subagentID, workspace string, after int64, limit int) (nativebridge.SupervisorObservation, error) {
	c.observeSessionID = sessionID
	c.observeSubagentID = subagentID
	c.observeWorkspace = workspace
	c.observeAfter = after
	c.observeLimit = limit
	return nativebridge.SupervisorObservation{RootOpenCodeSessionID: "ses-root"}, nil
}

func TestD3SupervisorToolsAreAdditiveStrictAndDoNotExposeAuthorityOverrides(t *testing.T) {
	for _, name := range []string{"agent_supervisor_message", "agent_supervisor_observe", "agent_supervisor_correct"} {
		if _, ok := toolSpecByName(name); !ok {
			t.Fatalf("missing additive D3 supervisor tool %s", name)
		}
		schema := InputSchema(name)
		if schema["additionalProperties"] != false {
			t.Fatalf("%s must reject undeclared authority inputs: %+v", name, schema)
		}
		props := schema["properties"].(map[string]any)
		for _, forbidden := range []string{"workspace", "permission_mode", "runtime", "runtime_id", "provider", "model", "approval", "always"} {
			if _, exists := props[forbidden]; exists {
				t.Fatalf("%s exposes forbidden authority override %s", name, forbidden)
			}
		}
	}

	message := InputSchema("agent_supervisor_message")
	assertRequiredFields(t, message, "session_id", "subagent_id", "client_message_id", "kind", "text")
	correct := InputSchema("agent_supervisor_correct")
	assertRequiredFields(t, correct, "session_id", "subagent_id", "client_message_id", "mode", "text")
	observe := InputSchema("agent_supervisor_observe")
	assertRequiredFields(t, observe, "session_id", "subagent_id")
}

func TestD3SupervisorHandlersUseCanonicalSessionWorkspaceAndDurableIdentity(t *testing.T) {
	rt, root := newSessionToolsRuntime(t)
	repo := filepath.Join(root, "d3-supervisor")
	if err := ensureDir(repo); err != nil {
		t.Fatal(err)
	}
	record := mustOpenSession(t, rt, map[string]any{"project_id": "d3-supervisor", "repo_root": repo})
	if _, err := rt.sessions.BindNativeSession(record.SessionID, sessionruntime.NativeSessionBinding{
		SubagentID: "subagent-a", AgentDockSessionID: record.SessionID, ClientRequestID: "start-a",
		OpenCodeSessionID: "ses-root", Workspace: record.Workspace(), RuntimeScope: "session:" + record.SessionID,
		PermissionMode: record.PermissionMode, Provider: "jiyuan", Model: "deepseek-flash", Variant: "high", ExecutionMode: "native-session",
		State: sessionruntime.NativeConversationActive, ExecutionState: sessionruntime.NativeExecutionIdle,
	}); err != nil {
		t.Fatal(err)
	}
	fake := &capturingD3Supervisor{}
	rt.nativeSupervisor = fake
	rt.cfg.OpenCodeEnabled = true
	rt.cfg.OpenCodeNativeURL = "http://127.0.0.1:40999"

	message, err := rt.Call(context.Background(), "agent_supervisor_message", map[string]any{
		"session_id": record.SessionID, "subagent_id": "subagent-a", "client_message_id": "client-followup-1", "kind": "FOLLOWUP", "text": "continue from the same root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if message["opencode_session_id"] != "ses-root" || message["created"] != true || message["dispatched"] != true {
		t.Fatalf("unexpected message result: %+v", message)
	}
	if fake.messageRequest.AgentDockSessionID != record.SessionID || fake.messageRequest.SubagentID != "subagent-a" || fake.messageRequest.ClientMessageID != "client-followup-1" || fake.messageRequest.Kind != "FOLLOWUP" || fake.messageRequest.Workspace != record.Workspace() {
		t.Fatalf("message route escaped canonical Session authority: %+v", fake.messageRequest)
	}
	if len(fake.messageRequest.Prompt.Parts) == 0 {
		t.Fatalf("message prompt was not forwarded: %+v", fake.messageRequest.Prompt)
	}
	if fake.messageRequest.Prompt.Model == nil || fake.messageRequest.Prompt.Model.ProviderID != "jiyuan" || fake.messageRequest.Prompt.Model.ModelID != "deepseek-flash" || fake.messageRequest.Prompt.Variant != "high" {
		t.Fatalf("message prompt did not inherit durable binding model/variant: model=%+v variant=%q", fake.messageRequest.Prompt.Model, fake.messageRequest.Prompt.Variant)
	}

	correction, err := rt.Call(context.Background(), "agent_supervisor_correct", map[string]any{
		"session_id": record.SessionID, "subagent_id": "subagent-a", "client_message_id": "client-correction-1", "mode": "force", "text": "stop the wrong direction",
	})
	if err != nil {
		t.Fatal(err)
	}
	if correction["opencode_session_id_before"] != "ses-root" || correction["opencode_session_id_after"] != "ses-root" {
		t.Fatalf("unexpected correction result: %+v", correction)
	}
	if fake.correctionMode != nativebridge.CorrectionForce || fake.correctionRequest.Kind != "CORRECTION" || fake.correctionRequest.Workspace != record.Workspace() || fake.correctionRequest.ClientMessageID != "client-correction-1" {
		t.Fatalf("correction route mismatch: request=%+v mode=%s", fake.correctionRequest, fake.correctionMode)
	}
	if fake.correctionRequest.Prompt.Model == nil || fake.correctionRequest.Prompt.Model.ProviderID != "jiyuan" || fake.correctionRequest.Prompt.Model.ModelID != "deepseek-flash" || fake.correctionRequest.Prompt.Variant != "high" {
		t.Fatalf("correction prompt lost durable model/variant: model=%+v variant=%q", fake.correctionRequest.Prompt.Model, fake.correctionRequest.Prompt.Variant)
	}

	observation, err := rt.Call(context.Background(), "agent_supervisor_observe", map[string]any{
		"session_id": record.SessionID, "subagent_id": "subagent-a", "after_cursor": 7, "limit": 23,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation["root_opencode_session_id"] != "ses-root" || fake.observeSessionID != record.SessionID || fake.observeSubagentID != "subagent-a" || fake.observeWorkspace != record.Workspace() || fake.observeAfter != 7 || fake.observeLimit != 23 {
		t.Fatalf("observe route mismatch: result=%+v fake=%+v", observation, fake)
	}

	if _, err := rt.Call(context.Background(), "agent_supervisor_message", map[string]any{
		"session_id": record.SessionID, "subagent_id": "subagent-a", "client_message_id": "client-followup-2", "kind": "FOLLOWUP", "text": "x", "workspace": filepath.Join(root, "foreign"),
	}); toolErrorCode(err) != "INVALID_ARGUMENTS" {
		t.Fatalf("workspace override must fail strict schema before routing: code=%s err=%v", toolErrorCode(err), err)
	}
}

func TestD3SupervisorToolsStayUnavailableWithoutNativeEndpoint(t *testing.T) {
	rt, _ := newSessionToolsRuntime(t)
	for _, name := range []string{"agent_supervisor_message", "agent_supervisor_observe", "agent_supervisor_correct"} {
		for _, exposed := range rt.ToolNames() {
			if exposed == name {
				t.Fatalf("%s must stay hidden without configured native endpoint", name)
			}
		}
	}
}

var _ d3Supervisor = (*capturingD3Supervisor)(nil)
