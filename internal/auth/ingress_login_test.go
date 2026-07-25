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
	return IngressLoginHandler(nil, signer, pairs, ds, 7*24*time.Hour, false)
}

// TestIngressApproveUserCode verifies that a request with a valid user_code
// approves the pending grant and renders an "Approved" page.
func TestIngressApproveUserCode(t *testing.T) {
	SetAllowedDomains([]string{"example.com"})
	t.Cleanup(func() { SetAllowedDomains([]string{"example.com", "example.org"}) })

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
	if !strings.Contains(rr.Body.String(), "Approved") {
		t.Fatalf("expected 'Approved' in body, got: %s", rr.Body.String())
	}

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

// TestIngressApproveUserCodeDomainRejected verifies that a domain-failing email
// returns 403 and does NOT approve the grant.
func TestIngressApproveUserCodeDomainRejected(t *testing.T) {
	SetAllowedDomains([]string{"example.com"})
	t.Cleanup(func() { SetAllowedDomains([]string{"example.com", "example.org"}) })

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
