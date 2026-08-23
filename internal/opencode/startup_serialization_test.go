package opencode

import (
	"context"
	"sync"
	"testing"
	"time"
)

type startupConcurrencyLauncher struct {
	mu        sync.Mutex
	active    int
	maxActive int
	entered   chan struct{}
	release   chan struct{}
}

func (l *startupConcurrencyLauncher) Launch(_ context.Context, _ RuntimeLocation, _ string, _ PermissionMode, _ string) (RuntimeConnection, error) {
	l.mu.Lock()
	l.active++
	if l.active > l.maxActive {
		l.maxActive = l.active
	}
	select {
	case l.entered <- struct{}{}:
	default:
	}
	l.mu.Unlock()

	<-l.release

	l.mu.Lock()
	l.active--
	l.mu.Unlock()
	return newFakeConnection(), nil
}

func (l *startupConcurrencyLauncher) maximum() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxActive
}

func TestSupervisorSerializesColdRuntimeStartupAcrossWorkspaces(t *testing.T) {
	launcher := &startupConcurrencyLauncher{entered: make(chan struct{}, 2), release: make(chan struct{})}
	supervisor, err := NewSupervisor(context.Background(), Options{
		Locator: fakeLocator{location: RuntimeLocation{
			RuntimeVersion: "rc1", VerifiedSHA: "d5664f38002d8326eca179cf2efb5774c8ce1247",
			Executable: `C:\fake\bun.exe`, Cwd: `C:\fake\opencode`, SourceRoot: `C:\fake\opencode`, ProtocolVersion: "test-protocol",
		}},
		Launcher: launcher, LogDir: t.TempDir(), RestartCap: 2, RestartBackoff: 5 * time.Millisecond, ShutdownGrace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	done := make(chan error, 2)
	go func() {
		_, err := supervisor.Acquire(context.Background(), t.TempDir(), PermissionProject)
		done <- err
	}()
	go func() {
		_, err := supervisor.Acquire(context.Background(), t.TempDir(), PermissionProject)
		done <- err
	}()

	select {
	case <-launcher.entered:
	case <-time.After(time.Second):
		t.Fatal("first runtime startup did not enter launcher")
	}
	time.Sleep(100 * time.Millisecond)
	close(launcher.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if max := launcher.maximum(); max != 1 {
		t.Fatalf("concurrent cold runtime startups=%d want 1", max)
	}
}
