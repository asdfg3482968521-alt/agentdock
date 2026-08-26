package opencodenative

import (
	"encoding/json"
	"time"
)

const (
	ErrorInvalid    = "OPENCODE_NATIVE_INVALID"
	ErrorNotFound   = "OPENCODE_NATIVE_NOT_FOUND"
	ErrorNotReady   = "OPENCODE_NATIVE_NOT_READY"
	ErrorTimeout    = "OPENCODE_NATIVE_TIMEOUT"
	ErrorHTTP       = "OPENCODE_NATIVE_HTTP"
	ErrorDecode     = "OPENCODE_NATIVE_DECODE"
	ErrorUnexpected = "OPENCODE_NATIVE_UNEXPECTED"
)

type Error struct {
	Code       string
	Message    string
	StatusCode int
	Retryable  bool
	Cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}
func (e *Error) Unwrap() error { return e.Cause }

type ModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

type SessionModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

type CreateSessionRequest struct {
	Title    string         `json:"title,omitempty"`
	Agent    string         `json:"agent,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type SessionInfo struct {
	ID        string           `json:"id"`
	Slug      string           `json:"slug,omitempty"`
	ProjectID string           `json:"projectID,omitempty"`
	Directory string           `json:"directory"`
	ParentID  string           `json:"parentID,omitempty"`
	Title     string           `json:"title,omitempty"`
	Agent     string           `json:"agent,omitempty"`
	Model     *SessionModelRef `json:"model,omitempty"`
	Version   string           `json:"version,omitempty"`
	Metadata  map[string]any   `json:"metadata,omitempty"`
	Time      map[string]any   `json:"time,omitempty"`
}

func (s SessionInfo) SelectedModel() (ModelRef, bool) {
	if s.Model == nil || s.Model.ProviderID == "" || s.Model.ID == "" {
		return ModelRef{}, false
	}
	return ModelRef{ProviderID: s.Model.ProviderID, ModelID: s.Model.ID}, true
}

type TextPartInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type PromptRequest struct {
	MessageID string          `json:"messageID,omitempty"`
	Model     *ModelRef       `json:"model,omitempty"`
	Agent     string          `json:"agent,omitempty"`
	System    string          `json:"system,omitempty"`
	Variant   string          `json:"variant,omitempty"`
	Parts     []TextPartInput `json:"parts"`
}

func TextPrompt(text string) PromptRequest {
	return PromptRequest{Parts: []TextPartInput{{Type: "text", Text: text}}}
}

type MessageInfo struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"sessionID"`
	Role       string          `json:"role"`
	Model      *ModelRef       `json:"model,omitempty"`
	ProviderID string          `json:"providerID,omitempty"`
	ModelID    string          `json:"modelID,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
}

func (m MessageInfo) HasError() bool {
	return len(m.Error) > 0 && string(m.Error) != "null"
}

func (m MessageInfo) SelectedModel() (ModelRef, bool) {
	if m.ProviderID != "" && m.ModelID != "" {
		return ModelRef{ProviderID: m.ProviderID, ModelID: m.ModelID}, true
	}
	if m.Model != nil && m.Model.ProviderID != "" && m.Model.ModelID != "" {
		return *m.Model, true
	}
	return ModelRef{}, false
}

type Message struct {
	Info  MessageInfo       `json:"info"`
	Parts []json.RawMessage `json:"parts"`
}

type StatusMap map[string]json.RawMessage

type ProviderModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
}

type ProviderInfo struct {
	ID     string                   `json:"id"`
	Name   string                   `json:"name,omitempty"`
	Source string                   `json:"source,omitempty"`
	Models map[string]ProviderModel `json:"models,omitempty"`
}

type ProviderListResult struct {
	All       []ProviderInfo    `json:"all"`
	Default   map[string]string `json:"default"`
	Connected []string          `json:"connected"`
}

type EffectiveConfig struct {
	Model string `json:"model,omitempty"`
}

type ModelDiscovery struct {
	Configured                  ModelRef           `json:"configured"`
	ConfiguredAvailable         bool               `json:"configured_available"`
	ConfiguredProviderSource    string             `json:"configured_provider_source,omitempty"`
	ConfiguredProviderConnected bool               `json:"configured_provider_connected"`
	ProviderDefault             ModelRef           `json:"provider_default"`
	Catalog                     ProviderListResult `json:"catalog"`
}

type NativeEvent struct {
	ID         string          `json:"id,omitempty"`
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type ReadinessResult struct {
	Attempts int
	ReadyAt  time.Time
}

type Config struct {
	BaseURL          string
	ReadinessTimeout time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
}
