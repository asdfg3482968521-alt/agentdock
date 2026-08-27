package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Manifest struct {
	RuntimeVersion  string   `json:"runtimeVersion"`
	VerifiedSHA     string   `json:"verifiedSha"`
	Executable      string   `json:"executable"`
	Args            []string `json:"args,omitempty"`
	Cwd             string   `json:"cwd"`
	SourceRoot      string   `json:"sourceRoot,omitempty"`
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
}

type ManifestLocator struct {
	Path string
}

func (l ManifestLocator) Locate(_ context.Context) (RuntimeLocation, error) {
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

	if manifest.RuntimeVersion == "" || manifest.VerifiedSHA == "" || manifest.Executable == "." || manifest.Cwd == "." {
		return RuntimeLocation{}, newError(
			"OPENCODE_MANIFEST_INVALID",
			"runtimeVersion, verifiedSha, executable, and cwd are required",
			false,
			map[string]any{"path": path},
			nil,
		)
	}
	for label, value := range map[string]string{"executable": manifest.Executable, "cwd": manifest.Cwd} {
		if !filepath.IsAbs(value) {
			return RuntimeLocation{}, newError(
				"OPENCODE_MANIFEST_INVALID",
				fmt.Sprintf("%s must be absolute", label),
				false,
				map[string]any{"path": path, label: value},
				nil,
			)
		}
	}

	// sourceRoot is optional provenance metadata only. AgentDock production
	// startup must not depend on the OpenCode development checkout remaining
	// on disk after the frozen runtime has been installed.
	if manifest.SourceRoot != "" && manifest.SourceRoot != "." && !filepath.IsAbs(manifest.SourceRoot) {
		return RuntimeLocation{}, newError(
			"OPENCODE_MANIFEST_INVALID",
			"sourceRoot must be absolute when provided",
			false,
			map[string]any{"path": path, "sourceRoot": manifest.SourceRoot},
			nil,
		)
	}

	if info, err := os.Stat(manifest.Executable); err != nil || info.IsDir() {
		return RuntimeLocation{}, newError(
			"OPENCODE_RUNTIME_NOT_FOUND",
			"OpenCode runtime executable is unavailable",
			false,
			map[string]any{"executable": manifest.Executable},
			err,
		)
	}
	if info, err := os.Stat(manifest.Cwd); err != nil || !info.IsDir() {
		return RuntimeLocation{}, newError(
			"OPENCODE_RUNTIME_NOT_FOUND",
			"OpenCode runtime cwd is unavailable",
			false,
			map[string]any{"cwd": manifest.Cwd},
			err,
		)
	}

	// verifiedSha remains the frozen runtime identity recorded by the runtime
	// manifest. Do not open sourceRoot, run Git, or compare a development HEAD
	// during production startup.
	return RuntimeLocation{
		RuntimeVersion:  manifest.RuntimeVersion,
		VerifiedSHA:     manifest.VerifiedSHA,
		Executable:      manifest.Executable,
		Args:            append([]string(nil), manifest.Args...),
		Cwd:             manifest.Cwd,
		SourceRoot:      manifest.SourceRoot,
		ProtocolVersion: manifest.ProtocolVersion,
	}, nil
}
