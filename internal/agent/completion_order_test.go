package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type terminalSaveGate struct {
	agentID string
	entered chan Record
	release chan struct{}
	failure error
}

func (g *terminalSaveGate) hook(record Record) error {
	if !record.State.Terminal() || (g.agentID != "" && record.AgentID != g.agentID) {
		return nil
	}
	if g.entered != nil {
		select {
		case g.entered <- cloneAgentRecord(record):
		default:
		}
	}
	if g.release != nil {
		<-g.release
	}
	return g.failure
}

func readDurableAgentRecord(t *testing.T, manager *Manager, agentID string) Record {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(manager.persistence.recordsDir, agentID+".json"))
	if err != nil {
		t.Fatalf("read durable agent record: %v", err)
	}
	var record Record
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("parse durable agent record: %v", err)
	}
	return record
}

func assertDoesNotReturn(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s returned before terminal persistence completed: %v", what, err)
	case <-time.After(80 * time.Millisecond):
	}
}

func startBlockingAgent(t *testing.T, manager *Manager, provider *blockingProvider, prompt string) Record {
	t.Helper()
	record, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default",
		Prompt: prompt, MaxRequests: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-provider.started:
		if id != record.AgentID {
			t.Fatalf("provider started agent %s want %s", id, record.AgentID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	return record
}

func TestAgentWaitDoesNotReturnBeforeTerminalPersistence(t *testing.T) {
	provider := &blockingProvider{started: make(chan string, 1), release: make(chan struct{})}
	manager := testAgentManager(t, provider, DefaultAgentLimits())
	defer manager.Close()

	record := startBlockingAgent(t, manager, provider, "success ordering")
	gate := &terminalSaveGate{agentID: record.AgentID, entered: make(chan Record, 1), release: make(chan struct{})}
	manager.persistence.beforeSave = gate.hook

	waitDone := make(chan error, 1)
	go func() {
		final, err := manager.Wait(context.Background(), record.AgentID)
		if err == nil && final.State != AgentStateSucceeded {
			err = errors.New("Wait returned non-SUCCEEDED terminal state")
		}
		waitDone <- err
	}()
	close(provider.release)
	select {
	case final := <-gate.entered:
		if final.State != AgentStateSucceeded {
			t.Fatalf("blocked terminal save state=%s", final.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal persistence was not reached")
	}
	assertDoesNotReturn(t, waitDone, "Wait")
	visible, err := manager.Get("session_a", record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if visible.State.Terminal() {
		t.Fatalf("terminal state became visible before durable save: %s", visible.State)
	}
	if _, err := manager.Result("session_a", record.AgentID); !errors.Is(err, ErrAgentNotTerminal) {
		t.Fatalf("Result before durable terminal save error=%v want AGENT_NOT_TERMINAL", err)
	}
	close(gate.release)
	if err := <-waitDone; err != nil {
		t.Fatal(err)
	}

	durable := readDurableAgentRecord(t, manager, record.AgentID)
	if durable.State != AgentStateSucceeded {
		t.Fatalf("durable state=%s want SUCCEEDED", durable.State)
	}
	restarted, err := NewManager(context.Background(), manager.options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Get("session_a", record.AgentID)
	if err != nil || recovered.State != AgentStateSucceeded {
		t.Fatalf("immediate restart recovered=%+v err=%v", recovered, err)
	}
}

func TestAgentCancelDoesNotReturnBeforeTerminalPersistence(t *testing.T) {
	provider := &blockingProvider{started: make(chan string, 1), release: make(chan struct{})}
	manager := testAgentManager(t, provider, DefaultAgentLimits())
	defer manager.Close()
	record := startBlockingAgent(t, manager, provider, "cancel ordering")

	gate := &terminalSaveGate{agentID: record.AgentID, entered: make(chan Record, 1), release: make(chan struct{})}
	manager.persistence.beforeSave = gate.hook
	cancelDone := make(chan error, 1)
	go func() {
		final, err := manager.Cancel(context.Background(), "session_a", record.AgentID, "stop")
		if err == nil && final.State != AgentStateCancelled {
			err = errors.New("Cancel returned non-CANCELLED state")
		}
		cancelDone <- err
	}()
	select {
	case final := <-gate.entered:
		if final.State != AgentStateCancelled {
			t.Fatalf("blocked terminal save state=%s", final.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel terminal persistence was not reached")
	}
	assertDoesNotReturn(t, cancelDone, "Cancel")
	close(gate.release)
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}

	durable := readDurableAgentRecord(t, manager, record.AgentID)
	if durable.State != AgentStateCancelled {
		t.Fatalf("durable state=%s want CANCELLED", durable.State)
	}
	restarted, err := NewManager(context.Background(), manager.options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Get("session_a", record.AgentID)
	if err != nil || recovered.State != AgentStateCancelled {
		t.Fatalf("immediate restart recovered=%+v err=%v", recovered, err)
	}
}

func TestAgentQueuedCancelDoesNotPublishCompletionBeforePersistence(t *testing.T) {
	limits := DefaultAgentLimits()
	limits.MaxConcurrent = 1
	provider := &blockingProvider{started: make(chan string, 2), release: make(chan struct{})}
	manager := testAgentManager(t, provider, limits)
	defer manager.Close()
	_ = startBlockingAgent(t, manager, provider, "occupy slot")
	queued, err := manager.Start(context.Background(), StartRequest{
		SessionID: "session_a", Provider: "fake", CredentialRef: "cred_fake_default",
		Prompt: "queued cancel", MaxRequests: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != AgentStateQueued {
		t.Fatalf("state=%s want QUEUED", queued.State)
	}

	gate := &terminalSaveGate{agentID: queued.AgentID, entered: make(chan Record, 1), release: make(chan struct{})}
	manager.persistence.beforeSave = gate.hook
	cancelDone := make(chan error, 1)
	go func() {
		final, err := manager.Cancel(context.Background(), "session_a", queued.AgentID, "queued stop")
		if err == nil && final.State != AgentStateCancelled {
			err = errors.New("queued Cancel returned non-CANCELLED state")
		}
		cancelDone <- err
	}()
	waitDone := make(chan error, 1)
	go func() {
		final, err := manager.Wait(context.Background(), queued.AgentID)
		if err == nil && final.State != AgentStateCancelled {
			err = errors.New("queued Wait returned non-CANCELLED state")
		}
		waitDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("queued cancel terminal persistence was not reached")
	}
	assertDoesNotReturn(t, cancelDone, "queued Cancel")
	assertDoesNotReturn(t, waitDone, "queued Wait")
	visible, err := manager.Get("session_a", queued.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if visible.State != AgentStateQueued {
		t.Fatalf("queued terminal state published before durable save: %s", visible.State)
	}
	close(gate.release)
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	if err := <-waitDone; err != nil {
		t.Fatal(err)
	}
	if durable := readDurableAgentRecord(t, manager, queued.AgentID); durable.State != AgentStateCancelled {
		t.Fatalf("durable queued cancel state=%s", durable.State)
	}
	close(provider.release)
}

func TestAgentTerminalPersistenceFailurePropagatesToWaitAndCancel(t *testing.T) {
	provider := &blockingProvider{started: make(chan string, 1), release: make(chan struct{})}
	manager := testAgentManager(t, provider, DefaultAgentLimits())
	defer manager.Close()
	record := startBlockingAgent(t, manager, provider, "persistence failure")

	persistErr := errors.New("synthetic terminal persistence failure")
	manager.persistence.beforeSave = (&terminalSaveGate{agentID: record.AgentID, failure: persistErr}).hook
	close(provider.release)
	if _, err := manager.Wait(context.Background(), record.AgentID); !errors.Is(err, persistErr) {
		t.Fatalf("Wait error=%v want persistence failure", err)
	}
	if _, err := manager.Cancel(context.Background(), "session_a", record.AgentID, "after failed finalize"); !errors.Is(err, persistErr) {
		t.Fatalf("Cancel error=%v want persistence failure", err)
	}

	restarted, err := NewManager(context.Background(), manager.options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Get("session_a", record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != AgentStateInterrupted {
		t.Fatalf("failed terminal persistence recovered state=%s want INTERRUPTED", recovered.State)
	}
}
