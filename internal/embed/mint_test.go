package embed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/auth"
)

// TestMain grants the reserved example domain: the mint handler re-checks
// auth.IsAllowed, and the binary ships with an empty (deny-all) allowlist.
func TestMain(m *testing.M) {
	auth.SetAllowedDomains([]string{"example.com"})
	os.Exit(m.Run())
}

type fakeMinter struct {
	title, slug, token string
	appErr, mintErr    error
}

func (f *fakeMinter) EmbedUserApp(_ context.Context, appID, email string) (string, string, error) {
	return f.title, f.slug, f.appErr
}
func (f *fakeMinter) MintEmbedUserToken(_ context.Context, appID, email string) (string, error) {
	return f.token, f.mintErr
}

// fakeStore is an in-memory PendingTokenStore for tests.
type fakeStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]string{}} }
func (s *fakeStore) Put(_ context.Context, surface, state, token, email string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[surface+"\x00"+state] = token
	return nil
}
func (s *fakeStore) Take(_ context.Context, surface, state string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := surface + "\x00" + state
	t := s.m[k]
	delete(s.m, k)
	return t, nil
}

const (
	mintAppID  = "5cc6a0b2-64c8-4c5a-9d4e-000000000001"
	mintKey    = "test-signing-key-at-least-32-bytes!"
	mintSurfSc = "US"
)

func mintSurfaces() map[string]Surface {
	return map[string]Surface{
		"panel": {Secret: mintSurfSc, Origin: OriginList{"https://app.frontapp.com", "https://couch.internal"},
			Identity: "user", SlugAllow: []string{"lp-*"}},
		"svc": {Secret: "S", Origin: OriginList{"https://dash.x"}, Email: "svc@example.com", SlugAllow: []string{"*"}},
	}
}

// mintStack wires a Service with Mount (poll) + MountMint (mint) on one router.
func mintStack(t *testing.T, minter UserTokenMinter, email string, store PendingTokenStore) (chi.Router, PendingTokenStore) {
	t.Helper()
	if store == nil {
		store = newFakeStore()
	}
	r := chi.NewRouter()
	s := New(mintSurfaces(), &fakeArt{}, fakeTok{})
	s.Mount(r)
	s.MountMint(r, MintConfig{
		Identify:     func(*http.Request) string { return email },
		Minter:       minter,
		ChallengeKey: []byte(mintKey),
		Store:        store,
	})
	return r, store
}

func mintQuery(overrides map[string]string) string {
	q := url.Values{"surface": {"panel"}, "auth_secret": {mintSurfSc}, "app": {mintAppID}, "state": {"n0nce"}}
	for k, v := range overrides {
		q.Set(k, v)
		if v == "" {
			q.Del(k)
		}
	}
	return mintPath + "?" + q.Encode()
}

func doMint(r chi.Router, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func doPost(r chi.Router, target string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(rec, req)
	return rec
}

// challengeRe pulls the consent_challenge out of the consent page's inline JS
// config (the value the Continue button POSTs to the completion endpoint).
var challengeRe = regexp.MustCompile(`"challenge":"([^"]+)"`)

func consentChallenge(t *testing.T, r chi.Router, consentTarget string) string {
	t.Helper()
	consent := doMint(r, consentTarget)
	if consent.Code != http.StatusOK {
		t.Fatalf("consent GET: got %d, want 200", consent.Code)
	}
	m := challengeRe.FindStringSubmatch(consent.Body.String())
	if m == nil {
		t.Fatalf("no challenge in consent page:\n%s", consent.Body.String())
	}
	return m[1]
}

// completeConsent runs the whole gate: render consent, then POST the challenge
// to the completion endpoint like the Continue button does.
func completeConsent(t *testing.T, r chi.Router, surface string) *httptest.ResponseRecorder {
	t.Helper()
	ch := consentChallenge(t, r, mintQuery(nil))
	return doPost(r, "/embed/"+surface+"/token",
		url.Values{"auth_secret": {mintSurfSc}, "consent_challenge": {ch}})
}

func TestMintGates(t *testing.T) {
	r, _ := mintStack(t, &fakeMinter{title: "LP", slug: "lp-ctx", token: "TOK"}, "tian@example.com", nil)
	cases := []struct {
		name, target string
		want         int
	}{
		{"unknown surface", mintQuery(map[string]string{"surface": "nope"}), http.StatusNotFound},
		{"service-mode surface", mintQuery(map[string]string{"surface": "svc", "auth_secret": "S"}), http.StatusNotFound},
		{"wrong secret", mintQuery(map[string]string{"auth_secret": "WRONG"}), http.StatusForbidden},
		{"missing app", mintQuery(map[string]string{"app": ""}), http.StatusBadRequest},
		{"missing state", mintQuery(map[string]string{"state": ""}), http.StatusBadRequest},
		{"oversized state", mintQuery(map[string]string{"state": strings.Repeat("x", 300)}), http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := doMint(r, tc.target); rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestMintRedirectsToLoginWithoutSession(t *testing.T) {
	r, _ := mintStack(t, &fakeMinter{}, "", nil)
	rec := doMint(r, mintQuery(nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("got %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	back, err := url.QueryUnescape(strings.TrimPrefix(loc, "/auth/login?return_to="))
	if err != nil || !strings.HasPrefix(back, mintPath+"?") || !strings.Contains(back, "state=n0nce") {
		t.Fatalf("return_to must round-trip the mint URL, got %q", loc)
	}
}

func TestMintDomainAllowlist(t *testing.T) {
	r, _ := mintStack(t, &fakeMinter{title: "T", slug: "lp-ctx", token: "TOK"}, "evil@other.com", nil)
	if rec := doMint(r, mintQuery(nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("disallowed domain: got %d, want 403", rec.Code)
	}
}

func TestMintAppDeniedOrOffSurface(t *testing.T) {
	r, _ := mintStack(t, &fakeMinter{appErr: errors.New("nope")}, "tian@example.com", nil)
	if rec := doMint(r, mintQuery(nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("denied app: got %d, want 404", rec.Code)
	}
	r, _ = mintStack(t, &fakeMinter{title: "T", slug: "report-q3", token: "TOK"}, "tian@example.com", nil)
	if rec := doMint(r, mintQuery(nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("off-allowlist slug: got %d, want 403", rec.Code)
	}
}

func TestMintConsentPage(t *testing.T) {
	r, _ := mintStack(t, &fakeMinter{title: "LP Context", slug: "lp-ctx", token: "TOK-123"}, "tian@example.com", nil)
	rec := doMint(r, mintQuery(nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("consent GET: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "tian@example.com") || !strings.Contains(body, "LP Context") {
		t.Fatal("consent page must show the identity and the app title")
	}
	if strings.Contains(body, "TOK-123") {
		t.Fatal("consent page must carry no token")
	}
	// Continue is a fetch-POST button (works in a sandboxed popup with
	// allow-scripts; not prefetchable), NOT a form and NOT a mint-on-GET link.
	if strings.Contains(body, `method="POST"`) || strings.Contains(body, `consent_challenge`) && strings.Contains(body, "href=") {
		t.Fatal("Continue must be a fetch button, not a form or a mint link")
	}
	if !challengeRe.MatchString(body) || !strings.Contains(body, `fetch(C.complete`) {
		t.Fatalf("consent page must carry a challenge + fetch the completion endpoint:\n%s", body)
	}
	if rec.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" ||
		rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("consent page must be DENY-framed and no-store")
	}
}

// A GET NEVER mints — it only renders consent (so link prefetch/preview can't
// bypass the human click). Minting happens only on the POST completion.
func TestMintGetNeverMints(t *testing.T) {
	r, store := mintStack(t, &fakeMinter{title: "T", slug: "lp-ctx", token: "TOK-SECRET"}, "tian@example.com", nil)
	// Consent GET, repeatedly (incl. with a stray challenge param): never
	// delivers or stores a token — the GET has no mint path at all.
	for _, extra := range []map[string]string{{"state": "a"}, {"state": "b"}, {"consent_challenge": "x.y"}} {
		rec := doMint(r, mintQuery(extra))
		if strings.Contains(rec.Body.String(), "TOK-SECRET") {
			t.Fatalf("%v: consent GET leaked a token", extra)
		}
	}
	if tok, _ := store.Take(context.Background(), "panel", "a"); tok != "" {
		t.Fatal("a GET minted into the store")
	}
	// A forged challenge POSTed to completion is rejected (no mint).
	rec := doPost(r, "/embed/panel/token", url.Values{"auth_secret": {mintSurfSc}, "consent_challenge": {"forged.deadbeef"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forged challenge completion: got %d, want 403", rec.Code)
	}
}

// The full handshake: consent GET → Continue POST (challenge) → mint into the
// store → the app polls and receives it once.
func TestMintCompletionAndPoll(t *testing.T) {
	r, store := mintStack(t, &fakeMinter{title: "LP Context", slug: "lp-ctx", token: "TOK-XYZ"}, "tian@example.com", nil)

	done := completeConsent(t, r, "panel")
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), `"ok":true`) {
		t.Fatalf("completion POST should return ok; got %d: %s", done.Code, done.Body.String())
	}
	if strings.Contains(done.Body.String(), "TOK-XYZ") {
		t.Fatal("completion response must NOT contain the token (delivery is via poll)")
	}
	if tok, _ := store.Take(context.Background(), "panel", "n0nce"); tok != "TOK-XYZ" {
		t.Fatalf("mint must store the token for (surface,state); got %q", tok)
	}

	// Re-run to repopulate, then exercise the poll endpoint end to end.
	completeConsent(t, r, "panel")
	pollURL := "/embed/panel/token?auth_secret=" + mintSurfSc + "&state=n0nce"
	rec := doMint(r, pollURL)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"token":"TOK-XYZ"`) {
		t.Fatalf("poll must return the token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doMint(r, pollURL); strings.Contains(rec.Body.String(), "TOK-XYZ") {
		t.Fatal("poll must be single-use")
	}
}

func TestPollGates(t *testing.T) {
	r, store := mintStack(t, &fakeMinter{}, "tian@example.com", nil)
	_ = store.Put(context.Background(), "panel", "n0nce", "T", "e@example.com", time.Minute)
	cases := []struct {
		name, target string
		want         int
	}{
		{"wrong secret", "/embed/panel/token?auth_secret=WRONG&state=n0nce", http.StatusForbidden},
		{"unknown surface", "/embed/nope/token?auth_secret=US&state=n0nce", http.StatusNotFound},
		{"service surface", "/embed/svc/token?auth_secret=S&state=n0nce", http.StatusNotFound},
		{"missing state", "/embed/panel/token?auth_secret=US", http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := doMint(r, tc.target); rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	// Not-yet-consented: ready:false, 200.
	rec := doMint(r, "/embed/panel/token?auth_secret=US&state=never")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ready":false`) {
		t.Fatalf("empty poll should be 200 ready:false; got %d %s", rec.Code, rec.Body.String())
	}
}

// A challenge is bound to its surface: it can't be redeemed on a different
// surface, and a forged/tampered challenge is rejected.
func TestMintChallengeBinding(t *testing.T) {
	r, store := mintStack(t, &fakeMinter{title: "T", slug: "lp-ctx", token: "TOK"}, "tian@example.com", nil)
	ch := consentChallenge(t, r, mintQuery(nil)) // issued for surface "panel"

	// Redeem the panel challenge against a different (service-mode) surface → 404.
	if rec := doPost(r, "/embed/svc/token", url.Values{"auth_secret": {"S"}, "consent_challenge": {ch}}); rec.Code != http.StatusNotFound {
		t.Fatalf("challenge redeemed on wrong surface: got %d, want 404", rec.Code)
	}
	// Tamper the challenge body → MAC fails → 403.
	tampered := "eyJlIjoiZXZpbEBleGFtcGxlLmNvbSJ9." + strings.SplitN(ch, ".", 2)[1]
	if rec := doPost(r, "/embed/panel/token", url.Values{"auth_secret": {mintSurfSc}, "consent_challenge": {tampered}}); rec.Code != http.StatusForbidden {
		t.Fatalf("tampered challenge: got %d, want 403", rec.Code)
	}
	if tok, _ := store.Take(context.Background(), "panel", "n0nce"); tok != "" {
		t.Fatal("a bad challenge minted a token")
	}
}

func TestMintPagesEscape(t *testing.T) {
	r, _ := mintStack(t, &fakeMinter{title: "<b>T</b>", slug: "lp-ctx", token: "TOK"}, "tian@example.com", nil)
	rec := doMint(r, mintQuery(nil))
	if strings.Contains(rec.Body.String(), "<b>T</b>") {
		t.Fatal("consent page reflects raw app title — must be HTML-escaped")
	}
}

func TestMountMintGuards(t *testing.T) {
	svcOnly := map[string]Surface{
		"svc": {Secret: "S", Origin: OriginList{"https://dash.x"}, Email: "svc@example.com", SlugAllow: []string{"*"}},
	}
	// No user-mode surface → no mint route.
	r := chi.NewRouter()
	New(svcOnly, &fakeArt{}, fakeTok{}).MountMint(r, MintConfig{
		Identify: func(*http.Request) string { return "tian@example.com" }, Minter: &fakeMinter{},
		ChallengeKey: []byte(mintKey), Store: newFakeStore(),
	})
	if rec := doMint(r, mintQuery(nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("no user surface → no mint route; got %d", rec.Code)
	}
	// Weak challenge key → not mounted.
	r2 := chi.NewRouter()
	New(mintSurfaces(), &fakeArt{}, fakeTok{}).MountMint(r2, MintConfig{
		Identify: func(*http.Request) string { return "tian@example.com" }, Minter: &fakeMinter{},
		ChallengeKey: []byte("short"), Store: newFakeStore(),
	})
	if rec := doMint(r2, mintQuery(nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("weak challenge key → no mint route; got %d", rec.Code)
	}
	// No store → not mounted.
	r3 := chi.NewRouter()
	New(mintSurfaces(), &fakeArt{}, fakeTok{}).MountMint(r3, MintConfig{
		Identify: func(*http.Request) string { return "tian@example.com" }, Minter: &fakeMinter{},
		ChallengeKey: []byte(mintKey),
	})
	if rec := doMint(r3, mintQuery(nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("no store → no mint route; got %d", rec.Code)
	}
}
