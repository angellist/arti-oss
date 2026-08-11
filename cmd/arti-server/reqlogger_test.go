package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The access log exists to observe real traffic; the kube-probe's /healthz
// polling was ~2/3 of arti's total log volume, drowning it. reqLogger must
// drop the probe path and nothing else.
func TestReqLoggerSkipsHealthz(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	h := reqLogger(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	get := func(path string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("GET %s: handler not reached, status %d", path, rec.Code)
		}
	}

	get("/healthz")
	if buf.Len() != 0 {
		t.Errorf("/healthz produced a log line: %q", buf.String())
	}

	get("/api/artifacts")
	if !strings.Contains(buf.String(), "p=/api/artifacts") {
		t.Errorf("real route missing from access log, got: %q", buf.String())
	}
}
