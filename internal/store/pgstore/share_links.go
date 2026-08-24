package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// Share links: the store half of the external-timed-share-link capability.
// See db/migrations/0022_share_links.sql for what a row means and why the
// token itself is never stored.

// ShareLinkInput is one mint. Exactly one of ArtifactID (a PINNED link, frozen
// to that version) and Slug (a TRACKING link, following the slug's newest
// version) may be set; the share_links_target_exactly_one CHECK enforces it in
// the database as well, because the distinction decides what a recipient sees.
type ShareLinkInput struct {
	TokenHash   []byte
	TokenPrefix string
	ArtifactID  *uuid.UUID
	Slug        *string
	CreatedBy   string
	Note        string
	ExpiresAt   time.Time
	// AnchorOwner pins a slug-scoped link to the lineage that owned the slug
	// when it was minted. Empty for pinned links, which name an artifact_id
	// and cannot follow a slug anywhere.
	AnchorOwner string
}

func (s *Store) CreateShareLink(ctx context.Context, in ShareLinkInput) (sqlc.ShareLink, error) {
	var aid pgtype.UUID
	if in.ArtifactID != nil {
		aid = pgUUID(*in.ArtifactID)
	}
	return s.q.CreateShareLink(ctx, sqlc.CreateShareLinkParams{
		TokenHash:   in.TokenHash,
		TokenPrefix: in.TokenPrefix,
		ArtifactID:  aid,
		Slug:        in.Slug,
		CreatedBy:   in.CreatedBy,
		Note:        in.Note,
		ExpiresAt:   pgtype.Timestamptz{Time: in.ExpiresAt, Valid: true},
		AnchorOwner: in.AnchorOwner,
	})
}

// GetShareLinkByHash looks a link up by sha256(token). The digest IS the key,
// so there is no secret-dependent comparison to leak. Returns the row whatever
// its state — the caller decides on revoked_at / expires_at, in an order that
// is itself a security property (see Service.ResolveShare).
func (s *Store) GetShareLinkByHash(ctx context.Context, hash []byte) (sqlc.ShareLink, error) {
	row, err := s.q.GetShareLinkByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ShareLink{}, ErrNotFound
	}
	return row, err
}

func (s *Store) GetShareLink(ctx context.Context, id pgtype.UUID) (sqlc.ShareLink, error) {
	row, err := s.q.GetShareLink(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ShareLink{}, ErrNotFound
	}
	return row, err
}

// ListShareLinksForDoc returns every link for a document, across the slug's
// whole version lineage. Pass slug=nil for a slugless artifact.
func (s *Store) ListShareLinksForDoc(ctx context.Context, artifactID uuid.UUID, slug *string) ([]sqlc.ShareLink, error) {
	return s.q.ListShareLinksForDoc(ctx, sqlc.ListShareLinksForDocParams{
		Slug:       slug,
		ArtifactID: pgUUID(artifactID),
	})
}

func (s *Store) RevokeShareLink(ctx context.Context, id pgtype.UUID, by string) error {
	return s.q.RevokeShareLink(ctx, sqlc.RevokeShareLinkParams{ID: id, RevokedBy: &by})
}

// RecordShareLinkOpen appends one open and bumps the denormalised counters.
// Callers treat a failure here as non-fatal: a document that cannot be read
// because its audit row failed to write is a worse outcome than a missing
// audit row.
func (s *Store) RecordShareLinkOpen(ctx context.Context, id pgtype.UUID, ip, peerAddr, ua string) error {
	if err := s.q.RecordShareLinkOpen(ctx, sqlc.RecordShareLinkOpenParams{
		LinkID: id, Ip: ip, PeerAddr: peerAddr, UserAgent: ua,
	}); err != nil {
		return err
	}
	return s.q.BumpShareLinkOpened(ctx, id)
}

func (s *Store) ListShareLinkOpens(ctx context.Context, id pgtype.UUID, limit int32) ([]sqlc.ShareLinkOpen, error) {
	return s.q.ListShareLinkOpens(ctx, sqlc.ListShareLinkOpensParams{LinkID: id, Limit: limit})
}

// SlugLiveCreator returns the creator of the slug's earliest NON-archived
// version — the owner of its live lineage. ErrNotFound when every version is
// archived, which callers must treat as "nobody currently holds this slug"
// rather than as an error.
func (s *Store) SlugLiveCreator(ctx context.Context, slug string) (string, error) {
	creator, err := s.q.SlugLiveCreator(ctx, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return creator, err
}

// GetLatestBySlugAnyState returns a slug's newest version REGARDLESS of
// archival. The share serve path needs this rather than GetBySlug(slug, nil):
// that filters deleted_at and walks back to an older version, so archiving the
// latest version would leave a tracking link quietly serving stale content
// instead of dying. The caller refuses an archived row itself.
func (s *Store) GetLatestBySlugAnyState(ctx context.Context, slug *string) (sqlc.Artifact, error) {
	row, err := s.q.GetLatestArtifactBySlugAnyState(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Artifact{}, ErrNotFound
	}
	return row, err
}
