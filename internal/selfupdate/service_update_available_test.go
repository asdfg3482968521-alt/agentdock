package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServiceUpdateCapabilityIsAvailableAndNotLocked proves the D3-I service
// update path is normally available: when a newer release exists the update
// runner downloads, verifies and applies it. There is no lock gate that would
// render the AgentDock service update unavailable (the wrong D3 custom
// "updates locked" semantics must never be reintroduced).
func TestServiceUpdateCapabilityIsAvailableAndNotLocked(t *testing.T) {
	archive := makeTarGz(t, "bin/agentdock", []byte("new-binary"))
	digest := sha256.Sum256(archive)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			_ = json.NewEncoder(w).Encode(release{
				TagName: "v0.4.5",
				Assets: []releaseAsset{
					{Name: "agentdock_darwin_arm64.tar.gz", URL: server.URL + "/archive"},
					{Name: "agentdock_darwin_arm64.tar.gz.sha256", URL: server.URL + "/checksum"},
				},
			})
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksum":
			fmt.Fprintf(w, "%s  agentdock_darwin_arm64.tar.gz", hex.EncodeToString(digest[:]))
			fmt.Fprintln(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var output strings.Builder
	applied := false
	err := run(context.Background(), options{
		CurrentVersion: "0.4.4",
		ExecutablePath: "/tmp/agentdock",
		GOOS:           "darwin",
		GOARCH:         "arm64",
		ReleaseAPI:     server.URL + "/release",
		HTTPClient:     server.Client(),
		Output:         &output,
		VerifyBinary: func(_ context.Context, path, targetVersion string) error {
			if targetVersion != "v0.4.5" {
				return fmt.Errorf("unexpected target version %q", targetVersion)
			}
			return nil
		},
		Apply: func(_ context.Context, request applyRequest) (applyResult, error) {
			applied = true
			if request.TargetVersion != "v0.4.5" {
				t.Fatalf("unexpected apply request: %#v", request)
			}
			return applyResult{Restarted: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("service update path returned an error, indicating it is locked or broken: %v", err)
	}
	if !applied {
		t.Fatal("service update was not applied; the update path is locked or unavailable")
	}
	for _, forbidden := range []string{"锁定", "已锁定", "locked", "disabled"} {
		if strings.Contains(strings.ToLower(output.String()), forbidden) {
			t.Fatalf("service update output indicates a locked/disabled state: %s", output.String())
		}
	}
}
