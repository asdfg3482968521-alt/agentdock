package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	NativePublicEventRetention  = 256
	nativePublicEventPayloadMax = 32 << 10
	CodeNativeCursorInvalid     = "SESSION_NATIVE_CURSOR_INVALID"
)

type NativePublicEvent struct {
	Seq             int64           `json:"seq"`
	Kind            string          `json:"kind"`
	NativeEventID   string          `json:"native_event_id,omitempty"`
	NativeEventSeq  int64           `json:"native_event_seq,omitempty"`
	NativeType      string          `json:"native_type,omitempty"`
	NativeSessionID string          `json:"native_session_id,omitempty"`
	MessageID       string          `json:"message_id,omitempty"`
	PartID          string          `json:"part_id,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

type NativePublicEventPage struct {
	Events       []NativePublicEvent `json:"events"`
	NextCursor   int64               `json:"next_cursor"`
	LatestCursor int64               `json:"latest_cursor"`
	HasMore      bool                `json:"has_more"`
	Truncated    bool                `json:"truncated"`
}

func (m *Manager) AppendNativePublicEvent(sessionID, subagentID string, event NativePublicEvent) (NativePublicEvent, error) {
	var appended NativePublicEvent
	_, err := m.UpdateNativeSessionBinding(sessionID, subagentID, func(binding *NativeSessionBinding) error {
		binding.LastEventCursor++
		event.Seq = binding.LastEventCursor
		event.Kind = strings.TrimSpace(event.Kind)
		event.NativeEventID = strings.TrimSpace(event.NativeEventID)
		event.NativeType = strings.TrimSpace(event.NativeType)
		event.NativeSessionID = strings.TrimSpace(event.NativeSessionID)
		event.MessageID = strings.TrimSpace(event.MessageID)
		event.PartID = strings.TrimSpace(event.PartID)
		if event.Kind == "" {
			event.Kind = "event"
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = m.config.Now().UTC()
		}
		event.Payload = boundedNativePublicPayload(event.Payload)
		binding.LastNativeEventID = event.NativeEventID
		binding.LastNativeEventSeq = event.NativeEventSeq
		binding.NativePublicEvents = append(binding.NativePublicEvents, cloneNativePublicEvent(event))
		if len(binding.NativePublicEvents) > NativePublicEventRetention {
			start := len(binding.NativePublicEvents) - NativePublicEventRetention
			binding.NativePublicEvents = append([]NativePublicEvent(nil), binding.NativePublicEvents[start:]...)
		}
		appended = cloneNativePublicEvent(event)
		return nil
	})
	if err != nil {
		return NativePublicEvent{}, fmt.Errorf("append native public event: %w", err)
	}
	return appended, nil
}

func (m *Manager) NativePublicEvents(sessionID, subagentID string, after int64, limit int) (NativePublicEventPage, error) {
	binding, err := m.NativeSessionBinding(sessionID, subagentID)
	if err != nil {
		return NativePublicEventPage{}, err
	}
	if after < 0 || after > binding.LastEventCursor {
		return NativePublicEventPage{}, newError(CodeNativeCursorInvalid, "native public event cursor is outside durable range", nil)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > NativePublicEventRetention {
		limit = NativePublicEventRetention
	}
	page := NativePublicEventPage{Events: []NativePublicEvent{}, NextCursor: after, LatestCursor: binding.LastEventCursor}
	if len(binding.NativePublicEvents) == 0 {
		return page, nil
	}
	first := binding.NativePublicEvents[0].Seq
	if after < first-1 {
		page.Truncated = true
		after = first - 1
		page.NextCursor = after
	}
	for _, event := range binding.NativePublicEvents {
		if event.Seq <= after {
			continue
		}
		if len(page.Events) >= limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, cloneNativePublicEvent(event))
		page.NextCursor = event.Seq
	}
	if page.NextCursor < page.LatestCursor && len(page.Events) >= limit {
		page.HasMore = true
	}
	return page, nil
}

func boundedNativePublicPayload(payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) <= nativePublicEventPayloadMax && json.Valid(payload) {
		return append(json.RawMessage(nil), payload...)
	}
	replacement, _ := json.Marshal(map[string]any{"truncated": true, "original_bytes": len(payload)})
	return replacement
}

func cloneNativePublicEvent(event NativePublicEvent) NativePublicEvent {
	copy := event
	copy.Payload = append(json.RawMessage(nil), event.Payload...)
	return copy
}
