package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeCapturer struct {
	email  string
	groups []string
	err    error
	called bool
}

func (f *fakeCapturer) UpsertIdPGroups(_ context.Context, email string, groups []string) error {
	f.called = true
	f.email = email
	f.groups = groups
	return f.err
}

// The ingress (proxy-mode) login captures the caller's IdP groups from the
// X-Auth-Request-Groups header at the shared finish() point, normalized the
// same way the required-groups gate sees them.
func TestIngressCapturesIdPGroups(t *testing.T) {
	SetAllowedDomains([]string{"example.com"})
	t.Cleanup(func() { SetAllowedDomains([]string{"example.com", "example.org"}) })

	cap := &fakeCapturer{}
	signer := NewJWTSigner([]byte("testkey"))
	handler := IngressLoginHandler(nil, signer, NewInMemPairStore(), newFakeStore(),
		7*24*time.Hour, false, cap)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/login", nil)
	req.Header.Set(IngressEmailHeader, "dev@example.com")
	req.Header.Set(IngressGroupsHeader, "engineering@example.com, platform")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !cap.called {
		t.Fatal("capturer was not called")
	}
	if cap.email != "dev@example.com" {
		t.Errorf("captured email = %q, want dev@example.com", cap.email)
	}
	// splitGroups strips @domain and trims.
	if len(cap.groups) != 2 || cap.groups[0] != "engineering" || cap.groups[1] != "platform" {
		t.Errorf("captured groups = %v, want [engineering platform]", cap.groups)
	}
}

// A capture failure must never break the login response (best-effort).
func TestIngressCaptureErrorDoesNotBlockLogin(t *testing.T) {
	SetAllowedDomains([]string{"example.com"})
	t.Cleanup(func() { SetAllowedDomains([]string{"example.com", "example.org"}) })

	cap := &fakeCapturer{err: errors.New("db down")}
	signer := NewJWTSigner([]byte("testkey"))
	handler := IngressLoginHandler(nil, signer, NewInMemPairStore(), newFakeStore(),
		7*24*time.Hour, false, cap)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/login", nil)
	req.Header.Set(IngressEmailHeader, "dev@example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Browser flow with no cli_code/user_code → redirect (302) after setting
	// the session cookie; the capture error must not turn this into a 5xx.
	if rr.Code >= 500 {
		t.Fatalf("capture error leaked into response: status %d, body %s", rr.Code, rr.Body.String())
	}
}
