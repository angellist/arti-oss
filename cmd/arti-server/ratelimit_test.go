package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ipRateLimiter is the per-client-IP throttle guarding the two public,
// unauthenticated, DB-writing setup endpoints. The behavior that matters:
// the configured count succeeds, the next request from the SAME IP gets 429,
// a DIFFERENT IP is unaffected, and 0 disables the limiter entirely (the
// "0 = no cap" convention used elsewhere in this config).
func serve(h http.Handler, ip string) int {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.RemoteAddr = ip + ":12345"
	h.ServeHTTP(rr, req)
	return rr.Code
}

func TestIPRateLimiter_ThrottlesPerIP(t *testing.T) {
	const limit = 3
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := ipRateLimiter(limit)(ok)

	// First `limit` requests from one IP succeed.
	for i := 0; i < limit; i++ {
		if code := serve(h, "1.2.3.4"); code != http.StatusOK {
			t.Fatalf("request %d from 1.2.3.4: got %d, want 200", i+1, code)
		}
	}
	// The next from the same IP is throttled.
	if code := serve(h, "1.2.3.4"); code != http.StatusTooManyRequests {
		t.Fatalf("over-limit from 1.2.3.4: got %d, want 429", code)
	}
	// A different IP has its own bucket and is unaffected.
	if code := serve(h, "5.6.7.8"); code != http.StatusOK {
		t.Fatalf("different IP 5.6.7.8: got %d, want 200", code)
	}
}

func TestIPRateLimiter_ZeroDisables(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := ipRateLimiter(0)(ok)
	// Far more requests than any positive limit; none should be throttled.
	for i := 0; i < 50; i++ {
		if code := serve(h, "1.2.3.4"); code != http.StatusOK {
			t.Fatalf("rpm=0 request %d: got %d, want 200 (0 must disable the limiter)", i+1, code)
		}
	}
}
