package opencodenative

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxErrorBody = 32 << 10

type Client struct {
	baseURL string
	http    *http.Client
	config  Config
}

func NewClient(config Config, httpClient *http.Client) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, &Error{Code: ErrorInvalid, Message: "OpenCode native base URL must be an absolute HTTP URL", Cause: err}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &Error{Code: ErrorInvalid, Message: "OpenCode native base URL must use http or https"}
	}
	if config.ReadinessTimeout <= 0 {
		config.ReadinessTimeout = 15 * time.Second
	}
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 50 * time.Millisecond
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 750 * time.Millisecond
	}
	if config.MaxBackoff < config.InitialBackoff {
		config.MaxBackoff = config.InitialBackoff
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: base, http: httpClient, config: config}, nil
}

func (c *Client) CreateNativeSession(ctx context.Context, directory string, request CreateSessionRequest) (SessionInfo, error) {
	var result SessionInfo
	if err := c.doJSON(ctx, http.MethodPost, "/session", directory, request, &result); err != nil {
		return SessionInfo{}, err
	}
	if strings.TrimSpace(result.ID) == "" {
		return SessionInfo{}, &Error{Code: ErrorDecode, Message: "OpenCode native create returned empty session id"}
	}
	return result, nil
}

func (c *Client) GetNativeSession(ctx context.Context, directory, sessionID string) (SessionInfo, error) {
	var result SessionInfo
	if strings.TrimSpace(sessionID) == "" {
		return result, &Error{Code: ErrorInvalid, Message: "OpenCode native session id is required"}
	}
	err := c.doJSON(ctx, http.MethodGet, "/session/"+url.PathEscape(strings.TrimSpace(sessionID)), directory, nil, &result)
	return result, err
}

func (c *Client) AttachNativeSession(ctx context.Context, directory, sessionID string) (SessionInfo, error) {
	return c.GetNativeSession(ctx, directory, sessionID)
}

func (c *Client) ListNativeSessions(ctx context.Context, directory string) ([]SessionInfo, error) {
	var result []SessionInfo
	if err := c.doJSON(ctx, http.MethodGet, "/session", directory, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) SubmitNativeMessage(ctx context.Context, directory, sessionID string, prompt PromptRequest) (Message, error) {
	var result Message
	if len(prompt.Parts) == 0 {
		return result, &Error{Code: ErrorInvalid, Message: "native prompt requires at least one part"}
	}
	err := c.doJSON(ctx, http.MethodPost, "/session/"+url.PathEscape(strings.TrimSpace(sessionID))+"/message", directory, prompt, &result)
	return result, err
}

func (c *Client) SubmitNativePromptAsync(ctx context.Context, directory, sessionID string, prompt PromptRequest) error {
	if len(prompt.Parts) == 0 {
		return &Error{Code: ErrorInvalid, Message: "native prompt requires at least one part"}
	}
	return c.doJSON(ctx, http.MethodPost, "/session/"+url.PathEscape(strings.TrimSpace(sessionID))+"/prompt_async", directory, prompt, nil)
}

func (c *Client) ContinueNativeConversation(ctx context.Context, directory, sessionID string, prompt PromptRequest) error {
	// D3 resume semantics are MESSAGE_CONTINUE_ONLY: submit a new message to
	// the same native conversation. This is deliberately not execution resume.
	return c.SubmitNativePromptAsync(ctx, directory, sessionID, prompt)
}

func (c *Client) ListNativeMessages(ctx context.Context, directory, sessionID string) ([]Message, error) {
	var result []Message
	err := c.doJSON(ctx, http.MethodGet, "/session/"+url.PathEscape(strings.TrimSpace(sessionID))+"/message", directory, nil, &result)
	return result, err
}

func (c *Client) ObserveNativeStatus(ctx context.Context, directory string) (StatusMap, error) {
	var result StatusMap
	if err := c.doJSON(ctx, http.MethodGet, "/session/status", directory, nil, &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = StatusMap{}
	}
	return result, nil
}

func (c *Client) AbortNativeExecution(ctx context.Context, directory, sessionID string) error {
	var result bool
	return c.doJSON(ctx, http.MethodPost, "/session/"+url.PathEscape(strings.TrimSpace(sessionID))+"/abort", directory, nil, &result)
}

func (c *Client) DiscoverNativeProviderModelMetadata(ctx context.Context, directory string) (json.RawMessage, error) {
	var result json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, "/provider", directory, nil, &result); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, &Error{Code: ErrorDecode, Message: "OpenCode native provider metadata was empty"}
	}
	return result, nil
}

func (c *Client) DiscoverNativeModels(ctx context.Context, directory string) (ModelDiscovery, error) {
	var result ModelDiscovery
	var providers ProviderListResult
	if err := c.doJSON(ctx, http.MethodGet, "/provider", directory, nil, &providers); err != nil {
		return result, err
	}
	result.Catalog = providers
	var config EffectiveConfig
	if err := c.doJSON(ctx, http.MethodGet, "/config", directory, nil, &config); err != nil {
		return result, err
	}

	configured := strings.TrimSpace(config.Model)
	if configured == "" {
		return result, nil
	}
	providerID, modelID, ok := strings.Cut(configured, "/")
	providerID, modelID = strings.TrimSpace(providerID), strings.TrimSpace(modelID)
	if !ok || providerID == "" || modelID == "" {
		return result, &Error{Code: ErrorDecode, Message: "OpenCode configured model must be provider/model"}
	}
	result.Configured = ModelRef{ProviderID: providerID, ModelID: modelID}
	if defaultModelID := strings.TrimSpace(providers.Default[providerID]); defaultModelID != "" {
		result.ProviderDefault = ModelRef{ProviderID: providerID, ModelID: defaultModelID}
	}
	for _, connected := range providers.Connected {
		if strings.TrimSpace(connected) == providerID {
			result.ConfiguredProviderConnected = true
			break
		}
	}
	for _, provider := range providers.All {
		if provider.ID != providerID {
			continue
		}
		result.ConfiguredProviderSource = provider.Source
		_, result.ConfiguredAvailable = provider.Models[modelID]
		break
	}
	return result, nil
}

func (c *Client) ObserveNativeEvents(ctx context.Context, directory string, onEvent func(NativeEvent) error) error {
	if onEvent == nil {
		return &Error{Code: ErrorInvalid, Message: "native event observer callback is required"}
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/event", directory, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError("subscribe OpenCode native events", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError(resp, "subscribe OpenCode native events")
	}

	scanner := bufio.NewScanner(resp.Body)
	buffer := make([]byte, 64<<10)
	scanner.Buffer(buffer, 4<<20)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		var event NativeEvent
		if err := json.Unmarshal([]byte(data.String()), &event); err != nil {
			return &Error{Code: ErrorDecode, Message: "decode OpenCode native event", Cause: err}
		}
		data.Reset()
		return onEvent(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return c.transportError("read OpenCode native event stream", err)
	}
	return ctx.Err()
}

func (c *Client) ProbeReady(ctx context.Context, directory string) error {
	if _, err := c.ObserveNativeStatus(ctx, directory); err != nil {
		return err
	}
	if _, err := c.DiscoverNativeProviderModelMetadata(ctx, directory); err != nil {
		return err
	}
	return nil
}

func (c *Client) WaitReady(ctx context.Context, directory string) (ReadinessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	readyCtx, cancel := context.WithTimeout(ctx, c.config.ReadinessTimeout)
	defer cancel()
	backoff := c.config.InitialBackoff
	attempts := 0
	var last error
	for {
		attempts++
		probeCtx, probeCancel := context.WithTimeout(readyCtx, 3*time.Second)
		err := c.ProbeReady(probeCtx, directory)
		probeCancel()
		if err == nil {
			return ReadinessResult{Attempts: attempts, ReadyAt: time.Now().UTC()}, nil
		}
		last = err
		timer := time.NewTimer(backoff)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			code := ErrorTimeout
			if errors.Is(ctx.Err(), context.Canceled) {
				code = ErrorNotReady
			}
			return ReadinessResult{Attempts: attempts}, &Error{Code: code, Message: "OpenCode native server did not become application-ready before timeout", Retryable: true, Cause: last}
		case <-timer.C:
		}
		backoff *= 2
		if backoff > c.config.MaxBackoff {
			backoff = c.config.MaxBackoff
		}
	}
}

func (c *Client) doJSON(ctx context.Context, method, path, directory string, body any, out any) error {
	req, err := c.newRequest(ctx, method, path, directory, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(method+" "+path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError(resp, method+" "+path)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	if raw, ok := out.(*json.RawMessage); ok {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return &Error{Code: ErrorDecode, Message: "read OpenCode native response", Cause: err}
		}
		if !json.Valid(data) {
			return &Error{Code: ErrorDecode, Message: "OpenCode native response was not valid JSON"}
		}
		*raw = append((*raw)[:0], data...)
		return nil
	}
	if err := decoder.Decode(out); err != nil {
		return &Error{Code: ErrorDecode, Message: "decode OpenCode native response", Cause: err}
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path, directory string, body any) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, &Error{Code: ErrorInvalid, Message: "OpenCode native directory is required"}
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, &Error{Code: ErrorInvalid, Message: "encode OpenCode native request", Cause: err}
		}
		reader = bytes.NewReader(data)
	}
	endpoint, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, &Error{Code: ErrorInvalid, Message: "construct OpenCode native URL", Cause: err}
	}
	query := endpoint.Query()
	query.Set("directory", directory)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, &Error{Code: ErrorInvalid, Message: "construct OpenCode native request", Cause: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) statusError(resp *http.Response, operation string) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	code, retryable := ErrorHTTP, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	if resp.StatusCode == http.StatusNotFound {
		code, retryable = ErrorNotFound, false
	}
	return &Error{Code: code, Message: fmt.Sprintf("%s returned HTTP %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(data))), StatusCode: resp.StatusCode, Retryable: retryable}
}

func (c *Client) transportError(operation string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: ErrorTimeout, Message: operation + " timed out", Retryable: true, Cause: err}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: ErrorUnexpected, Message: operation + " cancelled", Cause: err}
	}
	return &Error{Code: ErrorNotReady, Message: operation + " failed", Retryable: true, Cause: err}
}

func ErrorCode(err error) string {
	var nativeErr *Error
	if errors.As(err, &nativeErr) {
		return nativeErr.Code
	}
	return ""
}
