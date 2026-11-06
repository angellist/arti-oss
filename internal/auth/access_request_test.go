package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeRequests struct {
	status map[string]string
	calls  []string // "email|name" per RequestAccess
}

func (f *fakeRequests) AccessRequestStatus(_ context.Context, email string) (string, error) {
	return f.status[email], nil
}

func (f *fakeRequests) RequestAccess(_ context.Context, email, name string) error {
	f.calls = append(f.calls, email+"|"+name)
	f.status[email] = "PENDING"
	return nil
}

// gateFixture: example.com is auto-admitted, partner.com may request, and
// accounts/deactivated are the users-table answers. An account is admin-added
// unless prefixed "login:".
func gateFixture(t *testing.T, accounts, deactivated []string) {
	t.Helper()
	in := func(set []string) func(string) bool {
		return func(e string) bool {
			for _, s := range set {
				if strings.EqualFold(s, strings.TrimSpace(e)) {
					return true
				}
			}
			return false
		}
	}
	SetAllowedEmails([]string{"example.com"})
	SetAcceptedDomains([]string{"partner.com"})
	SetAccountCheck(func(e string) (bool, bool) {
		for _, a := range accounts {
			email, isLogin := strings.CutPrefix(a, "login:")
			if strings.EqualFold(email, strings.TrimSpace(e)) {
				return true, !isLogin
			}
		}
		return false, false
	})
	SetDeactivationCheck(in(deactivated))
	t.Cleanup(func() {
		SetAllowedEmails([]string{"example.com", "example.org"})
		SetAcceptedDomains(nil)
		SetAccountCheck(nil)
		SetDeactivationCheck(nil)
	})
}

func TestIsAllowed_AccountAdmitsOutsideAllowlist(t *testing.T) {
	gateFixture(t, []string{"added@elsewhere.com", "gone@partner.com"}, []string{"gone@partner.com"})

	cases := []struct {
		email string
		want  bool
	}{
		{"added@elsewhere.com", true},   // hand-added, domain not even accepted
		{"nobody@elsewhere.com", false}, // no account, not allowlisted
		{"new@partner.com", false},      // accepted domain is not admission
		{"gone@partner.com", false},     // deactivation beats the account
		{"svc@arti.invalid", false},
		{"dev@example.com", true},
	}
	for _, c := range cases {
		if got := IsAllowed(c.email); got != c.want {
			t.Errorf("IsAllowed(%q) = %v, want %v", c.email, got, c.want)
		}
	}
}

func TestCanRequestAccess(t *testing.T) {
	gateFixture(t, []string{"member@partner.com"}, []string{"gone@partner.com"})

	cases := []struct {
		email string
		want  bool
	}{
		{"new@partner.com", true},
		{"New@Partner.com", true},
		{"member@partner.com", false}, // already in
		{"gone@partner.com", false},   // deactivated people may not re-ask
		{"dev@example.com", false},    // auto-admitted
		{"x@elsewhere.com", false},    // domain not accepted
		{"svc@partner.invalid", false},
		{"no-at-sign", false},
	}
	for _, c := range cases {
		if got := CanRequestAccess(c.email); got != c.want {
			t.Errorf("CanRequestAccess(%q) = %v, want %v", c.email, got, c.want)
		}
	}
}

func TestPassesGroupGate_AccountSkipsRequiredGroups(t *testing.T) {
	gateFixture(t, []string{"added@partner.com", "dev@example.com", "login:old@partner.com"}, nil)
	required := []string{"engineers"}

	if !passesGroupGate("added@partner.com", nil, required) {
		t.Error("a hand-added account must not need a required group")
	}
	// A login row is not deliberate: leaving a required group still locks out
	// someone whose domain has left the allowlist.
	if passesGroupGate("old@partner.com", nil, required) {
		t.Error("a login-only account outside the allowlist must still need a required group")
	}
	if !IsAllowed("old@partner.com") {
		t.Error("a login-only account is still admitted past the allowlist")
	}
	// An allowlisted person with a login row is still group-gated: the skip is
	// for people the allowlist would not admit.
	if passesGroupGate("dev@example.com", nil, required) {
		t.Error("an allowlisted user without a required group must be refused")
	}
	if !passesGroupGate("dev@example.com", []string{"Engineers"}, required) {
		t.Error("an allowlisted user in a required group must pass")
	}
}

func requestHandler(reqs *fakeRequests) http.HandlerFunc {
	return IngressLoginHandler([]string{"engineers"}, NewJWTSigner([]byte("testkey")), NewInMemPairStore(),
		newFakeStore(), 7*24*time.Hour, false, nil, reqs)
}

func loginReq(method, email string, form url.Values) *http.Request {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, "/auth/login?return_to=/x", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, "/auth/login?return_to=/x", nil)
	}
	req.Header.Set(IngressEmailHeader, email)
	return req
}

func TestIngressLogin_RequestAccessFlow(t *testing.T) {
	gateFixture(t, nil, nil)
	reqs := &fakeRequests{status: map[string]string{}}
	h := requestHandler(reqs)

	w := httptest.NewRecorder()
	h(w, loginReq(http.MethodGet, "new@partner.com", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="name"`) {
		t.Fatalf("GET: want the request form, got %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("a person without an account must not get a session cookie")
	}

	// The body names someone else; only the proxy identity may be recorded.
	w = httptest.NewRecorder()
	h(w, loginReq(http.MethodPost, "new@partner.com", url.Values{"name": {"Ada"}, "email": {"ceo@example.com"}}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Request sent") {
		t.Fatalf("POST: got %d %s", w.Code, w.Body.String())
	}
	if len(reqs.calls) != 1 || reqs.calls[0] != "new@partner.com|Ada" {
		t.Fatalf("recorded %v, want exactly new@partner.com|Ada", reqs.calls)
	}

	// A resubmit while pending records nothing new.
	w = httptest.NewRecorder()
	h(w, loginReq(http.MethodPost, "new@partner.com", url.Values{"name": {"Ada again"}}))
	if len(reqs.calls) != 1 {
		t.Fatalf("resubmit recorded again: %v", reqs.calls)
	}
}

func TestIngressLogin_RequestAccessRefusals(t *testing.T) {
	gateFixture(t, nil, nil)
	reqs := &fakeRequests{status: map[string]string{"no@partner.com": "DENIED"}}
	h := requestHandler(reqs)

	w := httptest.NewRecorder()
	h(w, loginReq(http.MethodGet, "x@elsewhere.com", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("not-accepted domain: got %d, want 403", w.Code)
	}

	w = httptest.NewRecorder()
	h(w, loginReq(http.MethodPost, "x@elsewhere.com", url.Values{"name": {"X"}}))
	if w.Code != http.StatusMethodNotAllowed || len(reqs.calls) != 0 {
		t.Errorf("not-accepted POST: got %d calls=%v", w.Code, reqs.calls)
	}

	w = httptest.NewRecorder()
	h(w, loginReq(http.MethodPost, "new@partner.com", url.Values{"name": {"  "}}))
	if len(reqs.calls) != 0 || !strings.Contains(w.Body.String(), "Enter your name") {
		t.Errorf("blank name must re-show the form, got calls=%v", reqs.calls)
	}

	cross := loginReq(http.MethodPost, "new@partner.com", url.Values{"name": {"Ada"}})
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h(w, cross)
	if w.Code != http.StatusForbidden || len(reqs.calls) != 0 {
		t.Errorf("cross-site POST: got %d calls=%v", w.Code, reqs.calls)
	}

	w = httptest.NewRecorder()
	h(w, loginReq(http.MethodPost, "no@partner.com", url.Values{"name": {"No"}}))
	if len(reqs.calls) != 0 || !strings.Contains(w.Body.String(), "Request declined") {
		t.Errorf("a declined request must stay declined, got calls=%v", reqs.calls)
	}
}

func TestIngressLogin_ApprovedAccountSignsInWithoutGroups(t *testing.T) {
	gateFixture(t, []string{"ada@partner.com"}, nil)
	w := httptest.NewRecorder()
	requestHandler(&fakeRequests{status: map[string]string{}})(w, loginReq(http.MethodGet, "ada@partner.com", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("got %d %s, want a redirect with a session", w.Code, w.Body.String())
	}
	var minted bool
	for _, c := range w.Result().Cookies() {
		minted = minted || (c.Name == CookieName && c.Value != "")
	}
	if !minted {
		t.Fatal("no session cookie minted for an approved account")
	}
}
