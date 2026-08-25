//go:build windows

package command

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/envstore"
)

func TestCommandEnvPreservesWindowsProgramFilesVariables(t *testing.T) {
	home := t.TempDir()
	envs, err := envstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(
		func() config.Config { return config.Config{AgentDockHome: home, AgentDockDefaultDir: home} },
		nil,
		envs,
		NewSessionStore(),
		nil,
		nil,
	)

	t.Setenv("PROGRAMFILES", `C:\Program Files`)
	t.Setenv("PROGRAMW6432", `C:\Program Files`)
	got, err := svc.CommandEnv("", nil)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for _, key := range []string{"PROGRAMFILES", "PROGRAMW6432"} {
		if values[key] != `C:\Program Files` {
			t.Fatalf("%s = %q, want %q", key, values[key], `C:\Program Files`)
		}
	}
}

func TestCommandEnvPreservesWindowsHostEnvironment(t *testing.T) {
	home := t.TempDir()
	envs, err := envstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(
		func() config.Config { return config.Config{AgentDockHome: home, AgentDockDefaultDir: home} },
		nil,
		envs,
		NewSessionStore(),
		nil,
		nil,
	)

	root := t.TempDir()
	want := map[string]string{
		"USERPROFILE":  filepath.Join(root, "profile"),
		"HOME":         filepath.Join(root, "home"),
		"LOCALAPPDATA": filepath.Join(root, "local"),
		"APPDATA":      filepath.Join(root, "roaming"),
		"TEMP":         filepath.Join(root, "temp"),
		"TMP":          filepath.Join(root, "tmp"),
		"PATH":         filepath.Join(root, "bin"),
	}
	for key, value := range want {
		t.Setenv(key, value)
	}

	got, err := svc.CommandEnv("", nil)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, expected := range want {
		if values[key] != expected {
			t.Fatalf("%s = %q, want %q", key, values[key], expected)
		}
	}
}

func TestCommandEnvDoesNotInventHomeFromUserProfileWhenParentHomeMissing(t *testing.T) {
	home := t.TempDir()
	envs, err := envstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(
		func() config.Config { return config.Config{AgentDockHome: home, AgentDockDefaultDir: home} },
		nil,
		envs,
		NewSessionStore(),
		nil,
		nil,
	)

	originalHome, hadHome := os.LookupEnv("HOME")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if hadHome {
			_ = os.Setenv("HOME", originalHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	}()
	t.Setenv("USERPROFILE", filepath.Join(t.TempDir(), "profile"))

	got, err := svc.CommandEnv("", nil)
	if err != nil {
		t.Fatal(err)
	}
	values := formattedCommandEnvironment(got)
	if values["HOME"] != "" {
		t.Fatalf("HOME was invented as %q", values["HOME"])
	}
}

func TestCommandEnvKeepsParentPathUsableByChild(t *testing.T) {
	home := t.TempDir()
	envs, err := envstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(
		func() config.Config { return config.Config{AgentDockHome: home, AgentDockDefaultDir: home} },
		nil,
		envs,
		NewSessionStore(),
		nil,
		nil,
	)

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(bin, "mb-path-probe.cmd")
	if err := os.WriteFile(probe, []byte("@echo %AGENTDOCK_M_B_PARENT_SENTINEL%\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENTDOCK_M_B_PARENT_SENTINEL", "child-inherited")

	commandEnv, err := svc.CommandEnv("", nil)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Getenv("COMSPEC"), "/D", "/S", "/C", "mb-path-probe.cmd")
	command.Dir = home
	command.Env = commandEnv
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("child command failed: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "child-inherited" {
		t.Fatalf("child output = %q, want child-inherited", got)
	}
}
