package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Manifest struct {
	RuntimeVersion  string   `json:"runtimeVersion"`
	VerifiedSHA     string   `json:"verifiedSha"`
	Executable      string   `json:"executable"`
	Args            []string `json:"args,omitempty"`
	Cwd             string   `json:"cwd"`
	SourceRoot      string   `json:"sourceRoot"`
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
}

type ManifestLocator struct {
	Path string
}

func (l ManifestLocator) Locate(ctx context.Context) (RuntimeLocation, error) {
	path := filepath.Clean(strings.TrimSpace(l.Path))
	if path == "" || path == "." || !filepath.IsAbs(path) {
		return RuntimeLocation{}, newError("OPENCODE_MANIFEST_INVALID", "OpenCode runtime manifest path must be absolute", false, map[string]any{"path": l.Path}, nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RuntimeLocation{}, newError("OPENCODE_MANIFEST_READ_FAILED", "read OpenCode runtime manifest", false, map[string]any{"path": path}, err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return RuntimeLocation{}, newError("OPENCODE_MANIFEST_INVALID", "decode OpenCode runtime manifest", false, map[string]any{"path": path}, err)
	}
	manifest.RuntimeVersion = strings.TrimSpace(manifest.RuntimeVersion)
	manifest.VerifiedSHA = strings.ToLower(strings.TrimSpace(manifest.VerifiedSHA))
	manifest.Executable = filepath.Clean(strings.TrimSpace(manifest.Executable))
	manifest.Cwd = filepath.Clean(strings.TrimSpace(manifest.Cwd))
	manifest.SourceRoot = filepath.Clean(strings.TrimSpace(manifest.SourceRoot))
	manifest.ProtocolVersion = strings.TrimSpace(manifest.ProtocolVersion)
	if manifest.RuntimeVersion == "" || manifest.VerifiedSHA == "" || manifest.Executable == "." || manifest.Cwd == "." || manifest.SourceRoot == "." {
		return RuntimeLocation{}, newError("OPENCODE_MANIFEST_INVALID", "runtimeVersion, verifiedSha, executable, cwd, and sourceRoot are required", false, map[string]any{"path": path}, nil)
	}
	for label, value := range map[string]string{"executable": manifest.Executable, "cwd": manifest.Cwd, "sourceRoot": manifest.SourceRoot} {
		if !filepath.IsAbs(value) {
			return RuntimeLocation{}, newError("OPENCODE_MANIFEST_INVALID", fmt.Sprintf("%s must be absolute", label), false, map[string]any{"path": path, label: value}, nil)
		}
	}
	if info, err := os.Stat(manifest.Executable); err != nil || info.IsDir() {
		return RuntimeLocation{}, newError("OPENCODE_RUNTIME_NOT_FOUND", "OpenCode runtime executable is unavailable", false, map[string]any{"executable": manifest.Executable}, err)
	}
	for label, value := range map[string]string{"cwd": manifest.Cwd, "sourceRoot": manifest.SourceRoot} {
		info, err := os.Stat(value)
		if err != nil || !info.IsDir() {
			return RuntimeLocation{}, newError("OPENCODE_RUNTIME_NOT_FOUND", fmt.Sprintf("OpenCode runtime %s is unavailable", label), false, map[string]any{label: value}, err)
		}
	}
	actualSHA, err := gitHead(ctx, manifest.SourceRoot)
	if err != nil {
		return RuntimeLocation{}, newError("OPENCODE_RUNTIME_VERIFY_FAILED", "read OpenCode runtime Git HEAD", false, map[string]any{"source_root": manifest.SourceRoot}, err)
	}
	if !strings.EqualFold(actualSHA, manifest.VerifiedSHA) {
		return RuntimeLocation{}, newError(
			"OPENCODE_RUNTIME_SHA_MISMATCH",
			"OpenCode runtime HEAD does not match verified manifest SHA",
			false,
			map[string]any{"source_root": manifest.SourceRoot, "expected_sha": manifest.VerifiedSHA, "actual_sha": actualSHA},
			nil,
		)
	}
	return RuntimeLocation{
		RuntimeVersion: manifest.RuntimeVersion, VerifiedSHA: manifest.VerifiedSHA,
		Executable: manifest.Executable, Args: append([]string(nil), manifest.Args...), Cwd: manifest.Cwd,
		SourceRoot: manifest.SourceRoot, ProtocolVersion: manifest.ProtocolVersion,
	}, nil
}

func gitHead(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	sha := strings.ToLower(strings.TrimSpace(string(output)))
	if len(sha) != 40 {
		return "", fmt.Errorf("unexpected Git HEAD %q", sha)
	}
	return sha, nil
}
