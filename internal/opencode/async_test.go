package opencode

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSupervisorAsyncExecutionCancelWaitsForToolStop(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	launcher := &fakeLauncher{factory: func(int) *fakeConnection {
		connection := newFakeConnection()
		connection.callFn = func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			time.Sleep(30 * time.Millisecond)
			close(stopped)
			return nil, ctx.Err()
		}
		return connection
	}}
	supervisor := testSupervisor(t, launcher)
	handle, err := supervisor.StartExecution("job-async-cancel", t.TempDir(), PermissionProject, "bash", map[string]any{"command": "slow"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	if err := supervisor.CancelExecution(context.Background(), handle.RequestID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("CancelExecution returned before tool call stopped")
	}
	outcome := <-handle.Completion
	var runtimeErr *Error
	if !errors.As(outcome.Err, &runtimeErr) || runtimeErr.Code != "OPENCODE_ABORTED" {
		t.Fatalf("outcome error=%#v, want OPENCODE_ABORTED", outcome.Err)
	}
}

func TestSupervisorAsyncExecutionRejectsDuplicateRequestID(t *testing.T) {
	started := make(chan struct{})
	launcher := &fakeLauncher{factory: func(int) *fakeConnection {
		connection := newFakeConnection()
		connection.callFn = func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return connection
	}}
	supervisor := testSupervisor(t, launcher)
	handle, err := supervisor.StartExecution("same-request", t.TempDir(), PermissionProject, "bash", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := supervisor.StartExecution("same-request", t.TempDir(), PermissionProject, "read", map[string]any{}); err == nil {
		t.Fatal("expected duplicate request id error")
	}
	if err := supervisor.CancelExecution(context.Background(), handle.RequestID); err != nil {
		t.Fatal(err)
	}
	<-handle.Completion
}
