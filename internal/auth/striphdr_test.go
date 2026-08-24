package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimid "github.com/go-chi/chi/v5/middleware"

	"github.com/angellist/arti-oss/internal/auth"
)

// chi's RealIP prefers True-Client-IP over every other header, and this
// deployment's edge does not set or overwrite it — so without stripping, a
// caller picks the address used for rate-limit bucketing and audit rows.
func TestStripSpoofableIPHeaders_TrueClientIPCannotWin(t *testing.T) {
	var got string
	h := auth.StripSpoofableIPHeaders(chimid.RealIP(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { got = r.RemoteAddr })))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Set("True-Client-IP", "198.18.0.99")   // forged by the caller
	req.Header.Set("X-Forwarded-For", "203.0.113.10") // written by the edge
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got == "198.18.0.99" {
		t.Fatal("forged True-Client-IP won; it must be stripped before RealIP")
	}
	if got != "203.0.113.10" {
		t.Fatalf("RemoteAddr = %q, want the edge-supplied X-Forwarded-For value", got)
	}
}

// The edge-managed header must still reach RealIP, or every client collapses
// onto the load balancer's address and per-client rate limiting stops working.
func TestStripSpoofableIPHeaders_LeavesForwardedForAlone(t *testing.T) {
	var got string
	h := auth.StripSpoofableIPHeaders(chimid.RealIP(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { got = r.RemoteAddr })))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "203.0.113.10" {
		t.Fatalf("RemoteAddr = %q, want 203.0.113.10", got)
	}
}
