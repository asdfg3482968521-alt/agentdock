package command

import (
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/envstore"
)

func TestCommandEnvExplicitPathOverridesPlatformDefault(t *testing.T) {
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

	t.Setenv("PATH", "/usr/bin:/bin")
	got, err := svc.CommandEnv("", map[string]any{"PATH": "/custom/bin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range got {
		if strings.HasPrefix(entry, "PATH=") {
			if entry != "PATH=/custom/bin" {
				t.Fatalf("PATH override = %q, want %q", entry, "PATH=/custom/bin")
			}
			return
		}
	}
	t.Fatal("command environment missing PATH")
}

func TestCommandEnvAgentDockSkillAndRequestOverridePrecedence(t *testing.T) {
	home := t.TempDir()
	defaultDir := t.TempDir()
	envs, err := envstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := envs.Set(envstore.Scope{Kind: envstore.ScopeSkill, Name: "demo"}, "AGENTDOCK_M_B_PRECEDENCE", "skill-value"); err != nil {
		t.Fatal(err)
	}
	svc := New(
		func() config.Config { return config.Config{AgentDockHome: home, AgentDockDefaultDir: defaultDir} },
		nil,
		envs,
		NewSessionStore(),
		nil,
		nil,
	)

	t.Setenv("AGENTDOCK_HOME", "parent-home")
	t.Setenv("AGENTDOCK_DEFAULT_DIR", "parent-default")
	t.Setenv("AGENTDOCK_M_B_PRECEDENCE", "parent-value")

	skillOnly, err := svc.CommandEnv("demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	skillValues := formattedCommandEnvironment(skillOnly)
	if skillValues["AGENTDOCK_HOME"] != home || skillValues["AGENTDOCK_DEFAULT_DIR"] != defaultDir {
		t.Fatalf("AgentDock overrides did not win parent: %#v", skillValues)
	}
	if skillValues["AGENTDOCK_M_B_PRECEDENCE"] != "skill-value" {
		t.Fatalf("Skill override = %q, want skill-value", skillValues["AGENTDOCK_M_B_PRECEDENCE"])
	}

	request, err := svc.CommandEnv("demo", map[string]any{"AGENTDOCK_M_B_PRECEDENCE": "request-value"})
	if err != nil {
		t.Fatal(err)
	}
	requestValues := formattedCommandEnvironment(request)
	if requestValues["AGENTDOCK_M_B_PRECEDENCE"] != "request-value" {
		t.Fatalf("request override = %q, want request-value", requestValues["AGENTDOCK_M_B_PRECEDENCE"])
	}
}

func formattedCommandEnvironment(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func TestCommandEnvInheritsParentEnvironmentAndExplicitOverrideWins(t *testing.T) {
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

	t.Setenv("AGENTDOCK_M_B_PARENT_SENTINEL", "parent-value")
	t.Setenv("AGENTDOCK_M_B_OVERRIDE_SENTINEL", "parent-value")
	got, err := svc.CommandEnv("", map[string]any{"AGENTDOCK_M_B_OVERRIDE_SENTINEL": "override-value"})
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
	if values["AGENTDOCK_M_B_PARENT_SENTINEL"] != "parent-value" {
		t.Fatalf("parent environment was not inherited: %#v", values)
	}
	if values["AGENTDOCK_M_B_OVERRIDE_SENTINEL"] != "override-value" {
		t.Fatalf("explicit override = %q, want override-value", values["AGENTDOCK_M_B_OVERRIDE_SENTINEL"])
	}
}
