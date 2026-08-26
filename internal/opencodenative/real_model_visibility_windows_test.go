package opencodenative

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestD3ERealDefaultModelSessionVisibilityWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("D3-E live gate is Windows-only")
	}
	baseURL := os.Getenv("AGENTDOCK_D3_NATIVE_URL")
	workspace := os.Getenv("AGENTDOCK_D3_NATIVE_WORKSPACE")
	if baseURL == "" || workspace == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL and AGENTDOCK_D3_NATIVE_WORKSPACE for the D3-E live gate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	client, err := NewClient(Config{
		BaseURL:          baseURL,
		ReadinessTimeout: 30 * time.Second,
		InitialBackoff:   100 * time.Millisecond,
		MaxBackoff:       time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WaitReady(ctx, workspace); err != nil {
		t.Fatalf("runtime readiness: %v", err)
	}

	target := ModelRef{ProviderID: "jiyuan", ModelID: "deepseek-v4-flash-0731"}
	discovery, err := client.DiscoverNativeModels(ctx, workspace)
	if err != nil {
		t.Fatalf("model discovery: %v", err)
	}
	if discovery.Configured != (ModelRef{}) && discovery.Configured != target {
		t.Fatalf("explicit configured model=%+v want unset or %+v", discovery.Configured, target)
	}
	var provider *ProviderInfo
	for i := range discovery.Catalog.All {
		if discovery.Catalog.All[i].ID == target.ProviderID {
			provider = &discovery.Catalog.All[i]
			break
		}
	}
	if provider == nil || provider.Source != "config" {
		t.Fatalf("configured provider not discoverable as config source: %+v", provider)
	}
	if _, ok := provider.Models[target.ModelID]; !ok {
		t.Fatalf("target model not available in provider catalog: %+v", target)
	}
	if discovery.Catalog.Default[target.ProviderID] != target.ModelID {
		t.Fatalf("provider default=%q want %q", discovery.Catalog.Default[target.ProviderID], target.ModelID)
	}
	connected := false
	for _, id := range discovery.Catalog.Connected {
		if id == target.ProviderID {
			connected = true
			break
		}
	}
	if !connected {
		t.Fatalf("configured provider is not connected: %s", target.ProviderID)
	}

	created, err := client.CreateNativeSession(ctx, workspace, CreateSessionRequest{
		Title: "D3-E model/session visibility live gate",
		Metadata: map[string]any{
			"agentdock_lane": "D3-E",
			"gate":           "default-model-session-visibility",
		},
	})
	if err != nil {
		t.Fatalf("native session creation: %v", err)
	}
	t.Logf("D3E_SESSION_ID=%s", created.ID)

	listed, err := client.ListNativeSessions(ctx, workspace)
	if err != nil {
		t.Fatalf("session API list visibility: %v", err)
	}
	visible := false
	for _, item := range listed {
		if item.ID == created.ID {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatalf("created native session %s is not visible through /session", created.ID)
	}

	// Intentionally omit PromptRequest.Model. This is the D3-E real default-model gate.
	reply, err := client.SubmitNativeMessage(ctx, workspace, created.ID, TextPrompt("Reply with exactly D3E_DEFAULT_MODEL_OK and nothing else."))
	if err != nil {
		t.Fatalf("real default model execution: %v", err)
	}
	if got, ok := reply.Info.SelectedModel(); !ok || got != target {
		t.Fatalf("actual selected provider/model from assistant reply: got=%+v ok=%v want=%+v", got, ok, target)
	}
	t.Logf("D3E_ACTUAL_SELECTED=%s/%s", reply.Info.ProviderID, reply.Info.ModelID)
	if reply.Info.HasError() {
		if !bytes.Contains(reply.Info.Error, []byte(`"statusCode":401`)) {
			t.Fatalf("default model execution failed for a non-credential reason: %s", reply.Info.Error)
		}
		t.Log("D3E_DEFAULT_MODEL_EXECUTION=REAL_CREDENTIAL_BLOCKER")
	} else {
		if strings.TrimSpace(messageText(reply)) != "D3E_DEFAULT_MODEL_OK" {
			t.Fatalf("default model successful response mismatch: %q", messageText(reply))
		}
		t.Log("D3E_DEFAULT_MODEL_EXECUTION=PASS")
	}

	info, err := client.GetNativeSession(ctx, workspace, created.ID)
	if err != nil {
		t.Fatalf("session API readback: %v", err)
	}
	if got, ok := info.SelectedModel(); !ok || got != target {
		t.Fatalf("session model visibility: got=%+v ok=%v info=%+v", got, ok, info)
	}

	messages, err := client.ListNativeMessages(ctx, workspace, created.ID)
	if err != nil {
		t.Fatalf("message API readback: %v", err)
	}
	var userVisible, assistantVisible, assistantErrorVisible bool
	for _, message := range messages {
		selected, ok := message.Info.SelectedModel()
		if !ok || selected != target {
			continue
		}
		switch message.Info.Role {
		case "user":
			userVisible = true
		case "assistant":
			assistantVisible = true
			if message.Info.HasError() {
				assistantErrorVisible = true
			}
		}
	}
	if !userVisible || !assistantVisible {
		t.Fatalf("message model/provider readback incomplete: user=%v assistant=%v messages=%+v", userVisible, assistantVisible, messages)
	}
	if reply.Info.HasError() && !assistantErrorVisible {
		t.Fatalf("default 401 error state was not visible through message readback: %+v", messages)
	}

	// Recreate the SDK client to prove visibility survives a client/bridge reconnect.
	reconnected, err := NewClient(Config{BaseURL: baseURL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconnected.GetNativeSession(ctx, workspace, created.ID); err != nil {
		t.Fatalf("session visibility after client reconnect: %v", err)
	}
	readback, err := reconnected.ListNativeMessages(ctx, workspace, created.ID)
	if err != nil || len(readback) < 2 {
		t.Fatalf("message visibility after client reconnect: len=%d err=%v", len(readback), err)
	}
	t.Logf("D3E_API_VISIBILITY=PASS messages=%d", len(readback))
}

func TestD3ERealExplicitModelSessionVisibilityWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("D3-E live gate is Windows-only")
	}
	baseURL := os.Getenv("AGENTDOCK_D3_NATIVE_URL")
	workspace := os.Getenv("AGENTDOCK_D3_NATIVE_WORKSPACE")
	if baseURL == "" || workspace == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL and AGENTDOCK_D3_NATIVE_WORKSPACE for the D3-E live gate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	client, err := NewClient(Config{
		BaseURL:          baseURL,
		ReadinessTimeout: 30 * time.Second,
		InitialBackoff:   100 * time.Millisecond,
		MaxBackoff:       time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WaitReady(ctx, workspace); err != nil {
		t.Fatalf("runtime readiness: %v", err)
	}
	discovery, err := client.DiscoverNativeModels(ctx, workspace)
	if err != nil {
		t.Fatalf("model discovery: %v", err)
	}
	target, ok := firstAvailableExplicitModel(discovery)
	if !ok {
		t.Fatal("neither opencode/mimo-v2.5-free nor opencode/x-preview-f-free is available")
	}
	t.Logf("D3E_EXPLICIT_MODEL=%s/%s", target.ProviderID, target.ModelID)

	created, err := client.CreateNativeSession(ctx, workspace, CreateSessionRequest{
		Title: "D3-E successful explicit-model visibility live gate",
		Metadata: map[string]any{
			"agentdock_lane": "D3-E",
			"gate":           "explicit-model-session-visibility",
		},
	})
	if err != nil {
		t.Fatalf("native session creation: %v", err)
	}
	t.Logf("D3E_EXPLICIT_SESSION_ID=%s", created.ID)

	listed, err := client.ListNativeSessions(ctx, workspace)
	if err != nil {
		t.Fatalf("session API list visibility: %v", err)
	}
	found := false
	for _, item := range listed {
		if item.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created explicit-model session %s is not visible through /session", created.ID)
	}

	prompt := TextPrompt("Reply with exactly D3E_EXPLICIT_MODEL_OK and nothing else.")
	prompt.Model = &target
	reply, err := client.SubmitNativeMessage(ctx, workspace, created.ID, prompt)
	if err != nil {
		t.Fatalf("real explicit model execution: %v", err)
	}
	if reply.Info.HasError() {
		t.Fatalf("real explicit model execution returned assistant error state: %s", reply.Info.Error)
	}
	if got, ok := reply.Info.SelectedModel(); !ok || got != target {
		t.Fatalf("actual explicit provider/model mismatch: got=%+v ok=%v want=%+v", got, ok, target)
	}
	if got := strings.TrimSpace(messageText(reply)); got != "D3E_EXPLICIT_MODEL_OK" {
		t.Fatalf("successful explicit final response mismatch: %q", got)
	}

	info, err := client.GetNativeSession(ctx, workspace, created.ID)
	if err != nil {
		t.Fatalf("session API readback: %v", err)
	}
	if info.ID != created.ID {
		t.Fatalf("session get changed OpenCodeSessionID: got=%s want=%s", info.ID, created.ID)
	}
	if got, ok := info.SelectedModel(); !ok || got != target {
		t.Fatalf("explicit session model visibility: got=%+v ok=%v info=%+v", got, ok, info)
	}

	messages, err := client.ListNativeMessages(ctx, workspace, created.ID)
	if err != nil {
		t.Fatalf("message API readback: %v", err)
	}
	var userVisible, assistantVisible, finalVisible bool
	for _, message := range messages {
		selected, selectedOK := message.Info.SelectedModel()
		if !selectedOK || selected != target {
			continue
		}
		switch message.Info.Role {
		case "user":
			userVisible = true
		case "assistant":
			if message.Info.HasError() {
				t.Fatalf("assistant error became visible in successful explicit session: %s", message.Info.Error)
			}
			assistantVisible = true
			if strings.TrimSpace(messageText(message)) == "D3E_EXPLICIT_MODEL_OK" {
				finalVisible = true
			}
		}
	}
	if !userVisible || !assistantVisible || !finalVisible {
		t.Fatalf("explicit message visibility incomplete: user=%v assistant=%v final=%v messages=%+v", userVisible, assistantVisible, finalVisible, messages)
	}

	reconnected, err := NewClient(Config{BaseURL: baseURL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconnectedInfo, err := reconnected.GetNativeSession(ctx, workspace, created.ID)
	if err != nil {
		t.Fatalf("explicit session visibility after client reconnect: %v", err)
	}
	if reconnectedInfo.ID != created.ID {
		t.Fatalf("OpenCodeSessionID changed after client reconnect: got=%s want=%s", reconnectedInfo.ID, created.ID)
	}
	readback, err := reconnected.ListNativeMessages(ctx, workspace, created.ID)
	if err != nil || len(readback) < 2 {
		t.Fatalf("explicit message visibility after client reconnect: len=%d err=%v", len(readback), err)
	}
	t.Logf("D3E_EXPLICIT_EXECUTION=PASS session=%s model=%s/%s messages=%d", created.ID, target.ProviderID, target.ModelID, len(readback))
}

func TestD3ERealSessionVisibilityAfterRuntimeRestartWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("D3-E live gate is Windows-only")
	}
	baseURL := os.Getenv("AGENTDOCK_D3_NATIVE_URL")
	workspace := os.Getenv("AGENTDOCK_D3_NATIVE_WORKSPACE")
	sessionID := os.Getenv("AGENTDOCK_D3_NATIVE_EXISTING_SESSION")
	providerID := os.Getenv("AGENTDOCK_D3_NATIVE_EXPECTED_PROVIDER")
	modelID := os.Getenv("AGENTDOCK_D3_NATIVE_EXPECTED_MODEL")
	if baseURL == "" || workspace == "" || sessionID == "" || providerID == "" || modelID == "" {
		t.Skip("set AGENTDOCK_D3_NATIVE_URL, AGENTDOCK_D3_NATIVE_WORKSPACE, AGENTDOCK_D3_NATIVE_EXISTING_SESSION, AGENTDOCK_D3_NATIVE_EXPECTED_PROVIDER and AGENTDOCK_D3_NATIVE_EXPECTED_MODEL")
	}

	client, err := NewClient(Config{
		BaseURL:          baseURL,
		ReadinessTimeout: 30 * time.Second,
		InitialBackoff:   100 * time.Millisecond,
		MaxBackoff:       time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := client.WaitReady(ctx, workspace); err != nil {
		t.Fatalf("runtime readiness after restart: %v", err)
	}
	target := ModelRef{ProviderID: providerID, ModelID: modelID}
	info, err := client.GetNativeSession(ctx, workspace, sessionID)
	if err != nil {
		t.Fatalf("session readback after runtime restart: %v", err)
	}
	if info.ID != sessionID {
		t.Fatalf("OpenCodeSessionID changed after runtime restart: got=%s want=%s", info.ID, sessionID)
	}
	if got, ok := info.SelectedModel(); !ok || got != target {
		t.Fatalf("session model after runtime restart: got=%+v ok=%v info=%+v", got, ok, info)
	}
	messages, err := client.ListNativeMessages(ctx, workspace, sessionID)
	if err != nil || len(messages) < 2 {
		t.Fatalf("message readback after runtime restart: len=%d err=%v", len(messages), err)
	}
	assistantVisible := false
	finalVisible := false
	for _, message := range messages {
		if message.Info.Role != "assistant" {
			continue
		}
		if selected, ok := message.Info.SelectedModel(); ok && selected == target {
			if message.Info.HasError() {
				t.Fatalf("assistant error visible after runtime restart for successful explicit session: %s", message.Info.Error)
			}
			assistantVisible = true
			if strings.TrimSpace(messageText(message)) == "D3E_EXPLICIT_MODEL_OK" {
				finalVisible = true
			}
		}
	}
	if !assistantVisible || !finalVisible {
		t.Fatalf("assistant provider/model/final response missing after runtime restart: assistant=%v final=%v messages=%+v", assistantVisible, finalVisible, messages)
	}
	t.Logf("D3E_RUNTIME_RESTART_VISIBILITY=PASS session=%s messages=%d", sessionID, len(messages))
}

func firstAvailableExplicitModel(discovery ModelDiscovery) (ModelRef, bool) {
	for _, candidate := range []ModelRef{
		{ProviderID: "opencode", ModelID: "mimo-v2.5-free"},
		{ProviderID: "opencode", ModelID: "x-preview-f-free"},
	} {
		for _, provider := range discovery.Catalog.All {
			if provider.ID != candidate.ProviderID {
				continue
			}
			if _, ok := provider.Models[candidate.ModelID]; ok {
				return candidate, true
			}
		}
	}
	return ModelRef{}, false
}

func messageText(message Message) string {
	var out strings.Builder
	for _, raw := range message.Parts {
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &part); err != nil || part.Type != "text" {
			continue
		}
		out.WriteString(part.Text)
	}
	return out.String()
}
