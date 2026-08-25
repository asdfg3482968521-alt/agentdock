//go:build windows

package process

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestConfigureHidesConsoleWindow(t *testing.T) {
	cmd := exec.Command("cmd.exe")

	Configure(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("Configure did not initialize Windows process attributes")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("Configure did not hide the child process window")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("creation flags = %#x, want CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
}

func TestConfigurePreservesExistingCreationFlags(t *testing.T) {
	existingFlags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: existingFlags}

	Configure(cmd)

	wantFlags := existingFlags | uint32(windows.CREATE_NO_WINDOW)
	if cmd.SysProcAttr.CreationFlags != wantFlags {
		t.Fatalf("creation flags = %#x, want %#x", cmd.SysProcAttr.CreationFlags, wantFlags)
	}
}

func TestWindowsJobObjectTerminatesAttachedProcess(t *testing.T) {
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	controller, err := Attach(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := controller.Terminate(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("terminated process exited successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Windows Job Object did not terminate the process")
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsJobObjectDoesNotTerminateUnrelatedProcess(t *testing.T) {
	unrelated := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = unrelated.Process.Kill()
		_ = unrelated.Wait()
	}()

	owned := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 30")
	if err := owned.Start(); err != nil {
		t.Fatal(err)
	}
	controller, err := Attach(owned)
	if err != nil {
		_ = owned.Process.Kill()
		_ = owned.Wait()
		t.Fatal(err)
	}
	defer controller.Close()
	if err := controller.Terminate(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- owned.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned process did not terminate")
	}

	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(unrelated.Process.Pid))
	if err != nil {
		t.Fatalf("unrelated process is no longer queryable: %v", err)
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("unrelated process unexpectedly terminated, wait state=%d", state)
	}
}
