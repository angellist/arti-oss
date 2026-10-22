package embed

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

const hostKey = "host-key-0123456789abcdef0123456789"

func hostStack(t *testing.T, minter UserTokenMinter, mutate func(map[string]Surface)) chi.Router {
	t.Helper()
	surfs := mintSurfaces()
	p := surfs["panel"]
	p.HostKey = hostKey
	surfs["panel"] = p
	surfs["nohost"] = Surface{Secret: "S2", Origin: OriginList{"https://x"}, Identity: "user", SlugAllow: []string{"*"}}
	if mutate != nil {
		mutate(surfs)
	}
	r := chi.NewRouter()
	s := New(surfs, &fakeArt{}, fakeTok{})
	s.Mount(r)
	s.MountMint(r, MintConfig{
		Identify:     func(*http.Request) string { return "" },
		Minter:       minter,
		ChallengeKey: []byte(mintKey),
		Store:        newFakeStore(),
	})
	return r
}

func postHost(r chi.Router, surface, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/embed/"+surface+"/host-token", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestHostTokenMintsPinnedToLatestVersion(t *testing.T) {
	m := &fakeMinter{aid: "uuid-v31", version: 31, token: "tok"}
	rr := postHost(hostStack(t, m, nil), "panel", hostKey, `{"slug":"lp-thread-panel","email":" Ann@Example.com "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var got struct {
		Token   string `json:"token"`
		AppID   string `json:"app_id"`
		Version int32  `json:"version"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Token != "tok" || got.AppID != "uuid-v31" || got.Version != 31 {
		t.Fatalf("want tok@31, got %+v", got)
	}
	if m.hostSlug != "lp-thread-panel" || m.hostEmail != "Ann@Example.com" {
		t.Fatalf("resolved %q for %q", m.hostSlug, m.hostEmail)
	}
	// A browser page must never be able to read this answer.
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("host-token must not send CORS headers")
	}
}

func TestHostTokenGates(t *testing.T) {
	ok := `{"slug":"lp-thread-panel","email":"ann@example.com"}`
	cases := []struct {
		name, surface, key, body string
		minter                   *fakeMinter
		want                     int
	}{
		{"surface without host_key is absent", "nohost", hostKey, ok, &fakeMinter{}, 404},
		{"service surface is absent", "svc", hostKey, ok, &fakeMinter{}, 404},
		{"no key", "panel", "", ok, &fakeMinter{}, 401},
		// The client-visible surface secret must not work as the host key.
		{"surface secret is not the host key", "panel", mintSurfSc, ok, &fakeMinter{}, 401},
		{"slug outside slug_allow", "panel", hostKey, `{"slug":"other","email":"ann@example.com"}`, &fakeMinter{}, 403},
		{"email outside the domain allowlist", "panel", hostKey, `{"slug":"lp-x","email":"eve@evil.test"}`, &fakeMinter{}, 403},
		{"viewer cannot read the app", "panel", hostKey, ok, &fakeMinter{appErr: errors.New("nope")}, 404},
		{"missing email", "panel", hostKey, `{"slug":"lp-x"}`, &fakeMinter{}, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rr := postHost(hostStack(t, c.minter, nil), c.surface, c.key, c.body); rr.Code != c.want {
				t.Fatalf("want %d, got %d: %s", c.want, rr.Code, rr.Body)
			}
		})
	}
}

func TestParseSurfacesHostKey(t *testing.T) {
	cases := []struct{ name, json, wantErr string }{
		{"user surface accepts a host key",
			`{"u":{"secret":"S","origin":"https://x","slug_allow":["*"],"identity":"user","host_key":"` + hostKey + `"}}`, ""},
		{"service surface rejects a host key",
			`{"s":{"secret":"S","origin":"https://x","slug_allow":["*"],"email":"a@b.c","host_key":"` + hostKey + `"}}`, "only valid with identity"},
		{"short host key",
			`{"u":{"secret":"S","origin":"https://x","slug_allow":["*"],"identity":"user","host_key":"short"}}`, "at least 32"},
		{"host key equal to the client-visible secret",
			`{"u":{"secret":"` + hostKey + `","origin":"https://x","slug_allow":["*"],"identity":"user","host_key":"` + hostKey + `"}}`, "must differ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSurfaces(c.json)
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}
