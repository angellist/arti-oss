package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeArti serves the two routes `arti access` needs — slug→meta resolution
// and the PATCH — recording the PATCH body (nil until a PATCH happens).
func fakeArti(t *testing.T, meta map[string]any) (*httptest.Server, func() map[string]json.RawMessage) {
	t.Helper()
	var patched map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/artifacts/"):
			_ = json.NewDecoder(r.Body).Decode(&patched)
			_ = json.NewEncoder(w).Encode(meta)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/artifacts/by-slug/"):
			_ = json.NewEncoder(w).Encode(meta)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/meta"):
			_ = json.NewEncoder(w).Encode(meta)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/artifacts/"):
			// The real server streams CONTENT here, not metadata — a client
			// that reads this route as JSON metadata is broken.
			_, _ = w.Write([]byte("# markdown body, not JSON"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/append"):
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			patched = body
			_ = json.NewEncoder(w).Encode(meta)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]json.RawMessage { return patched }
}

var testMeta = map[string]any{
	"artifact_id":    "11111111-2222-3333-4444-555555555555",
	"named_slug":     "gated-doc",
	"version":        3,
	"allowed_access": []string{"*@example.com"},
	"allowed_write":  nil,
}

// `arti access <slug> --access … --write-access …` PATCHes the resolved
// artifact with both fields — the slug-wide edit the web modal and MCP have
// had since arti#230 but the CLI lacked entirely.
func TestAccessCmdPatchesACL(t *testing.T) {
	srv, patched := fakeArti(t, testMeta)
	cmd := &AccessCmd{
		Ident:       "gated-doc",
		Access:      []string{"thibaut@example.com", "group:eng"},
		WriteAccess: []string{"group:eng"},
	}
	if err := cmd.Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("access: %v", err)
	}
	body := patched()
	if body == nil {
		t.Fatal("no PATCH reached the server")
	}
	var access, write []string
	if err := json.Unmarshal(body["allowed_access"], &access); err != nil || len(access) != 2 {
		t.Fatalf("allowed_access = %v (err=%v), want 2 principals", access, err)
	}
	if err := json.Unmarshal(body["allowed_write"], &write); err != nil || len(write) != 1 {
		t.Fatalf("allowed_write = %v (err=%v), want [group:eng]", write, err)
	}
}

// --private / --write-private send explicit empty arrays (creator-only) —
// the shape the repeatable flags can't express.
func TestAccessCmdPrivate(t *testing.T) {
	srv, patched := fakeArti(t, testMeta)
	cmd := &AccessCmd{Ident: "gated-doc", Private: true, WritePrivate: true}
	if err := cmd.Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("access --private: %v", err)
	}
	body := patched()
	var access, write []string
	if err := json.Unmarshal(body["allowed_access"], &access); err != nil || access == nil || len(access) != 0 {
		t.Fatalf("allowed_access = %v (err=%v), want explicit []", access, err)
	}
	if err := json.Unmarshal(body["allowed_write"], &write); err != nil || write == nil || len(write) != 0 {
		t.Fatalf("allowed_write = %v (err=%v), want explicit []", write, err)
	}
}

// A field the caller didn't set must be ABSENT from the PATCH (arti treats
// absent as untouched); and with no flags at all the command only shows the
// current ACL — no PATCH.
func TestAccessCmdPartialAndShow(t *testing.T) {
	srv, patched := fakeArti(t, testMeta)
	if err := (&AccessCmd{Ident: "gated-doc", Access: []string{"*"}}).Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("access: %v", err)
	}
	if body := patched(); body == nil {
		t.Fatal("no PATCH reached the server")
	} else if _, ok := body["allowed_write"]; ok {
		t.Fatalf("allowed_write must be absent when no write flag was passed, got %s", body["allowed_write"])
	}

	srv2, patched2 := fakeArti(t, testMeta)
	if err := (&AccessCmd{Ident: "gated-doc"}).Run(&CLI{BaseURL: srv2.URL}); err != nil {
		t.Fatalf("access (show): %v", err)
	}
	if patched2() != nil {
		t.Fatal("flag-less `arti access` must be read-only, but a write reached the server")
	}
}

// Show mode addressed by UUID must read the /meta route — the bare
// /api/artifacts/{id} route streams the artifact CONTENT.
func TestAccessCmdShowByUUID(t *testing.T) {
	srv, patched := fakeArti(t, testMeta)
	if err := (&AccessCmd{Ident: "11111111-2222-3333-4444-555555555555"}).Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("access show by uuid: %v", err)
	}
	if patched() != nil {
		t.Fatal("show mode must not write")
	}
}

func TestAccessCmdMutualExclusion(t *testing.T) {
	srv, _ := fakeArti(t, testMeta)
	if err := (&AccessCmd{Ident: "x", Private: true, Access: []string{"*"}}).Run(&CLI{BaseURL: srv.URL}); err == nil {
		t.Fatal("--private with --access must error")
	}
	if err := (&AccessCmd{Ident: "x", WritePrivate: true, WriteAccess: []string{"a@x"}}).Run(&CLI{BaseURL: srv.URL}); err == nil {
		t.Fatal("--write-private with --write-access must error")
	}
}

// append gains the same write-side flags add has: --write-access,
// --write-private, and --private for reads.
func TestAppendCmdACLFlags(t *testing.T) {
	srv, sent := fakeArti(t, testMeta)
	f := filepath.Join(t.TempDir(), "entry.md")
	if err := os.WriteFile(f, []byte("entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &AppendCmd{File: f, Slug: "gated-doc", Private: true, WritePrivate: true}
	if err := cmd.Run(&CLI{BaseURL: srv.URL}); err != nil {
		t.Fatalf("append: %v", err)
	}
	body := sent()
	var access, write []string
	if err := json.Unmarshal(body["allowed_access"], &access); err != nil || access == nil || len(access) != 0 {
		t.Fatalf("allowed_access = %v (err=%v), want explicit [] from --private", access, err)
	}
	if err := json.Unmarshal(body["allowed_write"], &write); err != nil || write == nil || len(write) != 0 {
		t.Fatalf("allowed_write = %v (err=%v), want explicit [] from --write-private", write, err)
	}
}
