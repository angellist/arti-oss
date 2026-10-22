package dictate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
)

var browserSession = auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie}

type seen struct {
	auth, model, language, filename string
	hasLanguage                     bool
}

func provider(t *testing.T, status int, reply string, got *seen) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(MaxBytes); err != nil {
			t.Fatalf("provider got a non-multipart body: %v", err)
		}
		got.auth = r.Header.Get("Authorization")
		got.model = r.FormValue("model")
		_, got.hasLanguage = r.MultipartForm.Value["language"]
		got.language = r.FormValue("language")
		if _, fh, err := r.FormFile("file"); err == nil {
			got.filename = fh.Filename
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func post(h http.Handler, ct string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/dictate", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithTestCredential(req.Context(), browserSession))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNoKeyIsNotImplemented(t *testing.T) {
	rec := post(&Handler{}, "audio/webm", []byte("clip"))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestForwardsClipAndReturnsTrimmedText(t *testing.T) {
	var got seen
	p := provider(t, 200, `{"text":"  rebase this branch \n"}`, &got)
	rec := post(&Handler{Key: "sk-test", Endpoint: p.URL}, "audio/mp4;codecs=mp4a.40.2", []byte("clip"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["text"] != "rebase this branch" {
		t.Errorf("text = %q", out["text"])
	}
	if got.auth != "Bearer sk-test" || got.model != model {
		t.Errorf("auth=%q model=%q", got.auth, got.model)
	}
	// Safari records mp4; sent under a .webm name the provider fails to decode it.
	if got.filename != "clip.mp4" {
		t.Errorf("filename = %q, want clip.mp4", got.filename)
	}
	if got.hasLanguage {
		t.Errorf("language sent with none configured; the model must detect it")
	}
}

func TestConfiguredLanguageReachesProvider(t *testing.T) {
	var got seen
	p := provider(t, 200, `{"text":"hi"}`, &got)
	post(&Handler{Key: "k", Language: "en", Endpoint: p.URL}, "audio/webm", []byte("clip"))
	if got.language != "en" {
		t.Errorf("language = %q, want en", got.language)
	}
}

func TestProviderAuthFailureIsNotPassedThrough(t *testing.T) {
	var got seen
	p := provider(t, 401, `{"error":"Incorrect API key provided: provider-secret-marker"}`, &got)
	rec := post(&Handler{Key: "k", Endpoint: p.URL}, "audio/webm", []byte("clip"))
	// A 401 would tell the browser ITS session expired.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "provider-secret-marker") {
		t.Fatalf("provider body reached the browser: %s", rec.Body)
	}
}

func TestRejectsBadBodies(t *testing.T) {
	h := &Handler{Key: "k", Endpoint: "http://127.0.0.1:1"}
	for _, tc := range []struct {
		name string
		ct   string
		body []byte
		want int
	}{
		{"not audio", "application/json", []byte("{}"), http.StatusUnsupportedMediaType},
		{"empty", "audio/webm", nil, http.StatusBadRequest},
		{"over 25 MB", "audio/webm", bytes.Repeat([]byte("a"), MaxBytes+1), http.StatusRequestEntityTooLarge},
	} {
		if rec := post(h, tc.ct, tc.body); rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestAudioExt(t *testing.T) {
	for in, want := range map[string]string{
		"audio/webm;codecs=opus": "webm", "audio/mp4": "mp4", "audio/ogg": "ogg",
		"audio/wav": "wav", "audio/mpeg": "mp3", "": "webm",
	} {
		if got := AudioExt(strings.Split(in, ";")[0]); got != want {
			t.Errorf("AudioExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOnlyABrowserSessionMaySpendTheKey(t *testing.T) {
	var got seen
	p := provider(t, 200, `{"text":"x"}`, &got)
	h := &Handler{Key: "sk-test", Endpoint: p.URL}
	for _, cred := range []auth.Credential{
		{Kind: auth.CredKindAPIKey, Source: auth.CredSourceBearer},
		{Kind: auth.CredKindToken, Source: auth.CredSourceBearer},
		{Kind: auth.CredKindToken, Source: auth.CredSourceCookie},
		{},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/dictate", bytes.NewReader([]byte("clip")))
		req.Header.Set("Content-Type", "audio/webm")
		req = req.WithContext(auth.WithTestCredential(req.Context(), cred))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%+v: status = %d, want 403", cred, rec.Code)
		}
	}
	if got.auth != "" {
		t.Fatal("a non-browser credential reached the provider")
	}
}
