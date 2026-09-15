package artifacts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

func TestViewStatsDTO_JSONPreservesUnauthorizedRecent(t *testing.T) {
	base := ViewStatsDTO{
		ViewKey: "demo", Total: 2, Last7d: 1, Last30d: 2,
		UniqueViewers: 1, CanSeeViewers: false,
	}
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var got ViewStatsDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Recent != nil {
		t.Fatalf("unauthorized recent = %#v, want nil", got.Recent)
	}

	base.CanSeeViewers = true
	base.Recent = []ViewRowDTO{{Viewer: "a@x.com", Surface: "viewer", At: time.Unix(1, 0).UTC()}}
	b, err = json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Recent == nil || len(got.Recent) != 1 {
		t.Fatalf("authorized recent = %#v, want one row", got.Recent)
	}
}

func TestViewKeyOf(t *testing.T) {
	slug := "same-doc"
	id := uuid.New()
	row := sqlc.Artifact{
		ArtifactID: pgtype.UUID{Bytes: id, Valid: true},
		NamedSlug:  &slug,
	}
	if got := viewKeyOf(row); got != slug {
		t.Fatalf("slug key = %q, want %q", got, slug)
	}
	row.NamedSlug = nil
	if got := viewKeyOf(row); got == "" {
		t.Fatal("slugless key is empty")
	}
	empty := ""
	row.NamedSlug = &empty
	if got := viewKeyOf(row); got != id.String() {
		t.Fatalf("empty slug key = %q, want UUID %q", got, id)
	}
}
