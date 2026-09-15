package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/httperr"
	"github.com/angellist/arti-oss/internal/mcpclient"
)

// An upstream tool call that fails answers 502 with the reason in the response
// BODY, and nothing stores response bodies. Prod ran 82 of these in one day on
// /api/apps/mcp with no server-side record of which server or tool had failed,
// so the only thing recoverable afterwards was the duration: 5s for an
// upstream timeout, exactly 60s where chi's Timeout truncated the request.
// Log enough to name the culprit next time.
func TestUpstreamFailedLogsServerAndTool(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{logger: slog.New(slog.NewTextHandler(&buf, nil))}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
	s.writeUpstreamErr(rr, req, "notion", "search", time.Minute, errors.New("dial tcp: i/o timeout"))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	got := buf.String()
	for _, want := range []string{"notion", "search", "i/o timeout"} {
		if !strings.Contains(got, want) {
			t.Errorf("log line missing %q, got: %q", want, got)
		}
	}
}

// A viewer closing the app mid-call cancels the request context. That is not
// an upstream fault and must not be answered 502 or logged as one — the same
// misclassification that fabricated arti's daily 5xx count elsewhere.
func TestUpstreamCancellationIsNotLoggedAsAFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", context.Canceled},
		{"wrapped", fmt.Errorf("upstream: %w", context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := &Service{logger: slog.New(slog.NewTextHandler(&buf, nil))}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
			s.writeUpstreamErr(rr, req, "notion", "search", time.Minute, tc.err)

			if rr.Code != httperr.StatusClientClosedRequest {
				t.Errorf("status = %d, want %d", rr.Code, httperr.StatusClientClosedRequest)
			}
			if buf.Len() != 0 {
				t.Errorf("cancelled call logged as an upstream failure: %q", buf.String())
			}
		})
	}
}

// A server-side deadline is a genuine fault and must survive the filter above
// — chi's 60s Timeout produced 25 of the 82, and losing those would hide the
// slowest upstreams entirely. It answers 504 with a stable error code: the
// upstream may still finish the work (a Snowflake statement keeps running and
// stays retrievable by handle), and an app that can tell a timeout from any
// other failure can resume instead of re-running it.
func TestUpstreamDeadlineIsStillLogged(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{logger: slog.New(slog.NewTextHandler(&buf, nil))}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
	s.writeUpstreamErr(rr, req, "snowflake-v2", "query", 45*time.Second, fmt.Errorf("upstream: %w", context.DeadlineExceeded))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	if buf.Len() == 0 {
		t.Error("server-side deadline was not logged")
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["error"] != "upstream_timeout" || body["server"] != "snowflake-v2" || body["tool"] != "query" {
		t.Errorf("body = %v, want error=upstream_timeout for snowflake-v2/query", body)
	}
	if !strings.Contains(body["detail"], "re-running") {
		t.Errorf("detail should steer the app away from re-running the call, got %q", body["detail"])
	}
	if !strings.Contains(body["detail"], "45s") {
		t.Errorf("detail should name the timeout this call ran with, got %q", body["detail"])
	}
}

// An upstream page over the proxy's read cap used to surface as a JSON parse
// error on a truncated body. It is the caller's page size that is wrong, so the
// answer names the cap and asks for smaller pages.
func TestUpstreamResponseTooLargeNamesTheCap(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{logger: slog.New(slog.NewTextHandler(&buf, nil))}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
	s.writeUpstreamErr(rr, req, "snowflake-v2", "fetch_results", time.Minute, fmt.Errorf("%w (%d bytes)", mcpclient.ErrResponseTooLarge, mcpclient.MaxResponseBytes))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["error"] != "upstream_response_too_large" {
		t.Errorf("error = %q, want upstream_response_too_large", body["error"])
	}
	if !strings.Contains(body["detail"], strconv.Itoa(mcpclient.MaxResponseBytes)) {
		t.Errorf("detail should name the byte cap, got %q", body["detail"])
	}
	if buf.Len() == 0 {
		t.Error("oversized response was not logged")
	}
}

// A tool that answers with a JSON-RPC error object (unknown tool, bad
// arguments) is the app's problem, not the transport's: it gets its own code
// and the upstream message, so the app does not retry a call that can never
// succeed.
func TestUpstreamRPCErrorIsAToolError(t *testing.T) {
	s := &Service{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
	s.writeUpstreamErr(rr, req, "notion", "serch", time.Minute, &mcpclient.RPCError{Code: -32602, Message: "Unknown tool: notion_serch"})

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["error"] != "tool_error" || body["server"] != "notion" || body["tool"] != "serch" {
		t.Errorf("body = %v, want error=tool_error for notion/serch", body)
	}
	if !strings.Contains(body["detail"], "Unknown tool") || !strings.Contains(body["detail"], "-32602") {
		t.Errorf("detail should carry the upstream code and message, got %q", body["detail"])
	}
}

// Every other upstream failure still gets a code plus server/tool, so no app
// has to parse the message to learn which call failed.
func TestUpstreamOtherFailureIsStructured(t *testing.T) {
	s := &Service{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
	s.writeUpstreamErr(rr, req, "notion", "search", time.Minute, errors.New("mcpclient: upstream 500: boom"))

	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if rr.Code != http.StatusServiceUnavailable || body["error"] != "upstream_error" || body["server"] != "notion" || body["tool"] != "search" {
		t.Errorf("status=%d body=%v, want 503 error=upstream_error for notion/search", rr.Code, body)
	}
	if !strings.Contains(body["detail"], "upstream 500: boom") {
		t.Errorf("detail lost the upstream message: %q", body["detail"])
	}
}

// callTimeout: the app's ask is honored up to the cap; no ask means the default.
func TestCallTimeoutClamps(t *testing.T) {
	s := &Service{maxTimeout: 90 * time.Second}
	for _, tc := range []struct {
		ms   int
		want time.Duration
	}{
		{0, defaultCallTimeout},
		{-5, defaultCallTimeout},
		{15000, 15 * time.Second},
		{600000, 90 * time.Second},
	} {
		if got := s.callTimeout(tc.ms); got != tc.want {
			t.Errorf("callTimeout(%d) = %s, want %s", tc.ms, got, tc.want)
		}
	}
}

// Cloudflare fronts the public ingress and swaps an origin 502/504 for its own
// HTML page with no CORS headers, so a sandboxed APP page reads "Failed to
// fetch" instead of the JSON body. No upstream failure may use either status.
func TestUpstreamFailuresNeverUse502Or504(t *testing.T) {
	s := &Service{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	for name, err := range map[string]error{
		"timeout":   context.DeadlineExceeded,
		"too_large": mcpclient.ErrResponseTooLarge,
		"rpc":       &mcpclient.RPCError{Code: -32602, Message: "nope"},
		"other":     errors.New("mcpclient: upstream 500: boom"),
		"internal":  errors.New("pg: connection reset"),
	} {
		for _, write := range []func(http.ResponseWriter, *http.Request){
			func(w http.ResponseWriter, r *http.Request) {
				s.writeUpstreamErr(w, r, "srv", "tool", time.Minute, err)
			},
			func(w http.ResponseWriter, r *http.Request) {
				s.writeInProcessErr(w, r, "arti", "tool", time.Minute, err)
			},
		} {
			rr := httptest.NewRecorder()
			write(rr, httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil))
			if rr.Code == http.StatusBadGateway || rr.Code == http.StatusGatewayTimeout {
				t.Errorf("%s: status %d would be rewritten by Cloudflare", name, rr.Code)
			}
		}
	}
}

// An in-process arti call that hits the call's deadline is a timeout like any
// upstream's, with the same code, so an app handles both the same way.
func TestInProcessDeadlineIsUpstreamTimeout(t *testing.T) {
	s := &Service{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), maxTimeout: time.Minute}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
	s.writeInProcessErr(rr, req, "arti", "search_artifacts", 5*time.Millisecond, fmt.Errorf("query: %w", context.DeadlineExceeded))

	var body map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if rr.Code != http.StatusServiceUnavailable || body["error"] != "upstream_timeout" || body["tool"] != "search_artifacts" {
		t.Fatalf("status=%d body=%v, want 503 upstream_timeout for arti/search_artifacts", rr.Code, body)
	}
}
