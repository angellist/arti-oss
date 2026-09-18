//go:build integration

package apps_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/apps"
	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// newAppRouter mounts the /app document routes as cmd_serve wires them,
// with every request authenticated as email.
func newAppRouter(t *testing.T, email string) (*chi.Mux, *pgstore.Store, *apps.Service) {
	t.Helper()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	appsSvc := apps.New(st, signer, map[string]apps.ServerConfig{}, &recordingProvider{}, nil, nil)
	svc.SetAppTokenFn(appsSvc.SignAppToken)
	svc.SetAppTokenVerifyFn(appsSvc.VerifyEmbedToken)
	svc.SetAppConsentFns(appsSvc.SignAppConsent, appsSvc.VerifyAppConsent, false)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.Disabled(email))
		artifacts.MountApp(r, svc)
		artifacts.Mount(r, svc)
	})
	appsSvc.MountProxy(r)
	return r, st, appsSvc
}

func getWithCookies(h http.Handler, url string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// Opening an APP for the first time yields the consent page and no bridge;
// consenting sets one cookie; the same URL then serves the app with the
// bridge; the record is per viewer and per artifact version.
func TestAppRunConsentFlow(t *testing.T) {
	r, st, appsSvc := newAppRouter(t, "alice@example.com")
	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "add_comment"}})

	for _, url := range []string{"/app/" + appID, "/api/artifacts/" + appID + "/files/index.html"} {
		rr := getWithCookies(r, url, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d body %s", url, rr.Code, rr.Body.String())
		}
		if b := rr.Body.String(); strings.Contains(b, "__ARTI_APP__") || !strings.Contains(b, "run this app as me") {
			t.Fatalf("%s: first open must be the consent page, got:\n%s", url, b)
		}
		if csp := rr.Header().Get("Content-Security-Policy"); strings.Contains(csp, "sandbox") || !strings.Contains(csp, "frame-ancestors ") {
			t.Fatalf("%s: consent page must carry frame-ancestors and no sandbox, got %q", url, csp)
		}
		if xfo := rr.Header().Get("X-Frame-Options"); xfo != "" {
			t.Fatalf("%s: consent page must drop X-Frame-Options so an embedded viewer can frame it, got %q", url, xfo)
		}
	}

	// A fetch (no `next`) gets 204 + the cookie.
	req := httptest.NewRequest(http.MethodPost, "/app/"+appID+"/consent", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("consent status %d body %s", rr.Code, rr.Body.String())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "arti_app_ok_"+appID || !cookies[0].HttpOnly {
		t.Fatalf("consent cookie: %+v", cookies)
	}

	// A form post (with `next`) redirects back to the document.
	req = httptest.NewRequest(http.MethodPost, "/app/"+appID+"/consent", strings.NewReader("next=%2Fapp%2F"+appID+"%3Fq%3D1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/app/"+appID+"?q=1" {
		t.Fatalf("form consent: %d %q", rr.Code, rr.Header().Get("Location"))
	}

	for _, url := range []string{"/app/" + appID, "/api/artifacts/" + appID + "/files/index.html"} {
		rr := getWithCookies(r, url, cookies)
		if b := rr.Body.String(); rr.Code != http.StatusOK || !strings.Contains(b, "__ARTI_APP__") {
			t.Fatalf("%s after consent: %d\n%s", url, rr.Code, b)
		}
		if !strings.Contains(rr.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
			t.Fatalf("%s after consent: app must be sandboxed", url)
		}
	}

	// Another viewer presenting alice's cookie is still asked.
	r2, _, _ := newAppRouter(t, "bob@example.com")
	if rr := getWithCookies(r2, "/app/"+appID, cookies); strings.Contains(rr.Body.String(), "__ARTI_APP__") {
		t.Fatal("bob must not run the app on alice's consent")
	}
	// A new version is a new artifact id, so it is asked again.
	appID2 := newAppArtifact(t, st, nil)
	if rr := getWithCookies(r, "/app/"+appID2, cookies); strings.Contains(rr.Body.String(), "__ARTI_APP__") {
		t.Fatal("a different version must not inherit consent")
	}

	// The consent record is not a bridge token, and a bridge token is not a
	// consent record.
	if rr := proxyPost(t, r, cookies[0].Value, appID, "arti", "add_comment", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("proxy accepted the consent cookie as a bearer: %d %s", rr.Code, rr.Body.String())
	}
	if _, _, err := appsSvc.VerifyAppConsent(mustToken(t, appsSvc, "alice@example.com", appID)); err == nil {
		t.Fatal("a bridge token must not verify as consent")
	}
	if _, _, err := appsSvc.VerifyEmbedToken(cookies[0].Value); err == nil {
		t.Fatal("a consent record must not verify as a files token")
	}
}
