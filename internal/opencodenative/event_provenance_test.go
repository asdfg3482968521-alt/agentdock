package opencodenative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeEventSSEIDAndNativeSeqAreProvenance(t *testing.T) {
	const directory = `C:\work\d3`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("directory") != directory {
			http.Error(w, "bad directory", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/event" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: native-sse-42\ndata: {\"seq\":77,\"type\":\"session.status\",\"properties\":{\"sessionID\":\"ses-1\"}}\n\n")
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("observed")
	var got NativeEvent
	err = client.ObserveNativeEvents(context.Background(), directory, func(event NativeEvent) error { got = event; return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("observe err=%v", err)
	}
	if got.ID != "native-sse-42" || got.Seq != 77 {
		t.Fatalf("native provenance lost: %+v", got)
	}
}
