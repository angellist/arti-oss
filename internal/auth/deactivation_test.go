package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

func TestIsAllowed_RefusesDeactivated(t *testing.T) {
	prev := auth.AllowedEmails()
	t.Cleanup(func() {
		auth.SetAllowedEmails(prev)
		auth.SetDeactivationCheck(nil)
	})
	auth.SetAllowedEmails([]string{"example.com"})

	var asked []string
	auth.SetDeactivationCheck(func(email string) bool {
		asked = append(asked, email)
		return strings.EqualFold(email, "gone@example.com")
	})

	if auth.IsAllowed("gone@example.com") {
		t.Error("a deactivated email on an allowed domain must be refused")
	}
	if auth.IsAllowed(" Gone@Example.com ") {
		t.Error("deactivation must not be bypassed by case or whitespace")
	}
	if !auth.IsAllowed("here@example.com") {
		t.Error("an active email on an allowed domain must still pass")
	}
	asked = nil
	if auth.IsAllowed("gone@elsewhere.com") {
		t.Error("an email outside the allowlist must be refused")
	}
	if len(asked) != 0 {
		t.Error("the deactivation check should not run for an email the allowlist already refused")
	}

	auth.SetDeactivationCheck(nil)
	if !auth.IsAllowed("gone@example.com") {
		t.Error("clearing the check must restore access")
	}
}

func TestRequireAuth_DeactivatedSessionRefused(t *testing.T) {
	prev := auth.AllowedEmails()
	t.Cleanup(func() {
		auth.SetAllowedEmails(prev)
		auth.SetDeactivationCheck(nil)
	})
	auth.SetAllowedEmails([]string{"example.com"})
	auth.SetDeactivationCheck(func(email string) bool { return email == "gone@example.com" })

	signer := auth.NewJWTSigner([]byte("test-secret"))
	h := auth.RequireAuth(auth.NewConfig(nil, signer, "", nil))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for email, want := range map[string]int{"gone@example.com": http.StatusUnauthorized, "here@example.com": http.StatusOK} {
		tok, err := signer.Sign(auth.Claims{Email: email, Scopes: []string{"user"}, TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: want %d, got %d", email, want, rec.Code)
		}
	}
}
