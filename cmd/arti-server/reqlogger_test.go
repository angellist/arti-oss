package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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

// A browser that navigates away mid-request — or React unmounting a component
// whose fetch is still in flight — cancels the request context. The handler's
// DB query then fails with context.Canceled and the handler answers 500, so
// the access log recorded a server fault for something the server did nothing
// wrong in. On prod this fabricated 13-21 "500"s a day on
// /api/artifacts/aggregates alone, which is what made `s>=500` unusable as an
// error signal. Record those as 499 (nginx's "client closed request") instead.
func TestReqLoggerRecordsClientDisconnectAs499(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	h := reqLogger(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client is already gone by the time the handler returns
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := buf.String(); !strings.Contains(got, "s=499") {
		t.Errorf("client-cancelled 500 not recorded as 499, got: %q", got)
	}
}

// The rewrite must be narrow: a genuine 500 on a live connection is exactly
// what `s>=500` is for, and must survive.
func TestReqLoggerKeeps500WhenClientIsStillConnected(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	h := reqLogger(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/artifacts", nil))

	if got := buf.String(); !strings.Contains(got, "s=500") {
		t.Errorf("genuine 500 was not recorded as 500, got: %q", got)
	}
}

// A 2xx that the client abandoned mid-download is still a request the server
// served correctly. Only a would-be server fault is reclassified, so success
// counts and latency percentiles are unaffected.
func TestReqLoggerKeepsSuccessStatusOnClientDisconnect(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	h := reqLogger(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := buf.String(); !strings.Contains(got, "s=200") {
		t.Errorf("abandoned 200 should stay 200, got: %q", got)
	}
}

// The rewrite reads r.Context() AFTER the handler returns, which is only safe
// because net/http cancels a completed request's context after the whole
// middleware chain unwinds — not before. If that were the other way round,
// every 5xx would silently become a 499 and arti would report no server errors
// at all. httptest.NewRecorder cannot catch that: it never runs the real
// http.Server request lifecycle. This does, over a real TCP connection.
func TestReqLoggerKeeps500OverRealConnection(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&syncWriter{mu: &mu, w: &buf}, nil))

	srv := httptest.NewServer(reqLogger(l)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/artifacts/aggregates")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	mu.Lock()
	got := buf.String()
	mu.Unlock()
	if !strings.Contains(got, "s=500") {
		t.Fatalf("a real, fully-read 500 was not recorded as 500 — the 499 rewrite is eating server errors. got: %q", got)
	}
}

// A client that closes the connection before the response is written is the
// case the 499 rewrite exists for. Verified end to end rather than with a
// pre-cancelled context, so it pins net/http's real disconnect behaviour.
func TestReqLoggerRecords499OverRealConnection(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&syncWriter{mu: &mu, w: &buf}, nil))

	gone := make(chan struct{})
	srv := httptest.NewServer(reqLogger(l)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			<-gone               // wait until the client has hung up
			<-r.Context().Done() // ...and until the server has noticed
			http.Error(w, "boom", http.StatusInternalServerError)
		})))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "GET /api/artifacts/aggregates HTTP/1.1\r\nHost: x\r\n\r\n")
	// Give the server a moment to enter the handler, then vanish.
	time.Sleep(50 * time.Millisecond)
	conn.Close()
	close(gone)

	// Wait on the log line itself: reqLogger writes it after the handler
	// returns, so any signal raised inside the handler races ahead of it.
	var got string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got = buf.String()
		mu.Unlock()
		if got != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !strings.Contains(got, "s=499") {
		t.Fatalf("disconnected client's 500 not recorded as 499, got: %q", got)
	}
}

// slog handlers are not safe for concurrent use with a plain bytes.Buffer when
// the server goroutine writes while the test goroutine reads.
type syncWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
