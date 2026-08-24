//go:build windows

package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExternalAgentDockD1SessionWindowsE2E(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_E2E_BINARY"))
	manifest := strings.TrimSpace(os.Getenv("AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST"))
	if binary == "" || manifest == "" {
		t.Skip("set AGENTDOCK_OPENCODE_E2E_BINARY and AGENTDOCK_OPENCODE_INTEGRATION_MANIFEST for the D1 Session Windows E2E")
	}

	root := t.TempDir()
	home := filepath.Join(root, "agentdock-home")
	defaultDir := filepath.Join(root, "agentdock-default")
	logs := filepath.Join(root, "opencode-logs")
	jobsRoot := filepath.Join(root, "jobs")
	repo := filepath.Join(root, "repo")
	nonGit := filepath.Join(root, "non-git")
	for _, dir := range []string{home, defaultDir, logs, jobsRoot, repo, nonGit} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mustWriteOuter(t, filepath.Join(repo, "foo.txt"), "BASE\n")
	writeD1CancellationFixtures(t, repo)
	gitOuter(t, repo, "init")
	gitOuter(t, repo, "config", "user.email", "d1-session@example.invalid")
	gitOuter(t, repo, "config", "user.name", "D1 Session E2E")
	gitOuter(t, repo, "add", ".")
	gitOuter(t, repo, "commit", "-m", "baseline")
	baseRef := strings.TrimSpace(d1GitOutput(t, repo, "rev-parse", "HEAD"))

	port := reserveJobHTTPPort(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	process := startJobHTTPAgentDockHostProfile(t, binary, manifest, port, home, defaultDir, logs, jobsRoot)
	t.Cleanup(func() {
		if t.Failed() && process != nil && process.stderr != nil {
			t.Logf("D1_STAGING_STDERR:\n%s", process.stderr.String())
		}
	})
	processStopped := false
	defer func() {
		if !processStopped {
			stopJobHTTPProcess(process)
		}
	}()
	waitJobHTTPReady(t, port, process, 20*time.Second)
	client, closeClient := d1ConnectHTTPClient(t, endpoint)
	clientClosed := false
	defer func() {
		if !clientClosed {
			closeClient()
		}
	}()

	tools := d1ToolMap(t, client)
	d1AssertDirectToolSurface(t, tools)
	d1AssertSessionSchemas(t, tools)
	t.Logf("D1_L1_L2 registry=PASS raw_tools_list=PASS expected_direct_tools=18 session_schema=PASS")

	invalidRef := d1ExpectError(t, client, "session_open", map[string]any{"project_id": "d1", "repo_root": repo, "base_ref": "HEAD~1"}, "INVALID_BASE_REF")
	invalidTraversal := d1ExpectError(t, client, "session_open", map[string]any{"project_id": "d1", "repo_root": repo, "base_ref": "../main"}, "INVALID_BASE_REF")
	invalidRoot := d1ExpectError(t, client, "session_open", map[string]any{"project_id": "d1", "repo_root": filepath.Join(root, "missing")}, "INVALID_REPO_ROOT")
	_ = invalidRef
	_ = invalidTraversal
	_ = invalidRoot

	openA, _ := d1Call(t, client, "session_open", map[string]any{
		"project_id": "d1-e2e", "repo_root": repo, "base_ref": baseRef, "permission_mode": "project", "client_tag": "A",
	}, 30*time.Second)
	openB, _ := d1Call(t, client, "session_open", map[string]any{
		"project_id": "d1-e2e", "repo_root": repo, "base_ref": baseRef, "permission_mode": "project", "client_tag": "B",
	}, 30*time.Second)
	sessionA := d1SessionFromResult(t, openA)
	sessionB := d1SessionFromResult(t, openB)
	idA, idB := stringValue(sessionA["session_id"]), stringValue(sessionB["session_id"])
	branchA, branchB := stringValue(sessionA["branch"]), stringValue(sessionB["branch"])
	worktreeA, worktreeB := stringValue(sessionA["worktree"]), stringValue(sessionB["worktree"])
	if idA == "" || idB == "" || idA == idB || branchA == "" || branchB == "" || branchA == branchB || worktreeA == "" || worktreeB == "" || strings.EqualFold(worktreeA, worktreeB) {
		t.Fatalf("Session isolation identities invalid A=%#v B=%#v", sessionA, sessionB)
	}
	if stringValue(sessionA["state"]) != "ACTIVE" || stringValue(sessionB["state"]) != "ACTIVE" {
		t.Fatalf("session_open did not return ACTIVE A=%#v B=%#v", sessionA, sessionB)
	}
	if stringValue(sessionA["metadata_version"]) != "D1-v1" || stringValue(sessionB["metadata_version"]) != "D1-v1" {
		t.Fatalf("metadata version drift A=%#v B=%#v", sessionA, sessionB)
	}
	if _, err := os.Stat(worktreeA); err != nil {
		t.Fatalf("A worktree missing: %v", err)
	}
	if _, err := os.Stat(worktreeB); err != nil {
		t.Fatalf("B worktree missing: %v", err)
	}

	_, _ = d1Call(t, client, "opencode_write", map[string]any{"session_id": idA, "arguments": map[string]any{"filePath": "foo.txt", "content": "A_ONLY\n"}}, 3*time.Minute)
	_, _ = d1Call(t, client, "opencode_write", map[string]any{"session_id": idB, "arguments": map[string]any{"filePath": "foo.txt", "content": "B_ONLY\n"}}, 3*time.Minute)
	readA, _ := d1Call(t, client, "opencode_read", map[string]any{"session_id": idA, "arguments": map[string]any{"filePath": "foo.txt"}}, 3*time.Minute)
	readB, _ := d1Call(t, client, "opencode_read", map[string]any{"session_id": idB, "arguments": map[string]any{"filePath": "foo.txt"}}, 3*time.Minute)
	if !strings.Contains(outerJSON(readA), "A_ONLY") || strings.Contains(outerJSON(readA), "B_ONLY") {
		t.Fatalf("A read isolation failed: %s", outerJSON(readA))
	}
	if !strings.Contains(outerJSON(readB), "B_ONLY") || strings.Contains(outerJSON(readB), "A_ONLY") {
		t.Fatalf("B read isolation failed: %s", outerJSON(readB))
	}
	mustContainOuterFile(t, filepath.Join(repo, "foo.txt"), "BASE")
	if data, _ := os.ReadFile(filepath.Join(repo, "foo.txt")); strings.Contains(string(data), "A_ONLY") || strings.Contains(string(data), "B_ONLY") {
		t.Fatalf("original working tree polluted: %q", data)
	}

	statusA, _ := d1Call(t, client, "opencode_git", map[string]any{"session_id": idA, "arguments": map[string]any{"operation": "status"}}, 3*time.Minute)
	diffA, _ := d1Call(t, client, "opencode_git", map[string]any{"session_id": idA, "arguments": map[string]any{"operation": "diff", "mode": "working", "path": "foo.txt"}}, 3*time.Minute)
	repositoryA, _ := d1Call(t, client, "opencode_git", map[string]any{"session_id": idA, "arguments": map[string]any{"operation": "repository"}}, 3*time.Minute)
	statusB, _ := d1Call(t, client, "opencode_git", map[string]any{"session_id": idB, "arguments": map[string]any{"operation": "status"}}, 3*time.Minute)
	diffB, _ := d1Call(t, client, "opencode_git", map[string]any{"session_id": idB, "arguments": map[string]any{"operation": "diff", "mode": "working", "path": "foo.txt"}}, 3*time.Minute)
	repositoryB, _ := d1Call(t, client, "opencode_git", map[string]any{"session_id": idB, "arguments": map[string]any{"operation": "repository"}}, 3*time.Minute)
	for label, result := range map[string]*mcpsdk.CallToolResult{"statusA": statusA, "diffA": diffA, "repoA": repositoryA} {
		if strings.Contains(outerJSON(result), "B_ONLY") {
			t.Fatalf("%s leaked B changes: %s", label, outerJSON(result))
		}
	}
	for label, result := range map[string]*mcpsdk.CallToolResult{"statusB": statusB, "diffB": diffB, "repoB": repositoryB} {
		if strings.Contains(outerJSON(result), "A_ONLY") {
			t.Fatalf("%s leaked A changes: %s", label, outerJSON(result))
		}
	}
	if !strings.Contains(outerJSON(diffA), "A_ONLY") || !strings.Contains(outerJSON(diffB), "B_ONLY") {
		t.Fatalf("git diff isolation missing expected values A=%s B=%s", outerJSON(diffA), outerJSON(diffB))
	}
	if !strings.Contains(outerJSON(repositoryA), branchA) || !strings.Contains(outerJSON(repositoryB), branchB) {
		t.Fatalf("git repository did not report Session branches A=%s B=%s", outerJSON(repositoryA), outerJSON(repositoryB))
	}

	d1ExpectError(t, client, "opencode_read", map[string]any{"session_id": idA, "workspace": worktreeB, "arguments": map[string]any{"filePath": "foo.txt"}}, "SESSION_WORKSPACE_MISMATCH")
	d1ExpectError(t, client, "session_status", map[string]any{"session_id": "00000000-0000-4000-8000-000000000000"}, "SESSION_NOT_FOUND")

	jobA, _ := d1Call(t, client, "job_start", map[string]any{
		"session_id": idA, "tool": "bash", "arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "node cancel-parent.cjs a-child.pid a-grand.pid a-marker.txt"`},
	}, 15*time.Second)
	jobB, _ := d1Call(t, client, "job_start", map[string]any{
		"session_id": idB, "tool": "bash", "arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "node cancel-parent.cjs b-child.pid b-grand.pid b-marker.txt"`},
	}, 15*time.Second)
	jobIDa, jobIDb := stringValue(jobA.StructuredContent.(map[string]any)["job_id"]), stringValue(jobB.StructuredContent.(map[string]any)["job_id"])
	if jobIDa == "" || jobIDb == "" || jobIDa == jobIDb {
		t.Fatalf("job IDs invalid A=%s B=%s", jobIDa, jobIDb)
	}
	d1WaitJobState(t, client, idA, jobIDa, 20*time.Second, "RUNNING")
	d1WaitJobState(t, client, idB, jobIDb, 20*time.Second, "RUNNING")
	aChild := waitOuterPIDFile(t, filepath.Join(worktreeA, "a-child.pid"), 20*time.Second)
	aGrand := waitOuterPIDFile(t, filepath.Join(worktreeA, "a-grand.pid"), 20*time.Second)
	bChild := waitOuterPIDFile(t, filepath.Join(worktreeB, "b-child.pid"), 20*time.Second)
	bGrand := waitOuterPIDFile(t, filepath.Join(worktreeB, "b-grand.pid"), 20*time.Second)

	statusOwnedA, _ := d1Call(t, client, "job_status", map[string]any{"session_id": idA, "job_id": jobIDa}, 15*time.Second)
	ownedA := statusOwnedA.StructuredContent.(map[string]any)
	if stringValue(ownedA["session_id"]) != idA || stringValue(ownedA["runtime_scope"]) != "session:"+idA || !strings.EqualFold(stringValue(ownedA["workspace"]), worktreeA) {
		t.Fatalf("job A binding mismatch: %#v", ownedA)
	}
	statusOwnedB, _ := d1Call(t, client, "job_status", map[string]any{"session_id": idB, "job_id": jobIDb}, 15*time.Second)
	ownedB := statusOwnedB.StructuredContent.(map[string]any)
	if stringValue(ownedB["session_id"]) != idB || stringValue(ownedB["runtime_scope"]) != "session:"+idB || !strings.EqualFold(stringValue(ownedB["workspace"]), worktreeB) {
		t.Fatalf("job B binding mismatch: %#v", ownedB)
	}
	d1ExpectError(t, client, "job_status", map[string]any{"session_id": idB, "job_id": jobIDa}, "SESSION_JOB_OWNERSHIP_MISMATCH")
	d1ExpectError(t, client, "job_output", map[string]any{"session_id": idB, "job_id": jobIDa, "cursor": 0}, "SESSION_JOB_OWNERSHIP_MISMATCH")
	d1ExpectError(t, client, "job_cancel", map[string]any{"session_id": idB, "job_id": jobIDa, "reason": "must fail"}, "SESSION_JOB_OWNERSHIP_MISMATCH")

	cancelA, _ := d1Call(t, client, "job_cancel", map[string]any{"session_id": idA, "job_id": jobIDa, "reason": "D1 cancel isolation"}, 20*time.Second)
	if stringValue(cancelA.StructuredContent.(map[string]any)["state"]) != "CANCELLED" {
		t.Fatalf("job A not cancelled: %s", outerJSON(cancelA))
	}
	waitOuterPIDGone(t, aChild, 10*time.Second)
	waitOuterPIDGone(t, aGrand, 10*time.Second)
	statusBAfterCancel := d1WaitJobState(t, client, idB, jobIDb, 5*time.Second, "RUNNING", "SUCCEEDED")
	if state := stringValue(statusBAfterCancel["state"]); state != "RUNNING" && state != "SUCCEEDED" {
		t.Fatalf("B changed after cancelling A: %#v", statusBAfterCancel)
	}
	if !outerPIDAlive(bChild) || !outerPIDAlive(bGrand) {
		t.Fatalf("B descendant process tree was affected by cancelling A child=%d alive=%v grand=%d alive=%v", bChild, outerPIDAlive(bChild), bGrand, outerPIDAlive(bGrand))
	}
	_, _ = d1Call(t, client, "job_cancel", map[string]any{"session_id": idB, "job_id": jobIDb, "reason": "test cleanup"}, 20*time.Second)
	waitOuterPIDGone(t, bChild, 10*time.Second)
	waitOuterPIDGone(t, bGrand, 10*time.Second)

	// Create a third Session whose worktree will be removed while AgentDock is stopped.
	// Restart recovery must mark it FAILED without altering A/B.
	openFailed, _ := d1Call(t, client, "session_open", map[string]any{"project_id": "d1-e2e", "repo_root": repo, "base_ref": baseRef, "client_tag": "failed-on-restart"}, 30*time.Second)
	failedSession := d1SessionFromResult(t, openFailed)
	failedID := stringValue(failedSession["session_id"])
	failedWorktree := stringValue(failedSession["worktree"])

	statusABefore, _ := d1Call(t, client, "session_status", map[string]any{"session_id": idA}, 15*time.Second)
	statusBBefore, _ := d1Call(t, client, "session_status", map[string]any{"session_id": idB}, 15*time.Second)
	beforeA := d1SessionFromResult(t, statusABefore)
	beforeB := d1SessionFromResult(t, statusBBefore)
	oldRuntimeA := jobRuntimePIDForWorkspace(t, client, worktreeA)
	oldRuntimeB := jobRuntimePIDForWorkspace(t, client, worktreeB)
	if beforeA["runtime_id"] == nil || beforeB["runtime_id"] == nil {
		t.Fatalf("runtime_id not bound before restart A=%#v B=%#v", beforeA, beforeB)
	}

	closeClient()
	clientClosed = true
	if err := process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(8 * time.Second):
		t.Fatal("AgentDock did not exit for D1 restart test")
	}
	processStopped = true
	waitOuterPIDGone(t, oldRuntimeA, 10*time.Second)
	waitOuterPIDGone(t, oldRuntimeB, 10*time.Second)
	if err := os.RemoveAll(failedWorktree); err != nil {
		t.Fatalf("remove failed-session worktree fixture: %v", err)
	}

	process = startJobHTTPAgentDockHostProfile(t, binary, manifest, port, home, defaultDir, logs, jobsRoot)
	processStopped = false
	waitJobHTTPReady(t, port, process, 20*time.Second)
	client, closeClient = d1ConnectHTTPClient(t, endpoint)
	clientClosed = false
	statusAAfter, _ := d1Call(t, client, "session_status", map[string]any{"session_id": idA}, 15*time.Second)
	statusBAfter, _ := d1Call(t, client, "session_status", map[string]any{"session_id": idB}, 15*time.Second)
	afterA := d1SessionFromResult(t, statusAAfter)
	afterB := d1SessionFromResult(t, statusBAfter)
	if stringValue(afterA["state"]) != "ACTIVE" || stringValue(afterB["state"]) != "ACTIVE" || stringValue(afterA["branch"]) != branchA || stringValue(afterB["branch"]) != branchB || !strings.EqualFold(stringValue(afterA["worktree"]), worktreeA) || !strings.EqualFold(stringValue(afterB["worktree"]), worktreeB) {
		t.Fatalf("restart lost Session metadata A=%#v B=%#v", afterA, afterB)
	}
	if afterA["runtime_id"] != nil || afterB["runtime_id"] != nil {
		t.Fatalf("restart should recover runtime binding lazily A=%#v B=%#v", afterA["runtime_id"], afterB["runtime_id"])
	}
	failedStatus, _ := d1Call(t, client, "session_status", map[string]any{"session_id": failedID}, 15*time.Second)
	failedRecovered := d1SessionFromResult(t, failedStatus)
	if stringValue(failedRecovered["state"]) != "FAILED" || stringValue(failedRecovered["failure_reason"]) == "" {
		t.Fatalf("broken worktree did not recover FAILED: %#v", failedRecovered)
	}
	d1ExpectError(t, client, "session_close", map[string]any{"session_id": failedID}, "SESSION_FAILED")

	readAAfter, _ := d1Call(t, client, "opencode_read", map[string]any{"session_id": idA, "arguments": map[string]any{"filePath": "foo.txt"}}, 3*time.Minute)
	if !strings.Contains(outerJSON(readAAfter), "A_ONLY") {
		t.Fatalf("A failed to reacquire OpenCode after restart: %s", outerJSON(readAAfter))
	}
	statusARebound, _ := d1Call(t, client, "session_status", map[string]any{"session_id": idA}, 15*time.Second)
	if runtimeID := stringValue(d1SessionFromResult(t, statusARebound)["runtime_id"]); runtimeID != "session:"+idA {
		t.Fatalf("A runtime_id not rebound after real OpenCode call: %q", runtimeID)
	}

	// session_close must cancel only A-owned active work, preserve A's dirty
	// worktree/branch, remain idempotent, and leave B active.
	closeJobA, _ := d1Call(t, client, "job_start", map[string]any{"session_id": idA, "tool": "bash", "arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "Start-Sleep -Seconds 120; Write-Output A_CLOSE_DONE"`}}, 15*time.Second)
	closeJobB, _ := d1Call(t, client, "job_start", map[string]any{"session_id": idB, "tool": "bash", "arguments": map[string]any{"command": `powershell.exe -NoProfile -NonInteractive -Command "Start-Sleep -Seconds 120; Write-Output B_CLOSE_DONE"`}}, 15*time.Second)
	closeJobIDa := stringValue(closeJobA.StructuredContent.(map[string]any)["job_id"])
	closeJobIDb := stringValue(closeJobB.StructuredContent.(map[string]any)["job_id"])
	d1WaitJobState(t, client, idA, closeJobIDa, 20*time.Second, "RUNNING")
	d1WaitJobState(t, client, idB, closeJobIDb, 20*time.Second, "RUNNING")
	closedAResult, _ := d1Call(t, client, "session_close", map[string]any{"session_id": idA}, 30*time.Second)
	closedA := d1SessionFromResult(t, closedAResult)
	if stringValue(closedA["state"]) != "CLOSED" {
		t.Fatalf("session_close A state=%#v", closedA)
	}
	cancelled, _ := closedAResult.StructuredContent.(map[string]any)["jobs_cancelled"].([]any)
	if !d1AnyString(cancelled, closeJobIDa) {
		t.Fatalf("session_close A did not report owned active job cancellation: %s", outerJSON(closedAResult))
	}
	jobAAfterClose, _ := d1Call(t, client, "job_status", map[string]any{"job_id": closeJobIDa}, 15*time.Second)
	if stringValue(jobAAfterClose.StructuredContent.(map[string]any)["state"]) != "CANCELLED" {
		t.Fatalf("A job not cancelled by close: %s", outerJSON(jobAAfterClose))
	}
	jobBAfterClose := d1WaitJobState(t, client, idB, closeJobIDb, 5*time.Second, "RUNNING", "SUCCEEDED")
	if state := stringValue(jobBAfterClose["state"]); state != "RUNNING" && state != "SUCCEEDED" {
		t.Fatalf("B job changed by close A: %#v", jobBAfterClose)
	}
	statusBStill, _ := d1Call(t, client, "session_status", map[string]any{"session_id": idB}, 15*time.Second)
	if stringValue(d1SessionFromResult(t, statusBStill)["state"]) != "ACTIVE" {
		t.Fatalf("B Session changed by close A: %s", outerJSON(statusBStill))
	}
	mustContainOuterFile(t, filepath.Join(worktreeA, "foo.txt"), "A_ONLY")
	if got := strings.TrimSpace(d1GitOutput(t, worktreeA, "rev-parse", "--abbrev-ref", "HEAD")); got != branchA {
		t.Fatalf("A branch changed by close got=%q want=%q", got, branchA)
	}
	if got := strings.TrimSpace(d1GitOutput(t, worktreeB, "rev-parse", "--abbrev-ref", "HEAD")); got != branchB {
		t.Fatalf("B branch changed by close A got=%q want=%q", got, branchB)
	}
	closedAgain, _ := d1Call(t, client, "session_close", map[string]any{"session_id": idA}, 15*time.Second)
	if stringValue(d1SessionFromResult(t, closedAgain)["state"]) != "CLOSED" {
		t.Fatalf("repeat close not idempotent: %s", outerJSON(closedAgain))
	}
	d1ExpectError(t, client, "opencode_read", map[string]any{"session_id": idA, "arguments": map[string]any{"filePath": "foo.txt"}}, "SESSION_CLOSED")
	_, _ = d1Call(t, client, "job_cancel", map[string]any{"session_id": idB, "job_id": closeJobIDb, "reason": "test cleanup"}, 20*time.Second)

	// Non-Git external isolation and read-only mutation denial.
	nonGitA, _ := d1Call(t, client, "session_open", map[string]any{"project_id": "d1-non-git", "repo_root": nonGit, "isolation_mode": "exclusive"}, 15*time.Second)
	nonGitID := stringValue(d1SessionFromResult(t, nonGitA)["session_id"])
	d1ExpectError(t, client, "session_open", map[string]any{"project_id": "d1-non-git", "repo_root": nonGit, "isolation_mode": "exclusive"}, "SESSION_CONFLICT")
	_, _ = d1Call(t, client, "session_close", map[string]any{"session_id": nonGitID}, 15*time.Second)
	readonlyA, _ := d1Call(t, client, "session_open", map[string]any{"project_id": "d1-non-git", "repo_root": nonGit, "isolation_mode": "shared-readonly"}, 15*time.Second)
	readonlyB, _ := d1Call(t, client, "session_open", map[string]any{"project_id": "d1-non-git", "repo_root": nonGit, "isolation_mode": "shared-readonly"}, 15*time.Second)
	readonlyIDa := stringValue(d1SessionFromResult(t, readonlyA)["session_id"])
	readonlyIDb := stringValue(d1SessionFromResult(t, readonlyB)["session_id"])
	d1ExpectError(t, client, "opencode_write", map[string]any{"session_id": readonlyIDa, "arguments": map[string]any{"filePath": "denied.txt", "content": "no"}}, "SESSION_WRITE_DENIED")
	_, _ = d1Call(t, client, "session_close", map[string]any{"session_id": readonlyIDa}, 15*time.Second)
	_, _ = d1Call(t, client, "session_close", map[string]any{"session_id": readonlyIDb}, 15*time.Second)
	_, _ = d1Call(t, client, "session_close", map[string]any{"session_id": idB}, 20*time.Second)

	t.Logf("D1_REAL_E2E session_A=%s session_B=%s branch_A=%s branch_B=%s worktree_A=%s worktree_B=%s file_isolation=PASS git_isolation=PASS job_binding=PASS cancel_isolation=PASS close_isolation=PASS restart=PASS non_git=PASS security_errors=PASS", idA, idB, branchA, branchB, worktreeA, worktreeB)
}

func writeD1CancellationFixtures(t *testing.T, workspace string) {
	t.Helper()
	parent := `const fs=require("fs"); const cp=require("child_process"); const [childPidFile,grandPidFile,marker]=process.argv.slice(2); fs.writeFileSync(childPidFile,String(process.pid)); const child=cp.spawn(process.execPath,["cancel-grandchild.cjs",grandPidFile,marker],{stdio:"ignore"}); child.on("exit",()=>{}); setTimeout(()=>{},120000);`
	grandchild := `const fs=require("fs"); const [pidFile,marker]=process.argv.slice(2); fs.writeFileSync(pidFile,String(process.pid)); setTimeout(()=>fs.writeFileSync(marker,"should-not-exist"),90000); setTimeout(()=>{},120000);`
	mustWriteOuter(t, filepath.Join(workspace, "cancel-parent.cjs"), parent)
	mustWriteOuter(t, filepath.Join(workspace, "cancel-grandchild.cjs"), grandchild)
}

func d1ToolMap(t *testing.T, session *mcpsdk.ClientSession) map[string]*mcpsdk.Tool {
	t.Helper()
	tools := map[string]*mcpsdk.Tool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools[tool.Name] = tool
	}
	return tools
}

func d1ConnectHTTPClient(t *testing.T, endpoint string) (*mcpsdk.ClientSession, func()) {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agentdock-d1-session-e2e", Version: "1"}, &mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: endpoint,
		// Each D1 Git Session intentionally gets a fresh isolated worktree and the
		// Windows E2E also gives AgentDock a fresh USERPROFILE. On a genuinely cold
		// machine the frozen Bun/OpenCode workspace may populate its local cache
		// before the first tool call. Keep this transport budget above the per-call
		// cold-start budget so the test reports the actual tool result, not an HTTP
		// client timeout.
		HTTPClient:           &http.Client{Timeout: 4 * time.Minute},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("D1 HTTP MCP connect failed: %v", err)
	}
	return session, func() { _ = session.Close() }
}

func d1AssertDirectToolSurface(t *testing.T, tools map[string]*mcpsdk.Tool) {
	t.Helper()
	expected := []string{
		"opencode_bash", "opencode_read", "opencode_glob", "opencode_grep", "opencode_edit", "opencode_write", "opencode_apply_patch", "opencode_lsp", "opencode_git",
		"job_start", "job_status", "job_output", "job_cancel", "job_list",
		"session_open", "session_status", "session_list", "session_close",
	}
	matched := 0
	for _, name := range expected {
		if tools[name] == nil {
			t.Fatalf("raw tools/list missing D1 direct tool %s", name)
		}
		matched++
	}
	if matched != 18 {
		t.Fatalf("D1 expected direct tool match count=%d", matched)
	}
}

func d1AssertSessionSchemas(t *testing.T, tools map[string]*mcpsdk.Tool) {
	t.Helper()
	type shape struct {
		inputProps, inputRequired, outputProps, outputRequired []string
	}
	want := map[string]shape{
		"session_open":   {[]string{"base_ref", "client_tag", "isolation_mode", "permission_mode", "project_id", "repo_root"}, []string{"project_id", "repo_root"}, []string{"ok", "session"}, []string{"ok", "session"}},
		"session_status": {[]string{"session_id"}, []string{"session_id"}, []string{"ok", "session"}, []string{"ok", "session"}},
		"session_list":   {[]string{"client_tag", "cursor", "limit", "project_id", "state_filter"}, nil, []string{"next_cursor", "ok", "sessions"}, []string{"next_cursor", "ok", "sessions"}},
		"session_close":  {[]string{"session_id"}, []string{"session_id"}, []string{"branch_preserved", "jobs_cancelled", "ok", "session", "worktree_preserved"}, []string{"branch_preserved", "jobs_cancelled", "ok", "session", "worktree_preserved"}},
	}
	for name, expected := range want {
		tool := tools[name]
		if tool == nil {
			t.Fatalf("schema check missing %s", name)
		}
		input := d1SchemaMap(t, name+" input", tool.InputSchema)
		output := d1SchemaMap(t, name+" output", tool.OutputSchema)
		if additional, ok := input["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s input additionalProperties=%#v want false", name, input["additionalProperties"])
		}
		if additional, ok := output["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s output additionalProperties=%#v want false", name, output["additionalProperties"])
		}
		d1AssertStringSet(t, name+" input props", d1PropertyNames(input), expected.inputProps)
		d1AssertStringSet(t, name+" input required", d1StringSlice(input["required"]), expected.inputRequired)
		d1AssertStringSet(t, name+" output props", d1PropertyNames(output), expected.outputProps)
		d1AssertStringSet(t, name+" output required", d1StringSlice(output["required"]), expected.outputRequired)
	}

	openInput := d1SchemaMap(t, "session_open input", tools["session_open"].InputSchema)
	openProps := openInput["properties"].(map[string]any)
	if got := openProps["permission_mode"].(map[string]any)["default"]; got != "project" {
		t.Fatalf("raw session_open permission_mode default=%#v want project", got)
	}
	for _, name := range []string{"base_ref", "client_tag", "isolation_mode"} {
		prop := openProps[name].(map[string]any)
		value, exists := prop["default"]
		if !exists || value != nil {
			t.Fatalf("raw session_open %s default=%#v exists=%v want null", name, value, exists)
		}
	}

	listInput := d1SchemaMap(t, "session_list input", tools["session_list"].InputSchema)
	listProps := listInput["properties"].(map[string]any)
	if got := listProps["state_filter"].(map[string]any)["default"]; got != "all" {
		t.Fatalf("raw session_list state_filter default=%#v want all", got)
	}
	if got := listProps["limit"].(map[string]any)["default"]; got != float64(100) && got != 100 {
		t.Fatalf("raw session_list limit default=%#v want 100", got)
	}
	for _, name := range []string{"project_id", "client_tag", "cursor"} {
		prop := listProps[name].(map[string]any)
		value, exists := prop["default"]
		if !exists || value != nil {
			t.Fatalf("raw session_list %s default=%#v exists=%v want null", name, value, exists)
		}
	}

	statusOutput := d1SchemaMap(t, "session_status output", tools["session_status"].OutputSchema)
	sessionSchema := statusOutput["properties"].(map[string]any)["session"].(map[string]any)
	sessionProps := sessionSchema["properties"].(map[string]any)
	if got := sessionProps["metadata_version"].(map[string]any)["const"]; got != "D1-v1" {
		t.Fatalf("raw session metadata_version const=%#v want D1-v1", got)
	}
	if got := sessionProps["session_id"].(map[string]any)["maxLength"]; got != float64(128) && got != 128 {
		t.Fatalf("raw session_id maxLength=%#v want 128", got)
	}
	if got := sessionProps["runtime_id"].(map[string]any)["maxLength"]; got != float64(256) && got != 256 {
		t.Fatalf("raw runtime_id maxLength=%#v want 256", got)
	}
	if got := sessionProps["failure_reason"].(map[string]any)["maxLength"]; got != float64(4096) && got != 4096 {
		t.Fatalf("raw failure_reason maxLength=%#v want 4096", got)
	}
	if rules, ok := sessionSchema["allOf"].([]any); !ok || len(rules) != 3 {
		if typed, typedOK := sessionSchema["allOf"].([]map[string]any); !typedOK || len(typed) != 3 {
			t.Fatalf("raw Session allOf=%T want 3 frozen conditional rules", sessionSchema["allOf"])
		}
	}

	closeOutput := d1SchemaMap(t, "session_close output", tools["session_close"].OutputSchema)
	closeProps := closeOutput["properties"].(map[string]any)
	jobsCancelled := closeProps["jobs_cancelled"].(map[string]any)
	items := jobsCancelled["items"].(map[string]any)
	if got := items["minLength"]; got != float64(1) && got != 1 {
		t.Fatalf("raw session_close jobs_cancelled item minLength=%#v want 1", got)
	}
}

func d1SchemaMap(t *testing.T, label string, value any) map[string]any {
	t.Helper()
	schema, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s schema type=%T", label, value)
	}
	return schema
}

func d1PropertyNames(schema map[string]any) []string {
	properties, _ := schema["properties"].(map[string]any)
	out := make([]string, 0, len(properties))
	for name := range properties {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func d1StringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, stringValue(item))
		}
		return out
	default:
		return nil
	}
}

func d1AssertStringSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s got=%v want=%v", label, got, want)
	}
}

func d1Call(t *testing.T, session *mcpsdk.ClientSession, name string, arguments map[string]any, timeout time.Duration) (*mcpsdk.CallToolResult, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	started := time.Now()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	latency := time.Since(started)
	if err != nil {
		t.Fatalf("%s transport error after %s: %v", name, latency, err)
	}
	if result == nil || result.IsError {
		t.Fatalf("%s failed after %s: %s", name, latency, outerJSON(result))
	}
	return result, latency
}

func d1ExpectError(t *testing.T, session *mcpsdk.ClientSession, name string, arguments map[string]any, code string) *mcpsdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s expected structured %s but got transport error: %v", name, code, err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("%s expected %s but succeeded: %s", name, code, outerJSON(result))
	}
	if !strings.Contains(outerJSON(result), code) {
		t.Fatalf("%s expected code %s: %s", name, code, outerJSON(result))
	}
	return result
}

func d1SessionFromResult(t *testing.T, result *mcpsdk.CallToolResult) map[string]any {
	t.Helper()
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("session structured content type=%T body=%s", result.StructuredContent, outerJSON(result))
	}
	record, ok := structured["session"].(map[string]any)
	if !ok {
		t.Fatalf("session record type=%T body=%s", structured["session"], outerJSON(result))
	}
	return record
}

func d1WaitJobState(t *testing.T, session *mcpsdk.ClientSession, sessionID, jobID string, timeout time.Duration, states ...string) map[string]any {
	t.Helper()
	want := map[string]bool{}
	for _, state := range states {
		want[state] = true
	}
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		result, _ := d1Call(t, session, "job_status", map[string]any{"session_id": sessionID, "job_id": jobID}, 10*time.Second)
		last, _ = result.StructuredContent.(map[string]any)
		if want[stringValue(last["state"])] {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting job %s for %v last=%#v", jobID, states, last)
	return last
}

func d1AnyString(values []any, want string) bool {
	for _, value := range values {
		if stringValue(value) == want {
			return true
		}
	}
	return false
}

func d1GitOutput(t *testing.T, workspace string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
