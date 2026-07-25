package slacknotify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// testClient points an httpSlackClient at a fake server, with no rate limiter
// and a tiny backoff so retry tests run fast.
func testClient(baseURL string) *httpSlackClient {
	return &httpSlackClient{
		token:       "xoxb-test",
		baseURL:     baseURL,
		httpc:       &http.Client{Timeout: 2 * time.Second},
		limiter:     nil,
		maxAttempts: defaultMaxAttempts,
		baseBackoff: time.Millisecond,
	}
}

func TestClient_RetriesThen429Succeeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// First call: rate-limited, asks us to wait (0s so the test is fast).
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	if err := c.PostDM(context.Background(), "U1", "hi"); err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 calls (429 then 200), got %d", got)
	}
}

func TestClient_GivesUpAfterMaxAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	if err := c.PostDM(context.Background(), "U1", "hi"); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := atomic.LoadInt32(&calls); got != int32(defaultMaxAttempts) {
		t.Fatalf("expected %d attempts, got %d", defaultMaxAttempts, got)
	}
}

func TestClient_RetriesTransient5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"user":{"id":"U42"}}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	id, err := c.LookupUserByEmail(context.Background(), "a@x.com")
	if err != nil || id != "U42" {
		t.Fatalf("expected U42 after a 5xx retry, got id=%q err=%v", id, err)
	}
}

func TestClient_PostDMNotRetriedOnAmbiguousFailure(t *testing.T) {
	// chat.postMessage is non-idempotent: a 5xx might mean the DM was already
	// delivered, so retrying could duplicate it. It must NOT retry on 5xx (only
	// on a definitive 429 — covered by the retry-success test above).
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	if err := c.PostDM(context.Background(), "U1", "hi"); err == nil {
		t.Fatal("expected error on 5xx")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("non-idempotent PostDM must not retry on 5xx; got %d calls", got)
	}
}

func TestClient_NoRetryOnAppLevelError(t *testing.T) {
	// users_not_found is an HTTP 200 with ok:false — a definitive answer, not a
	// transient failure. It must NOT be retried, and maps to errNoSlackUser.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"ok":false,"error":"users_not_found"}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	_, err := c.LookupUserByEmail(context.Background(), "ghost@x.com")
	if !errors.Is(err, errNoSlackUser) {
		t.Fatalf("expected errNoSlackUser, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("app-level errors must not retry; got %d calls", got)
	}
}

func TestClient_ContextCancelStopsRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30") // long enough that ctx wins
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.PostDM(ctx, "U1", "hi"); err == nil {
		t.Fatal("expected error when context cancels during backoff")
	}
}

func TestTokenBucket_BurstThenThrottle(t *testing.T) {
	tb := newTokenBucket(1, 2) // 2 immediate; refill is 1/s, so none during this test
	ctx := context.Background()
	// First two are immediate (burst).
	for i := 0; i < 2; i++ {
		if err := tb.wait(ctx); err != nil {
			t.Fatalf("burst token %d should be immediate: %v", i, err)
		}
	}
	// A cancelled context must make wait return promptly rather than block.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tb.wait(cctx); err == nil {
		t.Fatal("expected ctx error when no tokens and ctx is cancelled")
	}
}
