package opensearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The is_latest recompute must survive concurrent writers: a version conflict
// on one doc used to abort the whole clear-then-set sequence, leaving MORE
// than one version flagged latest — so search could serve a stale version as
// "latest". These tests pin the conflict-tolerant one-shot shape and the
// conflict count the retry loop keys on.
func TestSetLatestFlag(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	respond := func(conflicts int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.RequestURI()
			gotBody = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"version_conflicts": conflicts, "updated": 1})
		}
	}

	t.Run("one conflict-tolerant update_by_query", func(t *testing.T) {
		srv := httptest.NewServer(respond(0))
		defer srv.Close()
		c := New(Config{Endpoint: srv.URL}, nil)

		n, err := c.SetLatestFlag(context.Background(), "my-slug", "abc-123")
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("conflicts = %d, want 0", n)
		}
		// conflicts=proceed is the load-bearing part: without it one racing
		// doc aborts the recompute for every other version of the slug.
		if !strings.Contains(gotPath, "_update_by_query") || !strings.Contains(gotPath, "conflicts=proceed") {
			t.Errorf("path = %q, want _update_by_query with conflicts=proceed", gotPath)
		}
		script := gotBody["script"].(map[string]any)
		if params := script["params"].(map[string]any); params["latest"] != "abc-123" {
			t.Errorf("script params = %v, want latest=abc-123", params)
		}
		// The flag write and the flag clear must be ONE request — the old
		// two-step left a window with zero or two latest docs.
		src := script["source"].(string)
		if !strings.Contains(src, "params.latest") || !strings.Contains(src, "noop") {
			t.Errorf("script source missing latest comparison or noop: %q", src)
		}
	})

	t.Run("archived slug clears every flag", func(t *testing.T) {
		srv := httptest.NewServer(respond(0))
		defer srv.Close()
		c := New(Config{Endpoint: srv.URL}, nil)

		if _, err := c.SetLatestFlag(context.Background(), "gone-slug", ""); err != nil {
			t.Fatal(err)
		}
		// latestID "" matches no _id, so the same script clears all docs.
		params := gotBody["script"].(map[string]any)["params"].(map[string]any)
		if params["latest"] != "" {
			t.Errorf("params.latest = %v, want empty", params["latest"])
		}
	})

	t.Run("version conflicts are reported, not fatal", func(t *testing.T) {
		srv := httptest.NewServer(respond(3))
		defer srv.Close()
		c := New(Config{Endpoint: srv.URL}, nil)

		n, err := c.SetLatestFlag(context.Background(), "hot-slug", "abc-123")
		if err != nil {
			t.Fatalf("conflicts with conflicts=proceed must not error: %v", err)
		}
		if n != 3 {
			t.Errorf("conflicts = %d, want 3 (drives the caller's retry)", n)
		}
	})

	t.Run("http error propagates", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		}))
		defer srv.Close()
		c := New(Config{Endpoint: srv.URL}, nil)

		if _, err := c.SetLatestFlag(context.Background(), "s", "x"); err == nil {
			t.Fatal("want error on HTTP 500")
		}
	})
}
