package apps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/httperr"
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
	s.writeUpstreamErr(rr, req, "notion", "search", errors.New("dial tcp: i/o timeout"))

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
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
			s.writeUpstreamErr(rr, req, "notion", "search", tc.err)

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
// slowest upstreams entirely.
func TestUpstreamDeadlineIsStillLogged(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{logger: slog.New(slog.NewTextHandler(&buf, nil))}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", nil)
	s.writeUpstreamErr(rr, req, "notion", "search", fmt.Errorf("upstream: %w", context.DeadlineExceeded))

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
	if buf.Len() == 0 {
		t.Error("server-side deadline was not logged")
	}
}
