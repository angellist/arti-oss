package apps_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/apps"
	"github.com/angellist/arti-oss/internal/auth"
)

// A 401 on the apps proxy used to leave no log line at all, so a stream of
// rejections was unattributable. These tests pin the two halves of the fix:
// an expired-but-authentic token is attributed to its email, while a FORGED
// token's email claim (attacker-controlled, signature never verified) must
// never be logged as an identity.
func newRejectFixture(t *testing.T, signer *auth.JWTSigner) (*chi.Mux, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	svc := apps.New(nil, signer, nil, nil, nil, slog.New(slog.NewTextHandler(&buf, nil)))
	r := chi.NewRouter()
	svc.MountProxy(r)
	return r, &buf
}

func postMCP(r http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHandleMCP_ExpiredTokenRejectionIsAttributed(t *testing.T) {
	signer := auth.NewJWTSigner([]byte("k"))
	router, buf := newRejectFixture(t, signer)

	tok, err := signer.Sign(auth.Claims{
		Email:  "stale-tab@example.com",
		Scopes: []string{auth.AppScopePrefix + "11111111-1111-1111-1111-111111111111"},
		TTL:    -time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := postMCP(router, tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	log := buf.String()
	if !strings.Contains(log, "stale-tab@example.com") {
		t.Errorf("rejection log missing the expired token's email: %q", log)
	}
	if !strings.Contains(log, "expired") {
		t.Errorf("rejection log missing the reason: %q", log)
	}
	if strings.Contains(log, tok) {
		t.Errorf("rejection log leaked the token itself")
	}
}

func TestHandleMCP_ForgedTokenIsNotAttributed(t *testing.T) {
	signer := auth.NewJWTSigner([]byte("real-key"))
	router, buf := newRejectFixture(t, signer)

	forger := auth.NewJWTSigner([]byte("attacker-key"))
	tok, err := forger.Sign(auth.Claims{Email: "victim@example.com", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if rec := postMCP(router, tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	log := buf.String()
	if !strings.Contains(log, "apps proxy: rejected") {
		t.Errorf("forged-token rejection produced no log line: %q", log)
	}
	// The email claim in a forged token is attacker-controlled text; logging
	// it as an identity would let an attacker frame a user.
	if strings.Contains(log, "victim@example.com") {
		t.Errorf("rejection log attributed an unverified email claim: %q", log)
	}
}

func TestHandleMCP_RejectionLogIsSampled(t *testing.T) {
	signer := auth.NewJWTSigner([]byte("k"))
	router, buf := newRejectFixture(t, signer)

	for range 40 {
		postMCP(router, "not-a-token")
	}
	lines := strings.Count(buf.String(), "\n")
	// 10 rejection lines + 1 "sampling" note; a brute force must not be able
	// to write one log line per attempt.
	if lines > 11 {
		t.Errorf("40 rejections produced %d log lines, want <= 11", lines)
	}
	if !strings.Contains(buf.String(), "sampling") {
		t.Errorf("suppression note missing: %q", buf.String())
	}
}
