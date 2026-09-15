package opensearch

import (
	"context"
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func siblingRow() sqlc.Artifact {
	slug := "al-service-graph-explorer"
	return sqlc.Artifact{
		ArtifactType: pgstore.TypePackage, // no store needed on this path
		Title:        "AngelList service graph explorer",
		NamedSlug:    &slug,
		ContentType:  "application/zip",
		Creator:      "alice@example.com",
	}
}

// is_latest is the only thing a latest-per-slug search filters on, and every
// index write replaces the whole document. A caller re-indexing an older
// version (the slug-wide ACL fan-out) must be able to write the flag off, or
// the whole version history answers the search.
func TestIndexVersion_NotLatestClearsFlag(t *testing.T) {
	c, got := indexCapture(t)
	ix := NewIndexer(c, nil, nil)

	ix.IndexVersion(context.Background(), siblingRow(), false)

	if (*got)["is_latest"] != false {
		t.Errorf("is_latest = %v, want false", (*got)["is_latest"])
	}
}

func TestIndexArtifact_FlagsLatest(t *testing.T) {
	c, got := indexCapture(t)
	ix := NewIndexer(c, nil, nil)

	ix.IndexArtifact(context.Background(), siblingRow())

	if (*got)["is_latest"] != true {
		t.Errorf("is_latest = %v, want true", (*got)["is_latest"])
	}
}
