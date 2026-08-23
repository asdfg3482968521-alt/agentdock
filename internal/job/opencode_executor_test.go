package job

import (
	"context"
	"errors"
	"testing"

	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
)

type fakeOpenCodeSupervisor struct {
	startRequestID string
	workspace      string
	mode           opencoderuntime.PermissionMode
	tool           string
	arguments      map[string]any
	completion     chan opencoderuntime.AsyncExecutionOutcome
	cancelRequest  string
	cancelErr      error
}

func (f *fakeOpenCodeSupervisor) StartExecution(requestID, workspace string, mode opencoderuntime.PermissionMode, tool string, arguments map[string]any) (opencoderuntime.AsyncExecutionHandle, error) {
	f.startRequestID, f.workspace, f.mode, f.tool, f.arguments = requestID, workspace, mode, tool, arguments
	if f.completion == nil {
		f.completion = make(chan opencoderuntime.AsyncExecutionOutcome, 1)
	}
	return opencoderuntime.AsyncExecutionHandle{RequestID: requestID, Completion: f.completion}, nil
}
func (f *fakeOpenCodeSupervisor) CancelExecution(_ context.Context, requestID string) error {
	f.cancelRequest = requestID
	return f.cancelErr
}

func TestOpenCodeExecutorMapsSupervisorHandleAndResult(t *testing.T) {
	fake := &fakeOpenCodeSupervisor{completion: make(chan opencoderuntime.AsyncExecutionOutcome, 1)}
	executor := newOpenCodeExecutor(fake)
	handle, err := executor.Execute(context.Background(), RuntimeExecutionRequest{
		JobID: "11111111-1111-4111-8111-111111111111", Workspace: t.TempDir(), Tool: "bash",
		Arguments: map[string]any{"command": "echo hello"}, PermissionMode: "project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.RuntimeID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("runtime id=%q", handle.RuntimeID)
	}
	if fake.tool != "bash" || fake.mode != opencoderuntime.PermissionProject || fake.arguments["command"] != "echo hello" {
		t.Fatalf("unexpected supervisor request")
	}
	fake.completion <- opencoderuntime.AsyncExecutionOutcome{Result: opencoderuntime.ExecutionResult{Result: map[string]any{
		"isError": false, "content": []any{map[string]any{"type": "text", "text": "hello\n"}},
	}}}
	close(fake.completion)
	result := <-handle.Completion
	if result.Err != nil || !result.OK || result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	if result.Summary != "hello\n" {
		t.Fatalf("summary=%q", result.Summary)
	}
}

func TestOpenCodeExecutorCancellationUsesSupervisorRequest(t *testing.T) {
	fake := &fakeOpenCodeSupervisor{}
	executor := newOpenCodeExecutor(fake)
	if err := executor.Cancel(context.Background(), RuntimeCancelRequest{JobID: "job-id", RuntimeID: "runtime-id", Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	if fake.cancelRequest != "runtime-id" {
		t.Fatalf("cancel request=%q", fake.cancelRequest)
	}
	fake.cancelErr = errors.New("cannot stop")
	if err := executor.Cancel(context.Background(), RuntimeCancelRequest{JobID: "fallback-id"}); err == nil {
		t.Fatal("expected cancel error")
	}
	if fake.cancelRequest != "fallback-id" {
		t.Fatalf("fallback cancel request=%q", fake.cancelRequest)
	}
}

func TestOpenCodeExecutorAvailability(t *testing.T) {
	executor := NewOpenCodeExecutor(nil)
	if err := executor.Available(); err == nil {
		t.Fatal("nil supervisor should be unavailable")
	}
}
