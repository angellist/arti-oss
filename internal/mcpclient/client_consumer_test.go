package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The calling app is sent both ways on tools/call: params._meta.consumer and
// the X-Snowflake-Consumer header, so attribution survives a proxy that drops
// either one. Neither appears on initialize.
func TestCallToolSendsConsumerInMetaAndHeader(t *testing.T) {
	var initHeader, callHeader string
	var callParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := readBody(r)
		if strings.Contains(body, `"initialize"`) {
			initHeader = r.Header.Get(ConsumerHeader)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			return
		}
		callHeader = r.Header.Get(ConsumerHeader)
		var req struct {
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		callParams = req.Params
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`)
	}))
	defer srv.Close()

	if _, err := New().CallTool(context.Background(), srv.URL, "", "query", nil, "arti:devin-data-impact"); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if initHeader != "" {
		t.Fatalf("initialize carried %s=%q, want none", ConsumerHeader, initHeader)
	}
	if callHeader != "arti:devin-data-impact" {
		t.Fatalf("tools/call %s = %q, want arti:devin-data-impact", ConsumerHeader, callHeader)
	}
	meta, _ := callParams["_meta"].(map[string]any)
	if meta["consumer"] != "arti:devin-data-impact" {
		t.Fatalf("params._meta = %v, want consumer arti:devin-data-impact", callParams["_meta"])
	}
}

// A slug that is not a valid header value still rides in params._meta; the
// header is skipped rather than failing the user's call.
func TestCallToolSkipsHeaderForInvalidConsumer(t *testing.T) {
	var callHeader string
	var callParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := readBody(r)
		if strings.Contains(body, `"initialize"`) {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			return
		}
		callHeader = r.Header.Get(ConsumerHeader)
		var req struct {
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		callParams = req.Params
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`)
	}))
	defer srv.Close()

	bad := "arti:a\nb"
	if _, err := New().CallTool(context.Background(), srv.URL, "", "query", nil, bad); err != nil {
		t.Fatalf("CallTool with control char in consumer: %v", err)
	}
	if callHeader != "" {
		t.Fatalf("header = %q, want none for an invalid value", callHeader)
	}
	meta, _ := callParams["_meta"].(map[string]any)
	if meta["consumer"] != bad {
		t.Fatalf("params._meta = %v, want consumer %q", callParams["_meta"], bad)
	}
}
