//go:build windows

package envstore

import "testing"

func TestMinimalSystemEnvDoesNotInventWindowsProfileOrToolchainPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("APPDATA", "")
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("GOPATH", "")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("TEMP", "")
	t.Setenv("TMP", "")

	environment := MinimalSystemEnv()
	for _, key := range []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA", "GOPATH", "GOMODCACHE"} {
		if value := environment[key]; value != "" {
			t.Fatalf("%s was invented as %q", key, value)
		}
	}
	if environment["HOME"] != home {
		t.Fatalf("HOME = %q, want parent value %q", environment["HOME"], home)
	}
}

func TestMinimalSystemEnvPreservesExistingWindowsProfilePaths(t *testing.T) {
	root := t.TempDir()
	want := map[string]string{
		"USERPROFILE":  root + `\profile`,
		"APPDATA":      root + `\roaming`,
		"LOCALAPPDATA": root + `\local`,
		"TEMP":         root + `\temp`,
		"TMP":          root + `\tmp`,
	}
	for key, value := range want {
		t.Setenv(key, value)
	}
	environment := MinimalSystemEnv()
	for key, expected := range want {
		if environment[key] != expected {
			t.Fatalf("%s = %q, want parent value %q", key, environment[key], expected)
		}
	}
}
