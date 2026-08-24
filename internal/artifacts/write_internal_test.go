package artifacts

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
	"time"

	"github.com/angellist/arti-oss/internal/httperr"
)

// captureLogs points the default slog logger at a buffer for one test, so the
// assertions below can read what writeInternal actually recorded. The default
// logger is what cmd_serve wires to the JSON stderr handler in a real server.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A genuine failure must leave a server-side record of WHY. Today the reason
// travels only in the response body, and nothing stores response bodies:
// /api/artifacts/aggregates failed 13-21 times a day in prod for a week with
// no way to tell what had failed.
func TestWriteInternalLogsGenuineFailures(t *testing.T) {
	buf := captureLogs(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil)
	writeInternal(rr, req, errors.New("pgstore: aggregates label: relation does not exist"))

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	got := buf.String()
	if !strings.Contains(got, "relation does not exist") {
		t.Errorf("failure reason not logged, got: %q", got)
	}
	if !strings.Contains(got, "level=ERROR") {
		t.Errorf("genuine failure not logged at ERROR, got: %q", got)
	}
}

// The caller hanging up mid-request is not a server fault. Answering 500 is
// what fabricated arti's daily 5xx count; logging it at ERROR would move the
// same noise into the error stream instead, so it must do neither.
func TestWriteInternalTreatsClientCancellationAs499(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare context.Canceled", context.Canceled},
		{"wrapped by pgstore", fmt.Errorf("pgstore: aggregates scope: %w", context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil)
			writeInternal(rr, req, tc.err)

			if rr.Code != httperr.StatusClientClosedRequest {
				t.Errorf("status = %d, want %d", rr.Code, httperr.StatusClientClosedRequest)
			}
			if buf.Len() != 0 {
				t.Errorf("client cancellation logged as an error: %q", buf.String())
			}
		})
	}
}

// Cancellation does not always reach the handler as a context error — a query
// that had already finished returns its own failure while the connection is
// gone. The live request context is the second, independent signal.
func TestWriteInternalTreatsDeadRequestContextAs499(t *testing.T) {
	buf := captureLogs(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil).WithContext(ctx)
	writeInternal(rr, req, errors.New("conn closed"))

	if rr.Code != httperr.StatusClientClosedRequest {
		t.Errorf("status = %d, want %d", rr.Code, httperr.StatusClientClosedRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("abandoned request logged as an error: %q", buf.String())
	}
}

// The internal error text must not become the response body of a 499 either —
// there is no client left to read it, and it names internal query structure.
func TestWriteInternal499HasNoBody(t *testing.T) {
	captureLogs(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil)
	writeInternal(rr, req, context.Canceled)

	if body := rr.Body.String(); body != "" {
		t.Errorf("499 carried a body: %q", body)
	}
}

// Inside a handler, r.Context() is chi's 60s Timeout context, not the
// connection's. An earlier draft checked `r.Context().Err() != nil`, which made
// every request that exhausted that deadline look like the client's fault and
// dropped it from the error stream entirely. A server-side deadline is a real
// fault and must be answered 500 and logged.
func TestWriteInternalLogsServerSideDeadline(t *testing.T) {
	buf := captureLogs(t)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil).WithContext(ctx)
	writeInternal(rr, req, fmt.Errorf("pgstore: aggregates scope: %w", context.DeadlineExceeded))

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 — a timeout is not the client leaving", rr.Code)
	}
	if buf.Len() == 0 {
		t.Error("server-side deadline was not logged")
	}
}
