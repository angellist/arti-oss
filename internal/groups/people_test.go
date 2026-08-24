//go:build integration

package groups_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The typeahead's whole value is that it finds a colleague from a few
// characters, and its whole risk is that the same endpoint could be walked for
// the workspace's address book. These tests pin both halves.
func TestSearchPeople(t *testing.T) {
	r, st := testRouter(t)
	ctx := context.Background()

	// A login is the broadest signal arti has that someone exists — it is the
	// only one that fires for a person who has never created an artifact.
	tag := unique("pplsearch")
	email := tag + "@example.com"
	if err := st.UpsertIdPGroups(ctx, email, []string{"eng"}); err != nil {
		t.Fatal(err)
	}
	// A glob member of a group is a pattern, not a person. Offering it as a
	// suggestion would let one keystroke widen a grant to an entire domain.
	if _, err := st.CreateGroup(ctx, unique("g"), "", []string{"*@example.com"}, "owner@example.com"); err != nil {
		t.Fatal(err)
	}

	got := searchPeople(t, r, tag)
	if !contains(got, email) {
		t.Errorf("search %q did not offer %s (got %v)", tag, email, got)
	}
	for _, e := range got {
		if e == "*@example.com" {
			t.Errorf("glob offered as a person: %v", got)
		}
	}

	// The floor is what keeps this a typeahead rather than a directory dump:
	// there is no query shape that returns the workspace.
	if short := searchPeople(t, r, "a"); len(short) != 0 {
		t.Errorf("single-character query returned %d rows, want 0", len(short))
	}
	if empty := searchPeople(t, r, ""); len(empty) != 0 {
		t.Errorf("empty query returned %d rows, want 0", len(empty))
	}

	// A LIKE metacharacter must be matched literally, or `%` alone would mean
	// "everyone" and walk straight past the two-character floor.
	if wild := searchPeople(t, r, "%%"); len(wild) != 0 {
		t.Errorf("wildcard query returned %d rows, want 0", len(wild))
	}
}

func searchPeople(t *testing.T, r *chi.Mux, q string) []string {
	t.Helper()
	w := do(t, r, "alice@example.com", http.MethodGet, "/api/people?q="+url.QueryEscape(q), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		People []struct {
			Email string `json:"email"`
		} `json:"people"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(resp.People))
	for _, p := range resp.People {
		out = append(out, p.Email)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
