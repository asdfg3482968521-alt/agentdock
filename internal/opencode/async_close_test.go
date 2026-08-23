package opencode

import (
	"context"
	"testing"
	"time"
)

func TestSupervisorCloseCancelsActiveAsyncExecution(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	launcher := &fakeLauncher{factory: func(int) *fakeConnection {
		connection := newFakeConnection()
		connection.callFn = func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return nil, ctx.Err()
		}
		return connection
	}}
	supervisor := testSupervisor(t, launcher)
	handle, err := supervisor.StartExecution("close-active", t.TempDir(), PermissionProject, "bash", map[string]any{"command": "slow"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("async request never started")
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active async request")
	}
	select {
	case <-handle.Completion:
	case <-time.After(time.Second):
		t.Fatal("completion did not resolve after Close")
	}
}
