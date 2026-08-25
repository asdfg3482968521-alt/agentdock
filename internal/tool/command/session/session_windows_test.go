//go:build windows

package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsPowerShellOutputIsUTF8(t *testing.T) {
	s, _, err := Start(
		context.Background(),
		"[Console]::Out.Write('中文输出')",
		t.TempDir(),
		os.Environ(),
		5*time.Second,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Cancel()
	<-s.Done
	if err := s.WaitError(); err != nil {
		t.Fatal(err)
	}
	result := s.Snapshot("exited", 4096)
	if result["stdout"] != "中文输出" {
		t.Fatalf("stdout = %#v", result["stdout"])
	}
}

func TestWindowsTTYUsesConPTYAndAcceptsInput(t *testing.T) {
	s, _, err := StartWithTTY(
		context.Background(),
		"$line=[Console]::In.ReadLine(); [Console]::Out.Write(\"received:$line\")",
		t.TempDir(),
		os.Environ(),
		10*time.Second,
		true,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Cancel()
	if s.Terminal != "conpty" {
		t.Fatalf("terminal = %q, want conpty", s.Terminal)
	}
	if err := s.Write("hello\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("ConPTY command did not finish")
	}
	if err := s.WaitError(); err != nil {
		t.Fatal(err)
	}
	result := s.Snapshot("exited", 4096)
	stdout, _ := result["stdout"].(string)
	if !strings.Contains(stdout, "received:hello") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestWindowsSpawnFailureDoesNotCorruptSubsequentSpawn(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-child.exe")
	_, _, err := StartCommandWithTTY(
		context.Background(),
		func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, missing) },
		5*time.Second,
		false,
		nil,
	)
	if err == nil {
		t.Fatal("missing child unexpectedly started")
	}

	s, _, err := Start(
		context.Background(),
		"[Console]::Out.Write('subsequent-ok')",
		t.TempDir(),
		os.Environ(),
		5*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("subsequent spawn failed after prior error: %v", err)
	}
	defer s.Cancel()
	select {
	case <-s.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("subsequent child did not finish")
	}
	if err := s.WaitError(); err != nil {
		t.Fatal(err)
	}
	result := s.Snapshot("exited", 4096)
	if result["stdout"] != "subsequent-ok" {
		t.Fatalf("subsequent stdout = %#v", result["stdout"])
	}
}
