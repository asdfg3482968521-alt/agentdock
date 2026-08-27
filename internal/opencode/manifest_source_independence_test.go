package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeManifestForLocatorTest(t *testing.T, manifest Manifest) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime-manifest.json")
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManifestLocatorDoesNotRequireSourceCheckout(t *testing.T) {
	runtimeDir := t.TempDir()
	executable := filepath.Join(runtimeDir, "opencode-runtime.exe")
	if err := os.WriteFile(executable, []byte("test-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	missingSource := filepath.Join(t.TempDir(), "deleted-opencode-source")
	manifestPath := writeManifestForLocatorTest(t, Manifest{
		RuntimeVersion:  "rc1",
		VerifiedSHA:     "d5664f38002d8326eca179cf2efb5774c8ce1247",
		Executable:      executable,
		Cwd:             runtimeDir,
		SourceRoot:      missingSource,
		ProtocolVersion: "2026-08-11",
	})

	location, err := (ManifestLocator{Path: manifestPath}).Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate() failed after source checkout deletion: %v", err)
	}
	if location.SourceRoot != missingSource {
		t.Fatalf("SourceRoot = %q, want preserved metadata %q", location.SourceRoot, missingSource)
	}
	if location.VerifiedSHA != "d5664f38002d8326eca179cf2efb5774c8ce1247" {
		t.Fatalf("VerifiedSHA = %q", location.VerifiedSHA)
	}
}

func TestManifestLocatorAllowsSourceRootToBeOmitted(t *testing.T) {
	runtimeDir := t.TempDir()
	executable := filepath.Join(runtimeDir, "opencode-runtime.exe")
	if err := os.WriteFile(executable, []byte("test-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeManifestForLocatorTest(t, Manifest{
		RuntimeVersion: "rc1",
		VerifiedSHA:    "d5664f38002d8326eca179cf2efb5774c8ce1247",
		Executable:     executable,
		Cwd:            runtimeDir,
	})

	location, err := (ManifestLocator{Path: manifestPath}).Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate() with omitted sourceRoot failed: %v", err)
	}
	if location.SourceRoot != "." && location.SourceRoot != "" {
		t.Fatalf("unexpected SourceRoot %q", location.SourceRoot)
	}
}

func TestManifestLocatorStillRequiresInstalledRuntimeCwd(t *testing.T) {
	runtimeDir := t.TempDir()
	executable := filepath.Join(runtimeDir, "opencode-runtime.exe")
	if err := os.WriteFile(executable, []byte("test-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	missingCwd := filepath.Join(t.TempDir(), "missing-runtime-cwd")
	manifestPath := writeManifestForLocatorTest(t, Manifest{
		RuntimeVersion: "rc1",
		VerifiedSHA:    "d5664f38002d8326eca179cf2efb5774c8ce1247",
		Executable:     executable,
		Cwd:            missingCwd,
	})

	_, err := (ManifestLocator{Path: manifestPath}).Locate(context.Background())
	var runtimeErr *Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "OPENCODE_RUNTIME_NOT_FOUND" {
		t.Fatalf("error = %#v, want OPENCODE_RUNTIME_NOT_FOUND", err)
	}
}
