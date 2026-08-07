package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// seedGrant inserts a pending device grant into the fake store.
func seedGrant(ds *fakeDeviceStore, userCode string) {
	deviceCode := "dc-" + userCode
	ds.auths[deviceCode] = sqlc.DeviceAuth{
		DeviceCode: deviceCode,
		UserCode:   userCode,
		Duration:   "short",
		Status:     "pending",
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(10 * time.Minute), Valid: true},
	}
	ds.byUser[userCode] = deviceCode
}

func newIngressHandler(ds *fakeDeviceStore) http.HandlerFunc {
	signer := NewJWTSigner([]byte("testkey"))
	pairs := NewInMemPairStore()
	return IngressLoginHandler(nil, signer, pairs, ds, 7*24*time.Hour, false, nil)
}

func confirmationState(body string) string {
	const prefix = `name="state" value="`
	start := strings.Index(body, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.IndexByte(body[start:], '"')
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

// TestIngressApproveUserCode verifies that the initial GET only renders an
// explicit confirmation page, and the POST performs the approval.
func TestIngressApproveUserCode(t *testing.T) {
	SetAllowedEmails([]string{"example.com"})
	t.Cleanup(func() { SetAllowedEmails([]string{"example.com", "example.org"}) })

	ds := newFakeStore()
	seedGrant(ds, "ABCD-2345")

	handler := newIngressHandler(ds)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/login?user_code=ABCD-2345", nil)
	req.Header.Set(IngressEmailHeader, "dev@example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Authorize") {
		t.Fatalf("expected confirmation page, got: %s", rr.Body.String())
	}
	if grant := ds.auths[ds.byUser["ABCD-2345"]]; grant.Status != "pending" {
		t.Fatalf("GET must not approve the grant, got status %q", grant.Status)
	}

	state := confirmationState(rr.Body.String())
	if state == "" {
		t.Fatal("confirmation state missing")
	}
	confirm := httptest.NewRequest(http.MethodPost, "/auth/login/confirm", strings.NewReader("state="+state))
	confirm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirm.Header.Set("Cookie", rr.Header().Get("Set-Cookie"))
	confirm.Header.Set(IngressEmailHeader, "dev@example.com")
	confirmed := httptest.NewRecorder()
	ConfirmLoginHandler(NewJWTSigner([]byte("testkey")), NewInMemPairStore(), ds, false).ServeHTTP(confirmed, confirm)
	// Verify the grant was approved with the correct email.
	dc := ds.byUser["ABCD-2345"]
	grant := ds.auths[dc]
	if grant.Status != "approved" {
		t.Fatalf("expected status 'approved', got %q", grant.Status)
	}
	if grant.Email == nil || *grant.Email != "dev@example.com" {
		t.Fatalf("expected email 'dev@example.com', got %v", grant.Email)
	}
}

func TestIngressCrossSiteCodeGETRejected(t *testing.T) {
	ds := newFakeStore()
	seedGrant(ds, "ABCD-2345")
	handler := newIngressHandler(ds)
	req := httptest.NewRequest(http.MethodGet, "/auth/google/login?user_code=ABCD-2345", nil)
	req.Header.Set(IngressEmailHeader, "dev@example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if ds.auths[ds.byUser["ABCD-2345"]].Status != "pending" {
		t.Fatal("cross-site GET must not approve the grant")
	}
}

func TestConfirmLoginRejectsTamperedExpiredAndMismatchedState(t *testing.T) {
	ds := newFakeStore()
	signer := NewJWTSigner([]byte("testkey"))
	pairs := NewInMemPairStore()
	makeRequest := func(state, email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/auth/login/confirm", strings.NewReader("state="+state))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Cookie", "arti_login_confirm_identity="+signLoginConfirmIdentity(signer, loginConfirmIdentity{
			Email: email, Exp: time.Now().Add(5 * time.Minute).Unix(),
		}))
		rr := httptest.NewRecorder()
		ConfirmLoginHandler(signer, pairs, ds, false).ServeHTTP(rr, req)
		return rr
	}
	state := signLoginConfirmState(signer, loginConfirmState{
		Email: "dev@example.com", Code: "abc", Flow: "cli",
		Exp: time.Now().Add(5 * time.Minute).Unix(),
	})
	if rr := makeRequest(state+"tampered", "dev@example.com"); rr.Code != http.StatusForbidden {
		t.Fatalf("tampered state status = %d, want 403", rr.Code)
	}
	expired := signLoginConfirmState(signer, loginConfirmState{
		Email: "dev@example.com", Code: "abc", Flow: "cli",
		Exp: time.Now().Add(-time.Minute).Unix(),
	})
	if rr := makeRequest(expired, "dev@example.com"); rr.Code != http.StatusForbidden {
		t.Fatalf("expired state status = %d, want 403", rr.Code)
	}
	if rr := makeRequest(state, "other@example.com"); rr.Code != http.StatusForbidden {
		t.Fatalf("mismatched identity status = %d, want 403", rr.Code)
	}
}

// TestIngressApproveUserCodeDomainRejected verifies that a domain-failing email
// returns 403 and does NOT approve the grant.
func TestIngressApproveUserCodeDomainRejected(t *testing.T) {
	SetAllowedEmails([]string{"example.com"})
	t.Cleanup(func() { SetAllowedEmails([]string{"example.com", "example.org"}) })

	ds := newFakeStore()
	seedGrant(ds, "ABCD-2345")

	handler := newIngressHandler(ds)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/login?user_code=ABCD-2345", nil)
	req.Header.Set(IngressEmailHeader, "x@evil.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d; body: %s", rr.Code, rr.Body.String())
	}

	// Grant must remain pending.
	dc := ds.byUser["ABCD-2345"]
	grant := ds.auths[dc]
	if grant.Status != "pending" {
		t.Fatalf("expected status 'pending' after domain rejection, got %q", grant.Status)
	}
}
