package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func nativeOwnedEventKey(event NativePublicEvent) string {
	nativeSessionID := strings.TrimSpace(event.NativeSessionID)
	if nativeEventID := strings.TrimSpace(event.NativeEventID); nativeEventID != "" {
		return "id:" + nativeSessionID + ":" + nativeEventID
	}
	if event.NativeEventSeq != 0 {
		return fmt.Sprintf("seq:%s:%d", nativeSessionID, event.NativeEventSeq)
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00", nativeSessionID, strings.TrimSpace(event.NativeType), strings.TrimSpace(event.MessageID), strings.TrimSpace(event.PartID))
	_, _ = hash.Write(event.Payload)
	return "hash:" + hex.EncodeToString(hash.Sum(nil))
}

// AppendNativeOwnedPublicEvent applies one event from the exact native root
// ownership closure at most once. LastOwnedEventCursor is independent from the
// public-event cursor because AgentDock supervisor events are not native event
// provenance and must not perturb F1 native ordering.
func (m *Manager) AppendNativeOwnedPublicEvent(sessionID, subagentID string, event NativePublicEvent) (NativePublicEvent, int64, bool, error) {
	key := nativeOwnedEventKey(event)
	var appended NativePublicEvent
	var ownedCursor int64
	applied := false
	_, err := m.UpdateNativeSessionBinding(sessionID, subagentID, func(binding *NativeSessionBinding) error {
		for _, existing := range binding.OwnedNativeEventKeys {
			if existing == key {
				ownedCursor = binding.LastOwnedEventCursor
				return nil
			}
		}

		binding.LastOwnedEventCursor++
		ownedCursor = binding.LastOwnedEventCursor
		binding.OwnedNativeEventKeys = append(binding.OwnedNativeEventKeys, key)

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
		applied = true
		return nil
	})
	if err != nil {
		return NativePublicEvent{}, 0, false, fmt.Errorf("append native owned public event: %w", err)
	}
	return appended, ownedCursor, applied, nil
}
