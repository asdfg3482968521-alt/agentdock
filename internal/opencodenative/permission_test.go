package opencodenative

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPermissionListAndBoundedReply(t *testing.T) {
	const directory = `C:\work\d3`
	var gotReply PermissionReplyRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != directory {
			http.Error(w, "bad directory", http.StatusBadRequest)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /permission":
			writeJSON(w, []PermissionRequest{{
				ID: "per_1", SessionID: "ses-1", Permission: "bash",
				Patterns: []string{"go test ./..."}, Metadata: map[string]any{"cwd": directory},
				Always: []string{"go test ./..."}, Tool: &PermissionTool{MessageID: "msg-1", CallID: "call-1"},
			}})
		case "POST /permission/per_1/reply":
			if err := json.NewDecoder(r.Body).Decode(&gotReply); err != nil {
				t.Fatal(err)
			}
			writeJSON(w, true)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := client.ListNativePermissions(context.Background(), directory)
	if err != nil || len(pending) != 1 || pending[0].ID != "per_1" || pending[0].Tool == nil || pending[0].Tool.CallID != "call-1" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := client.ReplyNativePermission(context.Background(), directory, "per_1", PermissionReplyRequest{Reply: PermissionReplyOnce}); err != nil {
		t.Fatal(err)
	}
	if gotReply.Reply != PermissionReplyOnce {
		t.Fatalf("reply=%q", gotReply.Reply)
	}
	if err := client.ReplyNativePermission(context.Background(), directory, "per_1", PermissionReplyRequest{Reply: PermissionReply("allow-forever")}); ErrorCode(err) != ErrorInvalid {
		t.Fatalf("invalid reply should be rejected locally: %v", err)
	}
}
