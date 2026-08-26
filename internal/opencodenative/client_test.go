package opencodenative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientNativePrimitivesAndApplicationReadiness(t *testing.T) {
	const directory = `C:\work\d3`
	var statusCalls atomic.Int32
	var createCalls atomic.Int32
	var asyncCalls atomic.Int32
	var abortCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != directory {
			http.Error(w, "bad directory: "+got, http.StatusBadRequest)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /session/status":
			if statusCalls.Add(1) < 3 {
				http.Error(w, "warming", http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, map[string]any{"ses-1": map[string]any{"type": "idle"}})
		case "GET /provider":
			writeJSON(w, map[string]any{"all": []any{}, "default": map[string]any{}})
		case "POST /session":
			createCalls.Add(1)
			var body CreateSessionRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			writeJSON(w, SessionInfo{ID: "ses-1", Directory: directory, Title: body.Title, Metadata: body.Metadata})
		case "GET /session":
			writeJSON(w, []SessionInfo{{ID: "ses-1", Directory: directory, Metadata: map[string]any{"owner": "a"}}})
		case "GET /session/ses-1":
			writeJSON(w, SessionInfo{ID: "ses-1", Directory: directory, Metadata: map[string]any{"owner": "a"}})
		case "POST /session/ses-1/message":
			writeJSON(w, Message{Info: MessageInfo{ID: "msg-1", SessionID: "ses-1", Role: "assistant"}, Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}})
		case "POST /session/ses-1/prompt_async":
			asyncCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "GET /session/ses-1/message":
			writeJSON(w, []Message{{Info: MessageInfo{ID: "msg-1", SessionID: "ses-1", Role: "assistant"}}})
		case "POST /session/ses-1/abort":
			abortCalls.Add(1)
			writeJSON(w, true)
		case "GET /event":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"evt-1\",\"type\":\"message.updated\",\"properties\":{\"sessionID\":\"ses-1\"}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, ReadinessTimeout: time.Second, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := client.WaitReady(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Attempts < 3 {
		t.Fatalf("readiness did not retry application probes: %+v", ready)
	}

	created, err := client.CreateNativeSession(context.Background(), directory, CreateSessionRequest{Title: "D3", Metadata: map[string]any{"owner": "a"}})
	if err != nil || created.ID != "ses-1" {
		t.Fatalf("create: %+v %v", created, err)
	}
	if got := createCalls.Load(); got != 1 {
		t.Fatalf("create count=%d", got)
	}
	listed, err := client.ListNativeSessions(context.Background(), directory)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	attached, err := client.AttachNativeSession(context.Background(), directory, "ses-1")
	if err != nil || attached.ID != "ses-1" {
		t.Fatalf("attach: %+v %v", attached, err)
	}
	message, err := client.SubmitNativeMessage(context.Background(), directory, "ses-1", TextPrompt("hello"))
	if err != nil || message.Info.ID != "msg-1" {
		t.Fatalf("message: %+v %v", message, err)
	}
	if err := client.SubmitNativePromptAsync(context.Background(), directory, "ses-1", TextPrompt("async")); err != nil {
		t.Fatal(err)
	}
	if err := client.ContinueNativeConversation(context.Background(), directory, "ses-1", TextPrompt("continue")); err != nil {
		t.Fatal(err)
	}
	if got := asyncCalls.Load(); got != 2 {
		t.Fatalf("same-session async/continue count=%d", got)
	}
	messages, err := client.ListNativeMessages(context.Background(), directory, "ses-1")
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages: %+v %v", messages, err)
	}
	statuses, err := client.ObserveNativeStatus(context.Background(), directory)
	if err != nil || statuses["ses-1"] == nil {
		t.Fatalf("status: %+v %v", statuses, err)
	}
	if err := client.AbortNativeExecution(context.Background(), directory, "ses-1"); err != nil {
		t.Fatal(err)
	}
	if abortCalls.Load() != 1 {
		t.Fatal("abort endpoint not called")
	}
	providers, err := client.DiscoverNativeProviderModelMetadata(context.Background(), directory)
	if err != nil || !json.Valid(providers) {
		t.Fatalf("provider: %s %v", providers, err)
	}

	stop := errors.New("observed")
	var observed NativeEvent
	err = client.ObserveNativeEvents(context.Background(), directory, func(event NativeEvent) error { observed = event; return stop })
	if !errors.Is(err, stop) || observed.ID != "evt-1" || observed.Type != "message.updated" {
		t.Fatalf("event: %+v err=%v", observed, err)
	}
}

func TestDiscoverNativeModelsReportsConfiguredAvailableAndProviderDefault(t *testing.T) {
	const directory = `C:\work\d3-e`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != directory {
			http.Error(w, "bad directory: "+got, http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/provider":
			writeJSON(w, map[string]any{
				"all": []any{
					map[string]any{
						"id": "jiyuan", "name": "Jiyuan", "source": "config", "env": []string{}, "options": map[string]any{},
						"models": map[string]any{
							"deepseek-v4-flash-0731": map[string]any{"id": "deepseek-v4-flash-0731", "providerID": "jiyuan", "name": "DeepSeek V4 Flash", "status": "active"},
							"fallback":               map[string]any{"id": "fallback", "providerID": "jiyuan", "name": "Fallback", "status": "active"},
						},
					},
				},
				"default":   map[string]any{"jiyuan": "fallback"},
				"connected": []string{"jiyuan"},
			})
		case "/config":
			writeJSON(w, map[string]any{"model": "jiyuan/deepseek-v4-flash-0731"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := client.DiscoverNativeModels(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Configured != (ModelRef{ProviderID: "jiyuan", ModelID: "deepseek-v4-flash-0731"}) {
		t.Fatalf("configured model mismatch: %+v", discovery.Configured)
	}
	if !discovery.ConfiguredAvailable || !discovery.ConfiguredProviderConnected || discovery.ConfiguredProviderSource != "config" {
		t.Fatalf("configured model truth mismatch: %+v", discovery)
	}
	if discovery.ProviderDefault != (ModelRef{ProviderID: "jiyuan", ModelID: "fallback"}) {
		t.Fatalf("provider default mismatch: %+v", discovery.ProviderDefault)
	}
	if discovery.Catalog.Default["jiyuan"] != "fallback" || len(discovery.Catalog.All) != 1 {
		t.Fatalf("provider catalog missing from discovery: %+v", discovery.Catalog)
	}
}

func TestNativeSessionAndMessageDecodeSelectedProviderModel(t *testing.T) {
	const directory = `C:\work\d3-e`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != directory {
			http.Error(w, "bad directory: "+got, http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/session/ses-visible":
			writeJSON(w, map[string]any{
				"id": "ses-visible", "directory": directory,
				"model": map[string]any{"id": "deepseek-v4-flash-0731", "providerID": "jiyuan", "variant": "default"},
			})
		case "/session/ses-visible/message":
			writeJSON(w, []any{
				map[string]any{"info": map[string]any{"id": "msg-user", "sessionID": "ses-visible", "role": "user", "model": map[string]any{"providerID": "jiyuan", "modelID": "deepseek-v4-flash-0731"}}, "parts": []any{}},
				map[string]any{"info": map[string]any{"id": "msg-assistant", "sessionID": "ses-visible", "role": "assistant", "providerID": "jiyuan", "modelID": "deepseek-v4-flash-0731", "error": map[string]any{"name": "APIError", "data": map[string]any{"statusCode": 401}}}, "parts": []any{}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.GetNativeSession(context.Background(), directory, "ses-visible")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := info.SelectedModel(); !ok || got != (ModelRef{ProviderID: "jiyuan", ModelID: "deepseek-v4-flash-0731"}) {
		t.Fatalf("session selected model mismatch: got=%+v ok=%v info=%+v", got, ok, info)
	}
	messages, err := client.ListNativeMessages(context.Background(), directory, "ses-visible")
	if err != nil || len(messages) != 2 {
		t.Fatalf("message readback: len=%d err=%v", len(messages), err)
	}
	for _, message := range messages {
		if got, ok := message.Info.SelectedModel(); !ok || got != (ModelRef{ProviderID: "jiyuan", ModelID: "deepseek-v4-flash-0731"}) {
			t.Fatalf("message selected model mismatch: role=%s got=%+v ok=%v info=%+v", message.Info.Role, got, ok, message.Info)
		}
	}
	if !messages[1].Info.HasError() {
		t.Fatalf("assistant error state was not preserved: %+v", messages[1].Info)
	}
}

func TestWaitReadyRejectsTCPListenOnlyAndClassifiesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, ReadinessTimeout: 60 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.WaitReady(context.Background(), `C:\work`)
	if ErrorCode(err) != ErrorTimeout {
		t.Fatalf("TCP-only/non-app server should timeout readiness, got %T %v code=%q", err, err, ErrorCode(err))
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
