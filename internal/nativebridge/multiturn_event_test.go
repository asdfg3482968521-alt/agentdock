package nativebridge

import (
	"context"
	"testing"

	"github.com/uvwt/agentdock/internal/opencodenative"
)

func TestMultiturnEventDuplicateDoesNotAdvanceCursorAcrossReconnectOrRestart(t *testing.T) {
	f := newMultiturnFixture(t)
	f.harness.setEventOrder(f.binding.OpenCodeSessionID)
	callbackCount := 0
	var firstCursor int64
	if err := f.bridge.ObserveEvents(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), func(_ opencodenative.NativeEvent, cursor int64) error {
		callbackCount++
		firstCursor = cursor
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if firstCursor != 1 || callbackCount != 1 {
		t.Fatalf("first owned event application mismatch: cursor=%d callbacks=%d", firstCursor, callbackCount)
	}
	binding, err := f.store.NativeSessionBinding(f.record.SessionID, f.binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.LastOwnedEventCursor != 1 || binding.LastEventCursor != 1 || len(binding.NativePublicEvents) != 1 {
		t.Fatalf("first owned event durable state mismatch: %+v", binding)
	}

	if err := f.bridge.ObserveEvents(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), func(_ opencodenative.NativeEvent, cursor int64) error {
		callbackCount++
		firstCursor = cursor
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	binding, err = f.store.NativeSessionBinding(f.record.SessionID, f.binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.LastOwnedEventCursor != 1 || binding.LastEventCursor != 1 || len(binding.NativePublicEvents) != 1 || callbackCount != 1 {
		t.Fatalf("reconnect duplicate was applied twice: cursor=%d legacy=%d events=%d callbacks=%d", binding.LastOwnedEventCursor, binding.LastEventCursor, len(binding.NativePublicEvents), callbackCount)
	}

	restartedStore, restartedBridge := f.restartedBridge(t)
	if err := restartedBridge.ObserveEvents(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), func(_ opencodenative.NativeEvent, cursor int64) error {
		callbackCount++
		firstCursor = cursor
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	binding, err = restartedStore.NativeSessionBinding(f.record.SessionID, f.binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.LastOwnedEventCursor != 1 || binding.LastEventCursor != 1 || len(binding.NativePublicEvents) != 1 || callbackCount != 1 {
		t.Fatalf("restart duplicate was applied twice: cursor=%d legacy=%d events=%d callbacks=%d", binding.LastOwnedEventCursor, binding.LastEventCursor, len(binding.NativePublicEvents), callbackCount)
	}
}

func TestMultiturnEventApprovalHookRequiresDurableParentAndPreservesOrder(t *testing.T) {
	f := newMultiturnFixture(t)
	if _, _, err := f.bridge.AdmitSupervisorPermissionApproval(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), "missing-parent", "native-permission-missing", 0); err == nil {
		t.Fatal("approval hook admitted a request before its durable parent supervisor message")
	}

	message, err := f.bridge.SubmitSupervisorMessage(context.Background(), f.request("client-parent", "TASK", "parent turn"))
	if err != nil {
		t.Fatal(err)
	}
	f.harness.setEventOrder(f.binding.OpenCodeSessionID)
	var cursor int64
	if err := f.bridge.ObserveEvents(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), func(_ opencodenative.NativeEvent, observed int64) error {
		cursor = observed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cursor != 1 {
		t.Fatalf("owned event cursor = %d, want 1", cursor)
	}

	first, created, err := f.bridge.AdmitSupervisorPermissionApproval(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), message.Message.SupervisorMessageID, "native-permission-1", cursor)
	if err != nil || !created {
		t.Fatalf("first approval hook failed: created=%v approval=%+v err=%v", created, first, err)
	}
	if first.MessageSeq != message.Message.MessageSeq || first.ApprovalSeq != 1 || first.ObservedEventCursor != cursor || first.ApprovalRequestID == "" {
		t.Fatalf("approval hook order/identity mismatch: message=%+v approval=%+v", message.Message, first)
	}
	duplicate, created, err := f.bridge.AdmitSupervisorPermissionApproval(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), message.Message.SupervisorMessageID, "native-permission-1", cursor)
	if err != nil || created || duplicate.ApprovalRequestID != first.ApprovalRequestID || duplicate.ApprovalSeq != first.ApprovalSeq {
		t.Fatalf("approval hook duplicate was not idempotent: first=%+v duplicate=%+v created=%v err=%v", first, duplicate, created, err)
	}

	restartedStore, restartedBridge := f.restartedBridge(t)
	afterRestart, created, err := restartedBridge.AdmitSupervisorPermissionApproval(context.Background(), f.record.SessionID, f.binding.SubagentID, f.record.Workspace(), message.Message.SupervisorMessageID, "native-permission-1", cursor)
	if err != nil || created || afterRestart.ApprovalRequestID != first.ApprovalRequestID {
		t.Fatalf("restart changed approval dedup identity: first=%+v restarted=%+v created=%v err=%v", first, afterRestart, created, err)
	}
	binding, err := restartedStore.NativeSessionBinding(f.record.SessionID, f.binding.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(binding.PermissionApprovals) != 1 || binding.PermissionApprovals[0].ApprovalSeq != 1 {
		t.Fatalf("restart approval ordering mismatch: %+v", binding.PermissionApprovals)
	}
}
