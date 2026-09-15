package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A tools/call body over MaxResponseBytes must fail as ErrResponseTooLarge,
// not as a JSON syntax error on whatever the first MaxResponseBytes happened to contain.
func TestCallToolRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(readBody(r), `"initialize"`) {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"%s"}]}}`,
			strings.Repeat("x", MaxResponseBytes))
	}))
	defer srv.Close()

	_, err := New().CallTool(context.Background(), srv.URL, "", "query", nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

// A body exactly at the cap is still read and parsed.
func TestCallToolAcceptsResponseAtCap(t *testing.T) {
	prefix := `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"`
	suffix := `"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(readBody(r), `"initialize"`) {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			return
		}
		fmt.Fprint(w, prefix+strings.Repeat("x", MaxResponseBytes-len(prefix)-len(suffix))+suffix)
	}))
	defer srv.Close()

	res, err := New().CallTool(context.Background(), srv.URL, "", "query", nil)
	if err != nil {
		t.Fatalf("err = %v, want a parsed result", err)
	}
	if len(res) == 0 {
		t.Fatal("empty result")
	}
}

// The caller's ctx deadline is the only timeout, and it surfaces as
// context.DeadlineExceeded so the apps proxy can name it.
func TestCallToolTimeoutIsDeadlineExceeded(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block) // release the handler before srv.Close waits on it

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := New().CallTool(ctx, srv.URL, "", "query", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func readBody(r *http.Request) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

// A tool-level failure comes back as the JSON-RPC error object, typed, so the
// proxy can tell "the tool refused" from "the transport failed".
func TestCallToolReturnsTypedRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(readBody(r), `"initialize"`) {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"Unknown tool: nope"}}`)
	}))
	defer srv.Close()

	_, err := New().CallTool(context.Background(), srv.URL, "", "nope", nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 || !strings.Contains(rpcErr.Message, "Unknown tool") {
		t.Fatalf("err = %v, want *RPCError -32602 Unknown tool", err)
	}
}
