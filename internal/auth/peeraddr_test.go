package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimid "github.com/go-chi/chi/v5/middleware"

	"github.com/angellist/arti-oss/internal/auth"
)

// chi's RealIP overwrites r.RemoteAddr from client-supplied headers, so the
// socket address survives only if it is captured upstream of it. This is the
// whole reason CapturePeerAddr exists: share-link opens record both, and the
// disagreement between them is the signal that a header was forged.
func TestCapturePeerAddr_SurvivesRealIP(t *testing.T) {
	var got string
	h := auth.CapturePeerAddr(chimid.RealIP(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			got = auth.PeerAddrFromContext(r.Context())
			// Sanity: RealIP really did clobber RemoteAddr, so the test is
			// not passing by accident on a build where it did nothing.
			if r.RemoteAddr != "203.0.113.9" {
				t.Errorf("RemoteAddr = %q; expected RealIP to have rewritten it", r.RemoteAddr)
			}
		})))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.7:44321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "198.51.100.7" {
		t.Fatalf("peer addr = %q, want the socket address 198.51.100.7", got)
	}
}

// Without the middleware the accessor is empty rather than wrong.
func TestPeerAddrFromContext_AbsentIsEmpty(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if got := auth.PeerAddrFromContext(req.Context()); got != "" {
		t.Fatalf("peer addr = %q, want empty", got)
	}
}
