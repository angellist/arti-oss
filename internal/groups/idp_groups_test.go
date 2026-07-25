//go:build integration

package groups_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/groups"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// GET /api/idp-groups returns the distinct captured IdP group names with member
// counts and their idp:<name> tokens — the typeahead source for granting SSO
// groups. It never exposes rosters.
func TestListIdPGroupsEndpoint(t *testing.T) {
	url := os.Getenv("ARTI_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{IdPGroupsMaxAge: time.Hour})
	r := chi.NewRouter()
	groups.NewService(st).Mount(r)

	tag := unique("idpend")
	if err := st.UpsertIdPGroups(context.Background(), tag+"@example.com", []string{tag}); err != nil {
		t.Fatal(err)
	}

	w := do(t, r, "alice@example.com", http.MethodGet, "/api/idp-groups", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		IdPGroups []groups.IdPGroupDTO `json:"idp_groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	var found *groups.IdPGroupDTO
	for i := range resp.IdPGroups {
		if resp.IdPGroups[i].Name == tag {
			found = &resp.IdPGroups[i]
		}
	}
	if found == nil {
		t.Fatalf("group %q not in response %+v", tag, resp.IdPGroups)
	}
	if found.Token != "idp:"+tag {
		t.Errorf("token = %q, want idp:%s", found.Token, tag)
	}
	if found.MemberCount != 1 {
		t.Errorf("member_count = %d, want 1", found.MemberCount)
	}
}
