//go:build windows

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/credential"
)

func TestCredentialCommandSetListDeleteNeverPrintsSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTDOCK_HOME", home)
	t.Setenv("AGENTDOCK_DEFAULT_DIR", t.TempDir())
	t.Setenv("AGENTDOCK_OPENCODE_ENABLED", "false")

	ctx := context.Background()
	ref := "cred_anthropic_cli"
	marker := "D2_TEST_SECRET_cli_provisioning"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runCredentialCommand(ctx, []string{"set", "--ref", ref, "--provider", "anthropic"}, strings.NewReader(marker+"\r\n"), &stdout, &stderr); err != nil {
		t.Fatalf("credential set error = %v, stderr=%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), marker) || strings.Contains(stderr.String(), marker) {
		t.Fatal("credential set output leaked plaintext marker")
	}

	store, err := credential.NewStore(home)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.Resolve(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(resolved) != marker {
		t.Fatalf("resolved credential = %q, want marker", resolved)
	}
	credential.Zero(resolved)

	stdout.Reset()
	stderr.Reset()
	if err := runCredentialCommand(ctx, []string{"list"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("credential list error = %v", err)
	}
	if !strings.Contains(stdout.String(), ref) || !strings.Contains(stdout.String(), "anthropic") {
		t.Fatalf("credential list output = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), marker) || strings.Contains(stderr.String(), marker) {
		t.Fatal("credential list output leaked plaintext marker")
	}

	stdout.Reset()
	stderr.Reset()
	if err := runCredentialCommand(ctx, []string{"delete", "--ref", ref}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("credential delete error = %v", err)
	}
	if strings.Contains(stdout.String(), marker) || strings.Contains(stderr.String(), marker) {
		t.Fatal("credential delete output leaked plaintext marker")
	}
}

func TestCredentialCommandHasNoPlaintextGetAction(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	t.Setenv("AGENTDOCK_DEFAULT_DIR", t.TempDir())
	t.Setenv("AGENTDOCK_OPENCODE_ENABLED", "false")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runCredentialCommand(context.Background(), []string{"get", "--ref", "cred_abc"}, strings.NewReader(""), &stdout, &stderr); err == nil {
		t.Fatal("credential get unexpectedly accepted")
	}
}
