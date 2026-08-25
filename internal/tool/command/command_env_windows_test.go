//go:build windows

package command

import (
	"fmt"
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

	t.Setenv("ProgramFiles", `C:\Program Files`)
	t.Setenv("ProgramW6432", `C:\Program Files`)
	got, err := svc.CommandEnv("", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"PROGRAMFILES", "PROGRAMW6432"} {
		value, ok := windowsEnvironmentValue(got, key)
		if !ok || value != `C:\Program Files` {
			t.Fatalf("%s = %q, want %q", key, value, `C:\Program Files`)
		}
	}
}

func TestCommandEnvWindowsCaseInsensitiveOverrideWins(t *testing.T) {
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

	const parentKey = "AgentDock_M_B_Casing"
	const overrideKey = "AGENTDOCK_M_B_CASING"
	t.Setenv(parentKey, "parent-value")
	commandEnv, err := svc.CommandEnv("", map[string]any{overrideKey: "override-value"})
	if err != nil {
		t.Fatal(err)
	}

	matches := 0
	observedKey := ""
	observed := ""
	for _, entry := range commandEnv {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, parentKey) {
			matches++
			observedKey = key
			observed = value
		}
	}
	if matches != 1 {
		t.Fatalf("case-insensitive key count = %d, want 1; env=%#v", matches, commandEnv)
	}
	if observedKey != parentKey {
		t.Fatalf("canonical parent key casing = %q, want %q", observedKey, parentKey)
	}
	if observed != "override-value" {
		t.Fatalf("case-insensitive override = %q, want override-value", observed)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCommandEnvWindowsCaseInsensitiveOverrideHelper")
	cmd.Env = append(commandEnv, "AGENTDOCK_M_B_CASING_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper child failed: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "override-value" {
		t.Fatalf("child observed %q, want override-value", got)
	}
}

func TestCommandEnvWindowsCaseInsensitiveOverrideHelper(t *testing.T) {
	if os.Getenv("AGENTDOCK_M_B_CASING_HELPER") != "1" {
		return
	}
	fmt.Print(os.Getenv("agentdock_m_b_casing"))
	os.Exit(0)
}

func TestCommandEnvWindowsRequestOverrideWinsSkillRegardlessOfCasing(t *testing.T) {
	home := t.TempDir()
	envs, err := envstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	const skillKey = "AgentDock_M_B_Skill_Casing"
	const requestKey = "AGENTDOCK_M_B_SKILL_CASING"
	if err := envs.Set(envstore.Scope{Kind: envstore.ScopeSkill, Name: "demo"}, skillKey, "skill-value"); err != nil {
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

	overrides, err := svc.commandEnvOverrides("demo", map[string]any{requestKey: "request-value"})
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	observed := ""
	for key, value := range overrides {
		if strings.EqualFold(key, skillKey) {
			matches++
			observed = value
		}
	}
	if matches != 1 {
		t.Fatalf("case-insensitive override count = %d, want 1: %#v", matches, overrides)
	}
	if observed != "request-value" {
		t.Fatalf("request override = %q, want request-value", observed)
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
	for key, expected := range want {
		value, ok := windowsEnvironmentValue(got, key)
		if !ok || value != expected {
			t.Fatalf("%s = %q, want %q", key, value, expected)
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
	if value, ok := windowsEnvironmentValue(got, "HOME"); ok && value != "" {
		t.Fatalf("HOME was invented as %q", value)
	}
}

func windowsEnvironmentValue(environment []string, name string) (string, bool) {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
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
