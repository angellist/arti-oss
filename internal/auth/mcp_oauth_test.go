package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// fakeMCPStore is an in-memory MCPOAuthStore for the authorize/consent
// handlers. Only the two calls those handlers make are meaningful; the rest
// satisfy the interface.
type fakeMCPStore struct {
	clients map[string]sqlc.McpOauthClient
	codes   []sqlc.InsertMCPCodeParams
}

func newFakeMCPStore() *fakeMCPStore {
	return &fakeMCPStore{clients: map[string]sqlc.McpOauthClient{}}
}

func (f *fakeMCPStore) register(clientID, name string, redirectURIs ...string) {
	f.clients[clientID] = sqlc.McpOauthClient{
		ClientID: clientID, ClientName: &name, RedirectUris: redirectURIs,
		TokenEndpointAuthMethod: "none",
	}
}

func (f *fakeMCPStore) GetMCPClient(_ context.Context, clientID string) (sqlc.McpOauthClient, error) {
	c, ok := f.clients[clientID]
	if !ok {
		return sqlc.McpOauthClient{}, pgx.ErrNoRows
	}
	return c, nil
}

func (f *fakeMCPStore) InsertMCPCode(_ context.Context, arg sqlc.InsertMCPCodeParams) error {
	f.codes = append(f.codes, arg)
	return nil
}

func (f *fakeMCPStore) InsertMCPClient(context.Context, sqlc.InsertMCPClientParams) error { return nil }
func (f *fakeMCPStore) TouchMCPClient(context.Context, string) error                      { return nil }
func (f *fakeMCPStore) TakeMCPCode(context.Context, string) (sqlc.McpOauthCode, error) {
	return sqlc.McpOauthCode{}, pgx.ErrNoRows
}

const (
	testClientID    = "arti-mcp-testclient"
	testRedirectURI = "https://gateway.example.com/cb"
	testUser        = "ada@example.com"
)

// testAuthorizeCfg is a "proxy"-mode deployment: an authenticating proxy is
// declared to be in front, so the X-Auth-Request-* headers are trusted.
func testAuthorizeCfg(st *fakeMCPStore, requiredGroups ...string) MCPOAuthConfig {
	cfg := testOIDCAuthorizeCfg(st, requiredGroups...)
	cfg.TrustProxyHeaders = true
	return cfg
}

// testOIDCAuthorizeCfg is an "oidc"-mode deployment: arti faces the network
// itself with nothing overwriting the identity headers, which is what
// self-hosters run. Identity may come only from the arti_session cookie.
func testOIDCAuthorizeCfg(st *fakeMCPStore, requiredGroups ...string) MCPOAuthConfig {
	return MCPOAuthConfig{
		BaseURL: "https://arti.example.com", Store: st, Signer: NewJWTSigner([]byte("k")),
		AccessTTL: time.Hour, RefreshTTL: time.Hour, RequiredGroups: requiredGroups,
	}
}

// sessionCookieFor mints the arti_session cookie a browser carries after
// signing in through /auth/login, the way loginFinisher.finish does.
func sessionCookieFor(t *testing.T, cfg MCPOAuthConfig, email string) *http.Cookie {
	t.Helper()
	jwt, err := cfg.Signer.Sign(Claims{Email: email, Scopes: []string{"user"}, TTL: time.Hour})
	if err != nil {
		t.Fatalf("sign session: %v", err)
	}
	return &http.Cookie{Name: CookieName, Value: jwt}
}

func authorizeURL() string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {testClientID},
		"redirect_uri":          {testRedirectURI},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
		"state":                 {"client-state-123"},
		"scope":                 {"user"},
	}
	return "/oauth/authorize?" + q.Encode()
}

// authorizeGET performs the consent-page GET as an identified user and
// returns the recorder.
func authorizeGET(t *testing.T, cfg MCPOAuthConfig, groups string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, authorizeURL(), nil)
	r.Header.Set(IngressEmailHeader, testUser)
	if groups != "" {
		r.Header.Set(IngressGroupsHeader, groups)
	}
	w := httptest.NewRecorder()
	MCPAuthorizeHandler(cfg)(w, r)
	return w
}

// consentFrom extracts the hidden consent blob and the identity cookie the
// consent page issued, i.e. exactly what a real browser would post back.
func consentFrom(t *testing.T, w *httptest.ResponseRecorder) (blob string, cookie *http.Cookie) {
	t.Helper()
	body := w.Body.String()
	const marker = `name="consent" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("consent page has no hidden consent field; body was:\n%s", body)
	}
	rest := body[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatal("unterminated consent field")
	}
	blob = rest[:j]
	for _, c := range w.Result().Cookies() {
		if c.Name == mcpConsentCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("consent page set no identity cookie")
	}
	return blob, cookie
}

// postConsent replays the consent form. A nil cookie models a cross-site
// POST, where the Strict-SameSite cookie is not sent.
func postConsent(cfg MCPOAuthConfig, blob string, cookie *http.Cookie, headerEmail string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/oauth/authorize/confirm",
		strings.NewReader(url.Values{"consent": {blob}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if headerEmail != "" {
		r.Header.Set(IngressEmailHeader, headerEmail)
	}
	w := httptest.NewRecorder()
	MCPAuthorizeConfirmHandler(cfg)(w, r)
	return w
}

// The GET is a navigation, and a navigation must not be enough to obtain a
// code — that is the whole defect this endpoint had. It renders a page and
// writes nothing.
func TestAuthorizeGETRendersConsentAndMintsNoCode(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	w := authorizeGET(t, testAuthorizeCfg(st), "")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 consent page, got %d: %s", w.Code, w.Body.String())
	}
	if len(st.codes) != 0 {
		t.Fatalf("authorize GET minted %d code(s); it must mint none", len(st.codes))
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("authorize GET redirected to %q; it must render a page", loc)
	}
	body := w.Body.String()
	// The user can only judge the request if the page names who is asking
	// and where approval sends them.
	for _, want := range []string{"Test Gateway", testRedirectURI, testUser, "/oauth/authorize/confirm"} {
		if !strings.Contains(body, want) {
			t.Errorf("consent page does not mention %q", want)
		}
	}
}

// A forged identity header alone yields no code, because a code now requires
// the confirm POST. Mirrors the intent of middleware_c1_test.go: nothing
// reachable by shaping a request produces credentials.
func TestForgedIdentityHeaderAloneYieldsNoCode(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	r := httptest.NewRequest(http.MethodGet, authorizeURL(), nil)
	r.Header.Set(IngressEmailHeader, "victim@example.com")
	w := httptest.NewRecorder()
	MCPAuthorizeHandler(testAuthorizeCfg(st))(w, r)

	if len(st.codes) != 0 {
		t.Fatalf("a bare GET minted a code for victim@example.com")
	}
	if w.Code == http.StatusFound {
		t.Fatalf("a bare GET redirected to the client")
	}
}

func TestConfirmRejectsUnusableConsentBlobs(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	good, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	// A blob signed with a different key stands in for an attacker-minted
	// one: the attacker has no access to the signing key.
	otherCfg := testAuthorizeCfg(st)
	otherCfg.Signer = NewJWTSigner([]byte("other-key"))
	forged := signLoginBlob(otherCfg.Signer, mcpConsentStateDomain, mcpConsentState{
		Email: testUser, ClientID: testClientID, RedirectURI: testRedirectURI,
		CodeChallenge: "x", Exp: time.Now().Add(time.Minute).Unix(),
	})
	expired := signLoginBlob(cfg.Signer, mcpConsentStateDomain, mcpConsentState{
		Email: testUser, ClientID: testClientID, RedirectURI: testRedirectURI,
		CodeChallenge: "x", Exp: time.Now().Add(-time.Second).Unix(),
	})
	// Signed by us, for a domain we never issue at this endpoint — domain
	// separation must stop an identity blob being used as a consent blob.
	wrongDomain := signLoginConfirmIdentity(cfg.Signer, loginConfirmIdentity{
		Email: testUser, Exp: time.Now().Add(time.Minute).Unix(),
	})

	for _, tc := range []struct{ name, blob string }{
		{"empty", ""},
		{"garbage", "not-a-blob"},
		{"truncated signature", good[:len(good)-4]},
		{"foreign signing key", forged},
		{"expired", expired},
		{"wrong signing domain", wrongDomain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(st.codes)
			w := postConsent(cfg, tc.blob, cookie, testUser)
			if w.Code != http.StatusForbidden {
				t.Errorf("want 403, got %d", w.Code)
			}
			if len(st.codes) != before {
				t.Errorf("a %s consent blob minted a code", tc.name)
			}
		})
	}
}

// SameSite is enforced by the browser, so no Go test can exercise a real
// cross-site POST. The attributes on the cookie are the only thing the unit
// level can prove; the cross-site behaviour itself is covered end to end.
func TestConsentCookieAttributes(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	cfg.CookieSecure = true
	_, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite=%v, want Strict — Lax would allow a cross-site submit", cookie.SameSite)
	}
	if cookie.Path != "/oauth" {
		t.Errorf("Path=%q, want /oauth so it is not sent to the /auth confirm endpoints", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Error("cookie is readable by scripts")
	}
	if !cookie.Secure {
		t.Error("cookie not marked Secure under CookieSecure")
	}
	if cookie.MaxAge <= 0 || cookie.MaxAge > 600 {
		t.Errorf("MaxAge=%d, want a short positive window", cookie.MaxAge)
	}
}

// The consent cookie is signed under its own domain, so an identity blob
// minted by the CLI/device confirmation flow cannot stand in for it. Both
// blobs carry the same fields; only the signing label separates them.
func TestConsentCookieRejectsForeignIdentityDomain(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	foreign := *cookie
	foreign.Value = signLoginConfirmIdentity(cfg.Signer, loginConfirmIdentity{
		Email: testUser, Exp: time.Now().Add(time.Minute).Unix(),
	})
	if w := postConsent(cfg, blob, &foreign, testUser); w.Code != http.StatusForbidden {
		t.Errorf("CLI-domain identity blob accepted as a consent cookie: got %d, want 403", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatal("minted a code from an identity blob signed for another purpose")
	}
}

// The confirm endpoint is always behind the front door, so an absent
// identity is a broken deployment. It must not be treated as a pass.
func TestConfirmRequiresFrontDoorIdentity(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	if w := postConsent(cfg, blob, cookie, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no identity header: want 401, got %d", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatal("minted a code for a request the front door did not identify")
	}
}

// The Strict-SameSite identity cookie is what stops a leaked consent blob
// being submitted from another site.
func TestConfirmRequiresIdentityCookie(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	if w := postConsent(cfg, blob, nil, testUser); w.Code != http.StatusUnauthorized {
		t.Errorf("cross-site POST (no cookie): want 401, got %d", w.Code)
	}
	// A cookie proving a *different* user must not approve this request.
	// Signed under the consent domain on purpose, so it clears the HMAC gate
	// and the rejection can only come from the email comparison.
	other := *cookie
	other.Value = signLoginBlob(cfg.Signer, mcpConsentIdentityDomain, loginConfirmIdentity{
		Email: "mallory@example.com", Exp: time.Now().Add(time.Minute).Unix(),
	})
	if w := postConsent(cfg, blob, &other, testUser); w.Code != http.StatusForbidden {
		t.Errorf("mismatched identity cookie: want 403, got %d", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatalf("minted %d code(s) without a matching identity cookie", len(st.codes))
	}
}

// If the front door identifies the POST as someone else, the consent page
// was rendered for a different session and must not be honoured.
func TestConfirmRejectsFrontDoorIdentityMismatch(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	w := postConsent(cfg, blob, cookie, "mallory@example.com")
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatal("minted a code for a request the front door attributes to someone else")
	}
}

// A client deleted (or its redirect URI changed) while the page sat open
// must not be usable on approval.
func TestConfirmRevalidatesClient(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	st.register(testClientID, "Test Gateway", "https://gateway.example.com/other")
	if w := postConsent(cfg, blob, cookie, testUser); w.Code != http.StatusBadRequest {
		t.Errorf("redirect_uri no longer registered: want 400, got %d", w.Code)
	}
	delete(st.clients, testClientID)
	if w := postConsent(cfg, blob, cookie, testUser); w.Code != http.StatusUnauthorized {
		t.Errorf("client deleted: want 401, got %d", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatalf("minted %d code(s) for an invalid client", len(st.codes))
	}
}

func TestConsentApprovalMintsBoundCode(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGET(t, cfg, ""))

	w := postConsent(cfg, blob, cookie, testUser)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302 to the client, got %d: %s", w.Code, w.Body.String())
	}
	if len(st.codes) != 1 {
		t.Fatalf("want exactly 1 code, got %d", len(st.codes))
	}
	got := st.codes[0]
	// The code must be bound to the request the user actually saw, not to
	// anything the POST could restate.
	if got.Email != testUser || got.ClientID != testClientID || got.RedirectUri != testRedirectURI {
		t.Errorf("code bound to the wrong request: %+v", got)
	}
	if got.CodeChallenge != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" || got.CodeChallengeMethod != "S256" {
		t.Errorf("code not bound to the PKCE challenge: %+v", got)
	}
	if got.Scope == nil || *got.Scope != "user" {
		t.Errorf("scope not carried across consent: %+v", got.Scope)
	}

	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme+"://"+loc.Host+loc.Path != testRedirectURI {
		t.Errorf("redirected to %q, want %q", loc, testRedirectURI)
	}
	if loc.Query().Get("code") != got.Code {
		t.Errorf("returned code %q, minted %q", loc.Query().Get("code"), got.Code)
	}
	// Losing `state` would break the client's own CSRF check.
	if loc.Query().Get("state") != "client-state-123" {
		t.Errorf("client state not preserved: %q", loc.Query().Get("state"))
	}
	// Single-use: the cookie is cleared so the page cannot be resubmitted.
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == mcpConsentCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("identity cookie not cleared after approval")
	}
}

// The authorize path must apply the same required-group gate as the two
// interactive login front doors, rather than relying on the group list
// happening to include a group everyone carries.
func TestAuthorizeEnforcesRequiredGroups(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st, "engineers")

	if w := authorizeGET(t, cfg, "marketing"); w.Code != http.StatusForbidden {
		t.Errorf("caller in no required group: want 403, got %d", w.Code)
	}
	if w := authorizeGET(t, cfg, ""); w.Code != http.StatusForbidden {
		t.Errorf("caller with no groups header: want 403, got %d", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatal("minted a code for a caller outside the required groups")
	}
	w := authorizeGET(t, cfg, "engineers,other")
	if w.Code != http.StatusOK {
		t.Fatalf("caller in a required group: want the consent page, got %d", w.Code)
	}
	// Losing the group while the page sits open must block approval too.
	blob, cookie := consentFrom(t, w)
	r := httptest.NewRequest(http.MethodPost, "/oauth/authorize/confirm",
		strings.NewReader(url.Values{"consent": {blob}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set(IngressEmailHeader, testUser)
	r.Header.Set(IngressGroupsHeader, "marketing")
	r.AddCookie(cookie)
	cw := httptest.NewRecorder()
	MCPAuthorizeConfirmHandler(cfg)(cw, r)
	if cw.Code != http.StatusForbidden {
		t.Errorf("group lost before approval: want 403, got %d", cw.Code)
	}
	if len(st.codes) != 0 {
		t.Fatal("minted a code for a caller outside the required groups")
	}
}

func TestAuthorizeRejectsUnknownClientAndRedirectURI(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testAuthorizeCfg(st)

	r := httptest.NewRequest(http.MethodGet, strings.Replace(authorizeURL(),
		url.QueryEscape(testRedirectURI), url.QueryEscape("https://attacker.example/cb"), 1), nil)
	r.Header.Set(IngressEmailHeader, testUser)
	w := httptest.NewRecorder()
	MCPAuthorizeHandler(cfg)(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unregistered redirect_uri: want 400, got %d", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, strings.Replace(authorizeURL(), testClientID, "arti-mcp-nope", 1), nil)
	r.Header.Set(IngressEmailHeader, testUser)
	w = httptest.NewRecorder()
	MCPAuthorizeHandler(cfg)(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unknown client_id: want 401, got %d", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatal("minted a code for an invalid authorization request")
	}
}

// ─── oidc mode: no proxy in front ────────────────────────────────────
//
// arti-oss's recommended self-hosted configuration runs arti facing the
// network with nothing overwriting X-Auth-Request-*. A header is then just
// a string the caller chose, so identity has to come from arti's own
// signed session instead.

// authorizeGETAs drives the consent GET with an explicit request shape:
// any session cookie the browser holds, and any identity header the caller
// chose to send.
func authorizeGETAs(cfg MCPOAuthConfig, session *http.Cookie, headerEmail, groups string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, authorizeURL(), nil)
	if session != nil {
		r.AddCookie(session)
	}
	if headerEmail != "" {
		r.Header.Set(IngressEmailHeader, headerEmail)
	}
	if groups != "" {
		r.Header.Set(IngressGroupsHeader, groups)
	}
	w := httptest.NewRecorder()
	MCPAuthorizeHandler(cfg)(w, r)
	return w
}

// The defect: with no proxy to overwrite it, anyone who can reach the
// endpoint sets X-Auth-Request-Email and is whoever they say they are. In
// oidc mode the header must buy nothing at all — no consent page to
// approve, and so no route to a code.
func TestOIDCModeIgnoresForgedIdentityHeader(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st)

	w := authorizeGETAs(cfg, nil, "victim@example.com", "arti-admins")

	if w.Code != http.StatusFound {
		t.Fatalf("forged header: want 302 to login, got %d: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/auth/login?") {
		t.Errorf("want a redirect to arti's own login, got %q", loc)
	}
	if body := w.Body.String(); strings.Contains(body, `name="consent"`) {
		t.Error("a forged header rendered an approvable consent page")
	}
	if len(st.codes) != 0 {
		t.Fatalf("forged header minted %d code(s)", len(st.codes))
	}
}

// The other half of the same rule: the approving POST must not accept the
// header either. An attacker holding a leaked consent blob and its cookie
// still cannot approve it by asserting the victim's email.
func TestOIDCModeConfirmIgnoresForgedIdentityHeader(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGETAs(cfg, sessionCookieFor(t, cfg, testUser), "", ""))

	// postConsent sends the identity header and no arti_session cookie.
	if w := postConsent(cfg, blob, cookie, testUser); w.Code != http.StatusUnauthorized {
		t.Errorf("forged header on confirm: want 401, got %d", w.Code)
	}
	if len(st.codes) != 0 {
		t.Fatalf("forged header on confirm minted %d code(s)", len(st.codes))
	}
}

// With no session and no trusted proxy there is nobody to ask, so the flow
// goes through arti's own login and comes back to the same request. This is
// what makes MCP OAuth usable without a proxy at all, instead of the 401
// that made it unusable.
func TestOIDCModeRedirectsAnonymousToLogin(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)

	w := authorizeGETAs(testOIDCAuthorizeCfg(st), nil, "", "")

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	want := "/auth/login?return_to=" + url.QueryEscape(authorizeURL())
	if loc != want {
		t.Errorf("redirect = %q, want %q", loc, want)
	}
	// The login handlers drop a return_to that is not a relative path, so a
	// non-relative one would silently strand the user on "/".
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("unparseable redirect: %v", err)
	}
	if rt := u.Query().Get("return_to"); !strings.HasPrefix(rt, "/") || strings.HasPrefix(rt, "//") {
		t.Errorf("return_to = %q, which the login handlers will discard", rt)
	}
}

// The working path in oidc mode: a browser carrying arti's own verified
// session reaches the consent page and its approval mints a code bound to
// the cookie's user.
func TestOIDCModeIdentifiesFromSessionCookie(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st)
	session := sessionCookieFor(t, cfg, testUser)

	w := authorizeGETAs(cfg, session, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want the consent page, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), testUser) {
		t.Errorf("consent page does not name the signed-in user %q", testUser)
	}
	if len(st.codes) != 0 {
		t.Fatal("the GET minted a code")
	}

	blob, cookie := consentFrom(t, w)
	r := httptest.NewRequest(http.MethodPost, "/oauth/authorize/confirm",
		strings.NewReader(url.Values{"consent": {blob}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	r.AddCookie(session)
	rec := httptest.NewRecorder()
	MCPAuthorizeConfirmHandler(cfg)(rec, r)

	if rec.Code != http.StatusFound {
		t.Fatalf("approval: want 302 back to the client, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(st.codes) != 1 {
		t.Fatalf("approval minted %d codes, want 1", len(st.codes))
	}
	if got := st.codes[0].Email; got != testUser {
		t.Errorf("code bound to %q, want the signed-in user %q", got, testUser)
	}
}

// A session belonging to someone other than the person the page was
// rendered for must not approve it, the same way a mismatched proxy
// identity does not.
func TestOIDCModeConfirmRejectsDifferentSession(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st)
	blob, cookie := consentFrom(t, authorizeGETAs(cfg, sessionCookieFor(t, cfg, testUser), "", ""))

	r := httptest.NewRequest(http.MethodPost, "/oauth/authorize/confirm",
		strings.NewReader(url.Values{"consent": {blob}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	r.AddCookie(sessionCookieFor(t, cfg, "mallory@example.com"))
	rec := httptest.NewRecorder()
	MCPAuthorizeConfirmHandler(cfg)(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if len(st.codes) != 0 {
		t.Fatal("approved a consent page rendered for a different user")
	}
}

// A session cookie that does not verify under arti's signing key is not an
// identity. Without this the "cookie path" would be as forgeable as the
// header path it replaces.
func TestOIDCModeRejectsUnverifiableSessionCookie(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st)

	// Correctly shaped and correctly signed — under the wrong key.
	forged := NewJWTSigner([]byte("not-arti's-key"))
	jwt, err := forged.Sign(Claims{Email: "victim@example.com", Scopes: []string{"user"}, TTL: time.Hour})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	w := authorizeGETAs(cfg, &http.Cookie{Name: CookieName, Value: jwt}, "", "")

	if w.Code != http.StatusFound {
		t.Fatalf("foreign-signed session: want 302 to login, got %d: %s", w.Code, w.Body.String())
	}
	if len(st.codes) != 0 {
		t.Fatal("a foreign-signed session minted a code")
	}
}

// An expired session is not an identity either. This needs its own case
// because JWTSigner.Verify returns the parsed claims *alongside* ErrExpired
// — the signature really was arti's — so a caller that reads the claims and
// drops the error authorizes an expired session. Verify's own comment says
// callers must still treat it as unauthenticated. A wrong-key token cannot
// catch that regression: it comes back with empty claims either way.
func TestOIDCModeRejectsExpiredSessionCookie(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st)

	// Authentically signed by arti, expired an hour ago.
	jwt, err := cfg.Signer.Sign(Claims{Email: testUser, Scopes: []string{"user"}, TTL: -time.Hour})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if claims, verr := cfg.Signer.Verify(jwt); claims.Email == "" || verr == nil {
		t.Fatalf("fixture is not the case under test: want populated claims with an error, got %q / %v", claims.Email, verr)
	}

	w := authorizeGETAs(cfg, &http.Cookie{Name: CookieName, Value: jwt}, "", "")

	if w.Code != http.StatusFound {
		t.Fatalf("expired session: want 302 to login, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `name="consent"`) {
		t.Error("an expired session rendered an approvable consent page")
	}
	if len(st.codes) != 0 {
		t.Fatal("an expired session minted a code")
	}
}

// The required-groups gate is deliberately not re-applied on the cookie
// path: both login front doors apply it before minting arti_session, and
// the request carries no groups to check. Pinned as a test so the
// asymmetry with the proxy path reads as intent rather than an oversight.
func TestOIDCModeSessionCookieSatisfiesGroupGate(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st, "arti-users")

	w := authorizeGETAs(cfg, sessionCookieFor(t, cfg, testUser), "", "")

	if w.Code != http.StatusOK {
		t.Fatalf("want the consent page, got %d: %s", w.Code, w.Body.String())
	}
	// The groups header must not be a way to satisfy the gate here either.
	if w2 := authorizeGETAs(cfg, nil, testUser, "arti-users"); w2.Code != http.StatusFound {
		t.Errorf("headers alone in oidc mode: want 302 to login, got %d", w2.Code)
	}
}

// A signature is not an authorization. Every narrower token arti mints is
// signed by the same key, and nothing stops a caller putting one in the
// arti_session cookie by hand — no browser is involved, so HttpOnly and the
// cookie's origin scoping buy nothing here. Accepting one would launder a
// token that is exposed in served-page source (embed, app) or deliberately
// restricted (device upload, CLI) into a full 7-day `user` MCP token.
func TestOIDCModeRejectsNarrowerScopedTokensInSessionCookie(t *testing.T) {
	st := newFakeMCPStore()
	st.register(testClientID, "Test Gateway", testRedirectURI)
	cfg := testOIDCAuthorizeCfg(st)

	for _, tc := range []struct {
		name   string
		claims Claims
	}{
		{"embed-scoped", Claims{Email: testUser, Scopes: []string{EmbedScopePrefix + "doc-1"}}},
		{"app-scoped", Claims{Email: testUser, Scopes: []string{AppScopePrefix + "app-1"}}},
		{"device upload-scoped", Claims{Email: testUser, Scopes: []string{UploadScope}}},
		{"device token family", Claims{Email: testUser, Scopes: []string{"user"}, Fam: "fam-1"}},
		{"cli token", Claims{Email: testUser, Scopes: []string{"user"}, Typ: TokenTypeCLI}},
		// Refresh tokens are the long-lived half of the very flow this
		// endpoint feeds (90 days), and are meant to be redeemed only at
		// /oauth/token.
		{"mcp refresh token", Claims{Email: testUser, Scopes: []string{"refresh"}, Typ: TokenTypeMCP}},
		{"legacy untyped refresh token", Claims{Email: testUser, Scopes: []string{"refresh"}}},
		{"mcp access token", Claims{Email: testUser, Scopes: []string{"user"}, Typ: TokenTypeMCP}},
		{"no scopes at all", Claims{Email: testUser}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(st.codes)
			c := tc.claims
			c.TTL = time.Hour
			jwt, err := cfg.Signer.Sign(c)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			// The fixture must be a token that genuinely verifies, or this
			// proves nothing about the scope check.
			if _, verr := cfg.Signer.Verify(jwt); verr != nil {
				t.Fatalf("fixture does not verify: %v", verr)
			}

			w := authorizeGETAs(cfg, &http.Cookie{Name: CookieName, Value: jwt}, "", "")

			if w.Code != http.StatusFound {
				t.Fatalf("want 302 to login, got %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), `name="consent"`) {
				t.Error("rendered an approvable consent page")
			}
			if len(st.codes) != before {
				t.Fatal("minted a code")
			}
		})
	}
}

// The positive control for the case above: an ordinary session cookie, which
// is what loginFinisher.finish mints, is still accepted. Without this a
// scope check that rejected everything would look correct.
func TestSessionCredentialAcceptsAnOrdinaryLoginCookie(t *testing.T) {
	if c := (Claims{Email: testUser, Scopes: []string{"user"}}); !c.IsSessionCredential() {
		t.Fatal("an ordinary login session must count as a session credential")
	}
}
