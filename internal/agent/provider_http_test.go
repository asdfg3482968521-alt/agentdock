package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/credential"
)

func TestAnthropicProviderRequestAndToolResponse(t *testing.T) {
	const secret = "D2_TEST_SECRET_anthropic_provider"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != secret {
			t.Fatalf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("anthropic-version header missing")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != AnthropicDefaultModel {
			t.Fatalf("model = %v", body["model"])
		}
		if _, ok := body["tools"].([]any); !ok {
			t.Fatalf("tools = %#v", body["tools"])
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_1","name":"opencode_read","input":{"path":"README.md"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer server.Close()

	provider := NewAnthropicProvider(ProviderHTTPOptions{BaseURL: server.URL, MaxRetries: 0})
	request := ProviderRequest{
		AgentID:    "agent_a",
		SessionID:  "session_a",
		Model:      "default",
		Role:       "You are a coding subagent.",
		Prompt:     "inspect the file",
		Credential: credential.Secret(secret),
		Tools: []ProviderTool{{
			Name:        "opencode_read",
			Description: "read a file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
	}
	var events []ProviderEvent
	result, err := provider.Run(context.Background(), request, func(event ProviderEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ResolvedModel != "claude-opus-5" || result.StopReason != "tool_use" {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if len(events) != 2 || events[0].Kind != ProviderEventText || events[0].Text != "hello" || events[1].Tool == nil {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Tool.Name != "opencode_read" || string(events[1].Tool.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool event = %+v", events[1])
	}
}

func TestDeepSeekProviderRequestAndToolResponse(t *testing.T) {
	const secret = "D2_TEST_SECRET_deepseek_provider"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != DeepSeekDefaultModel {
			t.Fatalf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chat_1","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"checking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"opencode_grep","arguments":"{\"pattern\":\"needle\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL, MaxRetries: 0})
	var events []ProviderEvent
	result, err := provider.Run(context.Background(), ProviderRequest{
		AgentID: "agent_b", SessionID: "session_b", Model: "default", Prompt: "find needle", Credential: credential.Secret(secret),
		Tools: []ProviderTool{{Name: "opencode_grep", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, func(event ProviderEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ResolvedModel != "deepseek-v4-flash" || result.StopReason != "tool_calls" || result.Usage.TotalTokens != 11 {
		t.Fatalf("result = %+v", result)
	}
	if len(events) != 2 || events[0].Text != "checking" || events[1].Tool == nil || events[1].Tool.Name != "opencode_grep" {
		t.Fatalf("events = %+v", events)
	}
}

func TestProviderAuthFailureIsNotRetriedAndDoesNotLeakCredential(t *testing.T) {
	const secret = "D2_TEST_SECRET_auth_failure"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"authorization bearer %s rejected"}}`, secret)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL, MaxRetries: 3})
	_, err := provider.Run(context.Background(), ProviderRequest{Model: "default", Prompt: "test", Credential: credential.Secret(secret)}, func(ProviderEvent) error { return nil })
	if providerErrorCode(err) != ProviderErrorAuthFailure {
		t.Fatalf("error = %v, code=%q", err, providerErrorCode(err))
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("provider error leaked credential")
	}
}

func TestProviderRateLimitRetryIsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			return
		}
		fmt.Fprint(w, `{"model":"deepseek-v4-flash","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL, MaxRetries: 1, RetryBackoff: time.Millisecond})
	result, err := provider.Run(context.Background(), ProviderRequest{Model: "default", Prompt: "test", Credential: credential.Secret("synthetic")}, func(ProviderEvent) error { return nil })
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ResolvedModel != DeepSeekDefaultModel || calls.Load() != 2 {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
}

func TestProviderCancellationStopsRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	provider := NewAnthropicProvider(ProviderHTTPOptions{BaseURL: server.URL, RequestTimeout: 30 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := provider.Run(ctx, ProviderRequest{Model: "default", Prompt: "wait", Credential: credential.Secret("synthetic")}, func(ProviderEvent) error { return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if providerErrorCode(err) != ProviderErrorCancelled || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, code=%q", err, providerErrorCode(err))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider cancellation did not stop request")
	}
}

func TestProviderResponseBodyIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 4096))
	}))
	defer server.Close()

	provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL, MaxResponseBytes: 128})
	_, err := provider.Run(context.Background(), ProviderRequest{Model: "default", Prompt: "test", Credential: credential.Secret("synthetic")}, func(ProviderEvent) error { return nil })
	if providerErrorCode(err) != ProviderErrorMalformedResponse {
		t.Fatalf("error = %v, code=%q", err, providerErrorCode(err))
	}
}

func TestProviderErrorMappings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{name: "bad request", status: http.StatusBadRequest, code: ProviderErrorBadRequest},
		{name: "server unavailable", status: http.StatusServiceUnavailable, code: ProviderErrorUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"message":"provider failure"}}`)
			}))
			defer server.Close()
			provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL, MaxRetries: 0})
			_, err := provider.Run(context.Background(), ProviderRequest{Model: "default", Prompt: "test", Credential: credential.Secret("synthetic")}, func(ProviderEvent) error { return nil })
			if providerErrorCode(err) != tc.code {
				t.Fatalf("error = %v, code=%q want=%q", err, providerErrorCode(err), tc.code)
			}
		})
	}
}

func TestProviderMalformedResponseIsNormalized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":"deepseek-v4-flash","choices":[]}`)
	}))
	defer server.Close()
	provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL})
	_, err := provider.Run(context.Background(), ProviderRequest{Model: "default", Prompt: "test", Credential: credential.Secret("synthetic")}, func(ProviderEvent) error { return nil })
	if providerErrorCode(err) != ProviderErrorMalformedResponse {
		t.Fatalf("error = %v, code=%q", err, providerErrorCode(err))
	}
}

func TestProviderSuccessfulOutputDoesNotLeakCredential(t *testing.T) {
	const secret = "D2_TEST_SECRET_provider_success_output"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "deepseek-v4-flash",
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content": "echo " + secret,
					"tool_calls": []any{map[string]any{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "opencode_grep", "arguments": `{"pattern":"` + secret + `"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer server.Close()
	provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL})
	var events []ProviderEvent
	result, err := provider.Run(context.Background(), ProviderRequest{Model: "default", Prompt: "test", Credential: credential.Secret(secret)}, func(event ProviderEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(result.Summary, secret) || strings.Contains(string(encoded), secret) {
		t.Fatalf("provider result/events leaked credential: result=%q events=%s", result.Summary, encoded)
	}
}

func TestProviderRejectsUnavailableModelBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()

	provider := NewDeepSeekProvider(ProviderHTTPOptions{BaseURL: server.URL})
	_, err := provider.Run(context.Background(), ProviderRequest{Model: "not-a-real-d2-model", Prompt: "test", Credential: credential.Secret("synthetic")}, func(ProviderEvent) error { return nil })
	if providerErrorCode(err) != ProviderErrorModelUnavailable {
		t.Fatalf("error = %v, code=%q", err, providerErrorCode(err))
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls = %d, want 0", calls.Load())
	}
}
