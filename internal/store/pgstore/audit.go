package pgstore

import (
	"context"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/audit"
)

// NoteAccess records an access decision on row in the request's audit entry.
func NoteAccess(ctx context.Context, row sqlc.Artifact, decision string) {
	e := audit.From(ctx)
	if e == nil {
		return
	}
	t := audit.Target{ArtifactID: UUIDFromPG(row.ArtifactID).String(), Version: row.Version, Decision: decision}
	if row.NamedSlug != nil {
		t.Slug = *row.NamedSlug
	}
	e.AddTarget(t)
}
