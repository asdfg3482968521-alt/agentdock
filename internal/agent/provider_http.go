package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/redact"
)

const (
	AnthropicDefaultModel = "claude-opus-5"
	DeepSeekDefaultModel  = "deepseek-v4-flash"

	ProviderErrorAuthFailure       = "AUTH_FAILURE"
	ProviderErrorRateLimited       = "RATE_LIMITED"
	ProviderErrorBadRequest        = "BAD_REQUEST"
	ProviderErrorUnavailable       = "PROVIDER_UNAVAILABLE"
	ProviderErrorTimeout           = "TIMEOUT"
	ProviderErrorCancelled         = "CANCELLED"
	ProviderErrorMalformedResponse = "MALFORMED_RESPONSE"
	ProviderErrorModelUnavailable  = "MODEL_UNAVAILABLE"
)

type ProviderHTTPOptions struct {
	BaseURL          string
	ConnectTimeout   time.Duration
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	MaxRetries       int
	RetryBackoff     time.Duration
}

type providerError struct {
	code string
	err  error
}

func (e *providerError) Error() string {
	if e == nil {
		return ""
	}
	if e.err == nil {
		return e.code
	}
	return e.code + ": " + e.err.Error()
}

func (e *providerError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func providerErrorCode(err error) string {
	var target *providerError
	if errors.As(err, &target) {
		return target.code
	}
	return ""
}

type httpProvider struct {
	kind             string
	baseURL          string
	client           *http.Client
	requestTimeout   time.Duration
	maxResponseBytes int64
	maxRetries       int
	retryBackoff     time.Duration
}

func NewAnthropicProvider(options ProviderHTTPOptions) ProviderAdapter {
	return newHTTPProvider("anthropic", options)
}

func NewDeepSeekProvider(options ProviderHTTPOptions) ProviderAdapter {
	return newHTTPProvider("deepseek", options)
}

func newHTTPProvider(kind string, options ProviderHTTPOptions) ProviderAdapter {
	if options.ConnectTimeout <= 0 {
		options.ConnectTimeout = 10 * time.Second
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 2 * time.Minute
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = 4 << 20
	}
	if options.MaxRetries < 0 {
		options.MaxRetries = 0
	}
	if options.RetryBackoff <= 0 {
		options.RetryBackoff = 250 * time.Millisecond
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: options.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	client := &http.Client{
		Transport: transport,
		Timeout:   options.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &httpProvider{
		kind:             kind,
		baseURL:          strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
		client:           client,
		requestTimeout:   options.RequestTimeout,
		maxResponseBytes: options.MaxResponseBytes,
		maxRetries:       options.MaxRetries,
		retryBackoff:     options.RetryBackoff,
	}
}

func (p *httpProvider) Run(ctx context.Context, request ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, error) {
	model, err := p.resolveModel(request.Model)
	if err != nil {
		return ProviderResult{}, err
	}
	if len(request.Credential) == 0 {
		return ProviderResult{}, &providerError{code: ProviderErrorAuthFailure, err: errors.New("provider credential is empty")}
	}
	if emit == nil {
		emit = func(ProviderEvent) error { return nil }
	}

	var payload []byte
	var endpoint string
	switch p.kind {
	case "anthropic":
		payload, err = marshalAnthropicRequest(request, model)
		endpoint = p.baseURL + "/v1/messages"
	case "deepseek":
		payload, err = marshalDeepSeekRequest(request, model)
		endpoint = p.baseURL + "/chat/completions"
	default:
		return ProviderResult{}, &providerError{code: ProviderErrorBadRequest, err: errors.New("unsupported provider adapter")}
	}
	if err != nil {
		return ProviderResult{}, &providerError{code: ProviderErrorBadRequest, err: errors.New("invalid provider request")}
	}

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return ProviderResult{}, contextProviderError(err)
		}
		result, retry, runErr := p.doRequest(ctx, endpoint, payload, request, emit)
		if runErr == nil {
			return result, nil
		}
		if !retry || attempt >= p.maxRetries {
			return ProviderResult{}, runErr
		}
		if err := sleepWithContext(ctx, p.retryBackoff); err != nil {
			return ProviderResult{}, contextProviderError(err)
		}
	}
}

func (p *httpProvider) resolveModel(raw string) (string, error) {
	model := strings.TrimSpace(raw)
	if model == "" || model == "default" {
		if p.kind == "anthropic" {
			return AnthropicDefaultModel, nil
		}
		return DeepSeekDefaultModel, nil
	}
	switch p.kind {
	case "anthropic":
		if model == AnthropicDefaultModel {
			return model, nil
		}
	case "deepseek":
		if model == DeepSeekDefaultModel || model == "deepseek-v4-pro" {
			return model, nil
		}
	}
	return "", &providerError{code: ProviderErrorModelUnavailable, err: errors.New("requested model is unavailable")}
}

func (p *httpProvider) doRequest(ctx context.Context, endpoint string, payload []byte, request ProviderRequest, emit func(ProviderEvent) error) (ProviderResult, bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ProviderResult{}, false, &providerError{code: ProviderErrorBadRequest, err: errors.New("build provider request")}
	}
	req.Header.Set("Content-Type", "application/json")
	switch p.kind {
	case "anthropic":
		req.Header.Set("x-api-key", string(request.Credential))
		req.Header.Set("anthropic-version", "2023-06-01")
	case "deepseek":
		req.Header.Set("Authorization", "Bearer "+string(request.Credential))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if reqCtx.Err() != nil {
			return ProviderResult{}, false, contextProviderError(reqCtx.Err())
		}
		return ProviderResult{}, true, &providerError{code: ProviderErrorUnavailable, err: errors.New("provider request failed")}
	}
	defer resp.Body.Close()

	body, err := readBoundedBody(resp.Body, p.maxResponseBytes)
	if err != nil {
		return ProviderResult{}, false, &providerError{code: ProviderErrorMalformedResponse, err: errors.New("provider response exceeds limit")}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, retry := normalizeHTTPStatus(resp.StatusCode)
		return ProviderResult{}, retry, &providerError{code: code, err: errors.New("provider returned an error response")}
	}

	switch p.kind {
	case "anthropic":
		result, events, err := parseAnthropicResponse(body)
		if err != nil {
			return ProviderResult{}, false, &providerError{code: ProviderErrorMalformedResponse, err: errors.New("malformed Anthropic response")}
		}
		result, events = sanitizeProviderOutput(result, events, request.Credential)
		for _, event := range events {
			if err := emit(event); err != nil {
				return ProviderResult{}, false, err
			}
		}
		return result, false, nil
	case "deepseek":
		result, events, err := parseDeepSeekResponse(body)
		if err != nil {
			return ProviderResult{}, false, &providerError{code: ProviderErrorMalformedResponse, err: errors.New("malformed DeepSeek response")}
		}
		result, events = sanitizeProviderOutput(result, events, request.Credential)
		for _, event := range events {
			if err := emit(event); err != nil {
				return ProviderResult{}, false, err
			}
		}
		return result, false, nil
	default:
		return ProviderResult{}, false, &providerError{code: ProviderErrorBadRequest, err: errors.New("unsupported provider adapter")}
	}
}

func normalizeHTTPStatus(status int) (string, bool) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ProviderErrorAuthFailure, false
	case status == http.StatusTooManyRequests:
		return ProviderErrorRateLimited, true
	case status >= 500:
		return ProviderErrorUnavailable, true
	case status >= 400:
		return ProviderErrorBadRequest, false
	default:
		return ProviderErrorUnavailable, false
	}
}

func contextProviderError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &providerError{code: ProviderErrorCancelled, err: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &providerError{code: ProviderErrorTimeout, err: context.DeadlineExceeded}
	}
	return &providerError{code: ProviderErrorUnavailable, err: errors.New("provider context failed")}
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readBoundedBody(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response too large")
	}
	return data, nil
}

func marshalAnthropicRequest(request ProviderRequest, model string) ([]byte, error) {
	type tool struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	messages := make([]map[string]any, 0, len(request.Messages)+1)
	for _, message := range request.Messages {
		if strings.TrimSpace(message.Text) == "" {
			continue
		}
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Text})
	}
	if len(messages) == 0 || strings.TrimSpace(request.Prompt) != "" {
		messages = append(messages, map[string]any{"role": "user", "content": request.Prompt})
	}
	tools := make([]tool, 0, len(request.Tools))
	for _, item := range request.Tools {
		tools = append(tools, tool{Name: item.Name, Description: item.Description, InputSchema: item.InputSchema})
	}
	maxTokens := request.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := map[string]any{"model": model, "max_tokens": maxTokens, "messages": messages}
	if strings.TrimSpace(request.Role) != "" {
		body["system"] = request.Role
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	return json.Marshal(body)
}

func marshalDeepSeekRequest(request ProviderRequest, model string) ([]byte, error) {
	messages := make([]map[string]any, 0, len(request.Messages)+2)
	if strings.TrimSpace(request.Role) != "" {
		messages = append(messages, map[string]any{"role": "system", "content": request.Role})
	}
	for _, message := range request.Messages {
		entry := map[string]any{"role": message.Role, "content": message.Text}
		if message.ToolCallID != "" {
			entry["tool_call_id"] = message.ToolCallID
		}
		messages = append(messages, entry)
	}
	if strings.TrimSpace(request.Prompt) != "" {
		messages = append(messages, map[string]any{"role": "user", "content": request.Prompt})
	}
	if len(messages) == 0 {
		messages = append(messages, map[string]any{"role": "user", "content": ""})
	}
	body := map[string]any{"model": model, "messages": messages, "stream": false}
	if request.MaxOutputTokens > 0 {
		body["max_tokens"] = request.MaxOutputTokens
	}
	if len(request.Tools) > 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, item := range request.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": item.Name, "description": item.Description, "parameters": item.InputSchema}})
		}
		body["tools"] = tools
	}
	return json.Marshal(body)
}

func parseAnthropicResponse(body []byte) (ProviderResult, []ProviderEvent, error) {
	var response struct {
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || strings.TrimSpace(response.Model) == "" || response.Content == nil {
		return ProviderResult{}, nil, errors.New("invalid response")
	}
	events := make([]ProviderEvent, 0, len(response.Content))
	var summary strings.Builder
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			events = append(events, ProviderEvent{Kind: ProviderEventText, Text: block.Text})
			summary.WriteString(block.Text)
		case "tool_use":
			if block.Name == "" || len(block.Input) == 0 {
				return ProviderResult{}, nil, errors.New("invalid tool use")
			}
			events = append(events, ProviderEvent{Kind: ProviderEventTool, Tool: &ToolCall{ID: block.ID, Name: block.Name, Arguments: append(json.RawMessage(nil), block.Input...)}})
		}
	}
	usage := Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return ProviderResult{Summary: summary.String(), ResolvedModel: response.Model, StopReason: response.StopReason, Usage: usage}, events, nil
}

func parseDeepSeekResponse(body []byte) (ProviderResult, []ProviderEvent, error) {
	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || strings.TrimSpace(response.Model) == "" || len(response.Choices) == 0 {
		return ProviderResult{}, nil, errors.New("invalid response")
	}
	choice := response.Choices[0]
	events := make([]ProviderEvent, 0, 1+len(choice.Message.ToolCalls))
	if choice.Message.Content != "" {
		events = append(events, ProviderEvent{Kind: ProviderEventText, Text: choice.Message.Content})
	}
	for _, call := range choice.Message.ToolCalls {
		arguments := json.RawMessage(call.Function.Arguments)
		if call.Function.Name == "" || !json.Valid(arguments) {
			return ProviderResult{}, nil, errors.New("invalid tool call")
		}
		events = append(events, ProviderEvent{Kind: ProviderEventTool, Tool: &ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: append(json.RawMessage(nil), arguments...)}})
	}
	return ProviderResult{
		Summary:       choice.Message.Content,
		ResolvedModel: response.Model,
		StopReason:    choice.FinishReason,
		Usage:         Usage{InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, TotalTokens: response.Usage.TotalTokens},
	}, events, nil
}

func sanitizeProviderOutput(result ProviderResult, events []ProviderEvent, secret []byte) (ProviderResult, []ProviderEvent) {
	result.Summary = sanitizeProviderString(result.Summary, secret)
	for i := range events {
		events[i].Text = sanitizeProviderString(events[i].Text, secret)
		if events[i].Tool != nil {
			copyCall := *events[i].Tool
			copyCall.Arguments = sanitizeProviderJSON(copyCall.Arguments, secret)
			events[i].Tool = &copyCall
		}
	}
	return result, events
}

func sanitizeProviderString(text string, secret []byte) string {
	if len(secret) > 0 {
		text = strings.ReplaceAll(text, string(secret), "[REDACTED]")
	}
	return redact.Text(text)
}

func sanitizeProviderJSON(raw json.RawMessage, secret []byte) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(sanitizeProviderString(string(raw), secret))
	}
	value = sanitizeProviderJSONValue(value, secret)
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return encoded
}

func sanitizeProviderJSONValue(value any, secret []byte) any {
	switch typed := value.(type) {
	case string:
		return sanitizeProviderString(typed, secret)
	case []any:
		for i := range typed {
			typed[i] = sanitizeProviderJSONValue(typed[i], secret)
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = sanitizeProviderJSONValue(item, secret)
		}
		return typed
	default:
		return value
	}
}

func (e *providerError) Format(state fmt.State, verb rune) {
	fmt.Fprint(state, e.Error())
}
