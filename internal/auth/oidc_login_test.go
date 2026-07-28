package auth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

// fakeIdP is a minimal OpenID Connect provider: discovery, JWKS, and a token
// endpoint. The test plays the browser: it inspects the authorization
// redirect, then hands the callback a code the token endpoint will honor.
type fakeIdP struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	clientID string

	// captured from the authorization redirect / consumed at /token
	codes map[string]fakeAuthReq // code → what the "user" approved
	// email/groups the IdP will assert for the next token
	email  string
	groups []string
	// nonce override: if set, the id_token carries this nonce instead of
	// the one from the authorization request (to test nonce validation).
	wrongNonce string
	// emailUnverified: if set, the id_token carries email_verified=false —
	// as a bool, or as the string "false" when emailUnverifiedAsString is
	// also set (the Cognito-style shape).
	emailUnverified         bool
	emailUnverifiedAsString bool
}

type fakeAuthReq struct {
	nonce     string
	challenge string // PKCE S256 challenge
}

func newFakeIdP(t *testing.T, clientID string) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, clientID: clientID, codes: map[string]fakeAuthReq{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := &f.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.Form.Get("code")
		req, ok := f.codes[code]
		if !ok {
			http.Error(w, "bad code", 400)
			return
		}
		delete(f.codes, code)
		// PKCE: S256(verifier) must equal the challenge from authorize.
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != req.challenge {
			http.Error(w, "pkce mismatch", 400)
			return
		}
		nonce := req.nonce
		if f.wrongNonce != "" {
			nonce = f.wrongNonce
		}
		idClaims := map[string]any{
			"iss": f.srv.URL, "aud": f.clientID, "sub": "u1",
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"nonce": nonce, "email": f.email, "groups": f.groups,
			"name": "Test User",
		}
		if f.emailUnverified {
			if f.emailUnverifiedAsString {
				idClaims["email_verified"] = "false"
			} else {
				idClaims["email_verified"] = false
			}
		}
		idTok := f.signIDToken(t, idClaims)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "id_token": idTok, "expires_in": 3600,
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) signIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// authorize simulates the user approving at the IdP: given the login
// handler's 302 Location, it captures state/nonce/challenge and returns the
// callback URL the browser would be sent to.
func (f *fakeIdP) authorize(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != f.clientID {
		t.Fatalf("client_id = %q", got)
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	code := fmt.Sprintf("code-%d", len(f.codes)+1)
	f.codes[code] = fakeAuthReq{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	cb, _ := url.Parse(q.Get("redirect_uri"))
	cbq := cb.Query()
	cbq.Set("code", code)
	cbq.Set("state", q.Get("state"))
	cb.RawQuery = cbq.Encode()
	return cb.String()
}

// loginStack builds an OIDCLogin wired to the fake IdP plus a router with
// /auth/login and /auth/callback mounted.
func loginStack(t *testing.T, f *fakeIdP, requiredGroups []string) (*http.ServeMux, *auth.JWTSigner, auth.PairStore) {
	t.Helper()
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	pairs := auth.NewInMemPairStore()
	l, err := auth.NewOIDCLogin(context.Background(), auth.OIDCLoginConfig{
		IssuerURL:      f.srv.URL,
		ClientID:       f.clientID,
		ClientSecret:   "shh",
		Scopes:         []string{"openid", "email", "profile"},
		GroupsClaim:    "groups",
		BaseURL:        "http://arti.test",
		RequiredGroups: requiredGroups,
		Signer:         signer,
		Pairs:          pairs,
		AccessTTL:      time.Hour,
		StateKey:       []byte("test-signing-key-at-least-32-bytes!"),
	})
	if err != nil {
		t.Fatalf("NewOIDCLogin: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", l.LoginHandler())
	mux.HandleFunc("/auth/callback", l.CallbackHandler())
	mux.Handle("/auth/login/confirm", auth.ConfirmLoginHandler(signer, pairs, nil, false))
	return mux, signer, pairs
}

// drive runs login → IdP consent → callback, carrying cookies like a browser.
func drive(t *testing.T, mux *http.ServeMux, f *fakeIdP, loginPath string) *httptest.ResponseRecorder {
	t.Helper()
	// Step 1: /auth/login
	r1 := httptest.NewRequest("GET", loginPath, nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, r1)
	if w1.Code != http.StatusFound {
		t.Fatalf("login: code = %d, body=%s", w1.Code, w1.Body.String())
	}
	// Step 2: IdP consent → callback URL
	cbURL := f.authorize(t, w1.Header().Get("Location"))
	// Step 3: /auth/callback with the state cookie
	r2 := httptest.NewRequest("GET", cbURL, nil)
	for _, c := range w1.Result().Cookies() {
		r2.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)
	return w2
}

func sessionCookie(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
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

func TestOIDCLogin_BrowserFlow(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email, f.groups = "alice@example.com", []string{"engineers@example.com"}
	mux, signer, _ := loginStack(t, f, nil)

	w := drive(t, mux, f, "/auth/login?return_to=/s/some-doc")
	if w.Code != http.StatusFound {
		t.Fatalf("callback: code = %d, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/s/some-doc" {
		t.Errorf("redirect = %q, want /s/some-doc", got)
	}
	ck := sessionCookie(w.Result())
	if ck == nil {
		t.Fatal("no arti_session cookie set")
	}
	claims, err := signer.Verify(ck.Value)
	if err != nil {
		t.Fatalf("session verify: %v", err)
	}
	if claims.Email != "alice@example.com" {
		t.Errorf("session email = %q", claims.Email)
	}
}

func TestOIDCLogin_RequiredGroupEnforced(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email, f.groups = "alice@example.com", []string{"sales@example.com"}
	mux, _, _ := loginStack(t, f, []string{"engineers"})

	if w := drive(t, mux, f, "/auth/login"); w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 for missing group", w.Code)
	}

	f.groups = []string{"engineers@example.com"}
	if w := drive(t, mux, f, "/auth/login"); w.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302 with group present", w.Code)
	}

	// Group matching is case-insensitive, like the bearer path's hasAnyGroup.
	f.groups = []string{"Engineers@example.com"}
	if w := drive(t, mux, f, "/auth/login"); w.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302 with differently-cased group", w.Code)
	}
}

func TestOIDCLogin_UnverifiedEmailRejected(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email, f.emailUnverified = "alice@example.com", true
	mux, _, _ := loginStack(t, f, nil)
	w := drive(t, mux, f, "/auth/login")
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 for email_verified=false", w.Code)
	}
	if sessionCookie(w.Result()) != nil {
		t.Fatal("session cookie must not be set for an unverified email")
	}

	// Cognito-style string form of the claim must be honored too.
	f2 := newFakeIdP(t, "arti")
	f2.email, f2.emailUnverified, f2.emailUnverifiedAsString = "alice@example.com", true, true
	mux2, _, _ := loginStack(t, f2, nil)
	if w := drive(t, mux2, f2, "/auth/login"); w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 for email_verified=\"false\" (string)", w.Code)
	}
}

func TestOIDCLogin_DomainAllowlistEnforced(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email = "mallory@evil.test"
	mux, _, _ := loginStack(t, f, nil)
	if w := drive(t, mux, f, "/auth/login"); w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 for disallowed domain", w.Code)
	}
}

func TestOIDCLogin_StateMismatchRejected(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email = "alice@example.com"
	mux, _, _ := loginStack(t, f, nil)

	r1 := httptest.NewRequest("GET", "/auth/login", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, r1)
	cbURL := f.authorize(t, w1.Header().Get("Location"))
	u, _ := url.Parse(cbURL)
	q := u.Query()
	q.Set("state", "forged")
	u.RawQuery = q.Encode()

	r2 := httptest.NewRequest("GET", u.String(), nil)
	for _, c := range w1.Result().Cookies() {
		r2.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 for state mismatch", w2.Code)
	}
	if sessionCookie(w2.Result()) != nil {
		t.Fatal("session cookie must not be set on state mismatch")
	}
}

func TestOIDCLogin_NonceMismatchRejected(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email, f.wrongNonce = "alice@example.com", "not-the-nonce"
	mux, _, _ := loginStack(t, f, nil)
	w := drive(t, mux, f, "/auth/login")
	if w.Code == http.StatusFound && sessionCookie(w.Result()) != nil {
		t.Fatal("login succeeded despite nonce mismatch")
	}
}

func TestOIDCLogin_MissingStateCookieRejected(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email = "alice@example.com"
	mux, _, _ := loginStack(t, f, nil)

	r1 := httptest.NewRequest("GET", "/auth/login", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, r1)
	cbURL := f.authorize(t, w1.Header().Get("Location"))
	r2 := httptest.NewRequest("GET", cbURL, nil) // no cookies
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 without state cookie", w2.Code)
	}
}

func TestOIDCLogin_CLIPairing(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email = "alice@example.com"
	mux, _, pairs := loginStack(t, f, nil)

	w := drive(t, mux, f, "/auth/login?cli_code=abc123")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 confirmation page; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Authorize CLI sign-in") {
		t.Errorf("body = %q, want confirmation page", w.Body.String())
	}
	if email, ok := pairs.Take("abc123"); ok || email != "" {
		t.Fatalf("GET must not bind CLI code, got %q, %v", email, ok)
	}
	state := confirmationState(w.Body.String())
	if state == "" {
		t.Fatal("confirmation state missing")
	}
	r := httptest.NewRequest("POST", "/auth/login/confirm", strings.NewReader("state="+state))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r)
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), "close this tab") {
		t.Fatalf("confirm: code = %d, body=%s", w2.Code, w2.Body.String())
	}
	if email, ok := pairs.Take("abc123"); !ok || email != "alice@example.com" {
		t.Errorf("pairs.Take = %q, %v; want alice@example.com, true", email, ok)
	}
}

func TestOIDCLogin_ReturnToSanitized(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email = "alice@example.com"
	mux, _, _ := loginStack(t, f, nil)
	w := drive(t, mux, f, "/auth/login?return_to=https://evil.test/phish")
	if w.Code != http.StatusFound {
		t.Fatalf("code = %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("redirect = %q, want / for absolute return_to", got)
	}
}

func TestRequireAuthOrRedirect_ForbiddenProxyToken(t *testing.T) {
	f := newFakeIdP(t, "arti")
	f.email = "alice@example.com"
	verifier, err := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{
		IssuerURL:      f.srv.URL,
		Audience:       "arti",
		RequiredGroups: []string{"engineers"},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth.SetAllowedDomains([]string{"example.com"})
	t.Cleanup(func() { auth.SetAllowedDomains([]string{"example.com", "example.org"}) })
	token := f.signIDToken(t, map[string]any{
		"iss":    f.srv.URL,
		"aud":    "arti",
		"sub":    "u1",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"iat":    time.Now().Unix(),
		"email":  "alice@example.com",
		"groups": []string{"other"},
	})

	mw := auth.RequireAuthOrRedirect(auth.NewConfig(verifier, nil, "", []string{"engineers"}), "/auth/login")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/app/demo?x=1", nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set(auth.ProxyTokenHeader, token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("want 302, got %d; body=%s", rec.Code, rec.Body.String())
	}
	want := "/auth/login?return_to=" + url.QueryEscape("/app/demo?x=1")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	reqAPI := httptest.NewRequest(http.MethodGet, "/app/demo?x=1", nil)
	reqAPI.Header.Set("Accept", "application/json")
	reqAPI.Header.Set(auth.ProxyTokenHeader, token)
	recAPI := httptest.NewRecorder()
	h.ServeHTTP(recAPI, reqAPI)
	if recAPI.Code != http.StatusForbidden {
		t.Fatalf("API request: want 403, got %d", recAPI.Code)
	}
	if !strings.Contains(recAPI.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("API body = %q, want forbidden JSON", recAPI.Body.String())
	}
}
