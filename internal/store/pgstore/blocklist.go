package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// ─── the block list ──────────────────────────────────────────────────
//
// A block takes a document away from everyone, including admins, without
// touching the document. It is the stop-gap for "this must not be readable
// right now": an ACL edit rewrites state an owner may want back, archiving
// leaves the document readable, and neither can be aimed at a slug before it
// exists. Blocked rows are not returned by any read here, so every surface
// above — HTTP, MCP, apps, embeds, share links, comments — answers 404 on its
// own existing not-found path rather than growing a second denial code.
//
// Admins are blocked too. A kill switch with an exempt audience is not one,
// and lifting is a block-list edit, not a document read, so nothing an admin
// needs to undo this goes through the blocked path.
//
// Writes are refused as well (see Put): a slug that could still take a new
// version would come back from a block with whatever ACL the writer supplied.

// blockCacheTTL bounds how long a block takes to reach a replica that missed
// the invalidation notification. Same discipline as the rbac/groups snapshots:
// served only while the invalidation bus is healthy, reloaded from Postgres
// otherwise.
const blockCacheTTL = 15 * time.Second

// Block is one entry of the block list.
type Block struct {
	Pattern   string    `json:"pattern"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type blockCache struct {
	mu         sync.Mutex
	patterns   []string // lowercased globs, as stored
	loadedAt   time.Time
	generation uint64
}

func (c *blockCache) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.generation++
	c.mu.Unlock()
}

func (s *Store) blockSnap(ctx context.Context) ([]string, error) {
	for {
		s.blocks.mu.Lock()
		if s.authzCacheHealthy() && !s.blocks.loadedAt.IsZero() && time.Since(s.blocks.loadedAt) < blockCacheTTL {
			pats := s.blocks.patterns
			s.blocks.mu.Unlock()
			return pats, nil
		}
		generation := s.blocks.generation
		s.blocks.mu.Unlock()

		// Reload outside the lock, like rbacSnap, so a slow round-trip does
		// not serialize every concurrent read.
		rows, err := s.pool.Query(ctx, `SELECT pattern FROM artifact_blocks`)
		if err != nil {
			return nil, fmt.Errorf("pgstore: block list: %w", err)
		}
		pats := []string{}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, fmt.Errorf("pgstore: block list scan: %w", err)
			}
			pats = append(pats, strings.ToLower(p))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("pgstore: block list iter: %w", err)
		}

		s.blocks.mu.Lock()
		if generation != s.blocks.generation {
			s.blocks.mu.Unlock()
			continue
		}
		if s.authzCacheHealthy() {
			s.blocks.patterns, s.blocks.loadedAt = pats, time.Now()
		}
		s.blocks.mu.Unlock()
		return pats, nil
	}
}

// blockKey is what a pattern is matched against: the document's slug, or the
// artifact id when it has none. A slugless artifact has no name to aim a
// pattern at, and a slugged one is blocked by the slug so every version of it
// goes at once.
func blockKey(row sqlc.Artifact) string {
	if row.NamedSlug != nil && *row.NamedSlug != "" {
		return *row.NamedSlug
	}
	return uuidFrom(row.ArtifactID).String()
}

// Blocked reports whether this row is currently blocked.
func (s *Store) Blocked(ctx context.Context, row sqlc.Artifact) (bool, error) {
	return s.blockedKey(ctx, blockKey(row))
}

// BlockedSlug reports whether a slug is currently blocked — for the write
// paths, which decide before there is a row.
func (s *Store) BlockedSlug(ctx context.Context, slug string) (bool, error) {
	if slug == "" {
		return false, nil
	}
	return s.blockedKey(ctx, slug)
}

func (s *Store) blockedKey(ctx context.Context, key string) (bool, error) {
	pats, err := s.blockSnap(ctx)
	if err != nil {
		return false, err
	}
	if len(pats) == 0 {
		return false, nil
	}
	lower := strings.ToLower(key)
	for _, p := range pats {
		if matchGlob(p, lower) {
			return true, nil
		}
	}
	return false, nil
}

// dropBlocked removes blocked rows from a multi-row result.
func (s *Store) dropBlocked(ctx context.Context, rows []sqlc.Artifact) ([]sqlc.Artifact, error) {
	pats, err := s.blockSnap(ctx)
	if err != nil {
		return nil, err
	}
	if len(pats) == 0 || len(rows) == 0 {
		return rows, nil
	}
	out := rows[:0]
	for _, r := range rows {
		blocked, err := s.blockedKey(ctx, blockKey(r))
		if err != nil {
			return nil, err
		}
		if !blocked {
			out = append(out, r)
		}
	}
	return out, nil
}

// blockFilterSQL returns a WHERE fragment that excludes blocked rows, plus the
// pattern array it expects at $pos. Both are empty when nothing is blocked.
// The expression mirrors blockKey exactly, so a row dropped here is a row
// Blocked would also refuse.
func (s *Store) blockFilterSQL(ctx context.Context, pos int) (string, []string, error) {
	pats, err := s.blockSnap(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(pats) == 0 {
		return "", nil, nil
	}
	likes := make([]string, len(pats))
	for i, p := range pats {
		likes[i] = blockLike(p)
	}
	return fmt.Sprintf(
		"COALESCE(NULLIF(named_slug, ''), artifact_id::text) NOT ILIKE ALL ($%d::text[])", pos,
	), likes, nil
}

// blockLike converts a pattern to a SQL LIKE pattern honouring the SAME two
// wildcards matchGlob does: `*` for any run, `?` for one character. globToLike
// alone leaves `?` literal, which would let the catalog list a document the
// single-row reads have already made absent. The `?` substitution runs after
// the escaping, and an escape sequence never contains one.
func blockLike(p string) string {
	return strings.ReplaceAll(globToLike(p), "?", "_")
}

// ─── admin surface ───────────────────────────────────────────────────

// ListBlocks returns the whole block list, newest first.
func (s *Store) ListBlocks(ctx context.Context) ([]Block, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT pattern, reason, created_by, created_at FROM artifact_blocks ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list blocks: %w", err)
	}
	defer rows.Close()
	out := []Block{}
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.Pattern, &b.Reason, &b.CreatedBy, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("pgstore: list blocks scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AddBlock blocks everything matching pattern. Re-blocking an existing pattern
// replaces its reason and attribution.
func (s *Store) AddBlock(ctx context.Context, pattern, reason, by string) error {
	// Stored lowercased because matching folds case and the pattern is the
	// primary key. Otherwise two spellings of one name become two rows, and
	// lifting the one an admin typed leaves the document hidden by the other.
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return fmt.Errorf("%w: block pattern is required", ErrInvalidInput)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO artifact_blocks (pattern, reason, created_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (pattern) DO UPDATE SET reason = EXCLUDED.reason,
		                                    created_by = EXCLUDED.created_by,
		                                    created_at = now()`,
		pattern, reason, by); err != nil {
		return fmt.Errorf("pgstore: add block: %w", err)
	}
	s.invalidateBlocks(ctx)
	return nil
}

// RemoveBlock lifts one pattern and returns how many rows it removed.
func (s *Store) RemoveBlock(ctx context.Context, pattern string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM artifact_blocks WHERE pattern = $1`,
		strings.ToLower(strings.TrimSpace(pattern)))
	if err != nil {
		return 0, fmt.Errorf("pgstore: remove block: %w", err)
	}
	s.invalidateBlocks(ctx)
	return tag.RowsAffected(), nil
}

// blockedSlugTx answers the same question as BlockedSlug from the table rather
// than the snapshot, inside a caller's transaction. Writes are rare enough to
// afford the query, and a write is the one operation where a stale answer
// leaves something behind.
func blockedSlugTx(ctx context.Context, tx pgx.Tx, slug string) (bool, error) {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM artifact_blocks
		 WHERE lower($1) LIKE lower(translate(replace(replace(pattern, '%', '\%'), '_', '\_'), '*?', '%_')) ESCAPE '\'
		 LIMIT 1`, slug).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pgstore: block check: %w", err)
	}
	return true, nil
}

// ─── the admin review path ───────────────────────────────────────────
//
// These two reads are the ONLY ones in this package that deliberately ignore
// the block, and both are confined so that neither becomes a general bypass.
// Each can return a row only while that row is BLOCKED, so a caller already
// had to name the document and hold MANAGE_ARTIFACTS to reach anything here,
// and nothing reachable through them is reachable any other way.

// BlockedDoc is what a block currently hides, in the only shape the review
// path hands out: identity and provenance, never content. It is deliberately
// NOT sqlc.Artifact, so a caller cannot pass it to the content path by
// mistake — fetching a body is a second, separately named call.
type BlockedDoc struct {
	ArtifactID   string    `json:"artifact_id"`
	NamedSlug    *string   `json:"named_slug"`
	Version      *int32    `json:"version"`
	Title        string    `json:"title"`
	Creator      string    `json:"creator"`
	ArtifactType string    `json:"artifact_type"`
	ContentType  string    `json:"content_type"`
	SizeBytes    *int64    `json:"size_bytes"`
	CreatedAt    time.Time `json:"created_at"`
}

// BlockedMatchLimit bounds one pattern's match list. A glob can name a large
// family, and the page asking is a review surface, not an export.
const BlockedMatchLimit = 200

// ListBlockedMatches returns what one pattern currently hides: the latest live
// version of each matching document, metadata only. Archived versions are left
// out for the same reason the catalog leaves them out — they were already
// taken down, and this answers "what is this block costing me".
func (s *Store) ListBlockedMatches(ctx context.Context, pattern string) ([]BlockedDoc, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT artifact_id, named_slug, version, title, creator,
		       artifact_type, content_type, size_bytes, created_at
		  FROM artifacts
		 WHERE deleted_at IS NULL
		   AND (named_slug IS NULL
		        OR version = (SELECT MAX(b.version) FROM artifacts b
		                       WHERE b.named_slug = artifacts.named_slug AND b.deleted_at IS NULL))
		   AND COALESCE(NULLIF(named_slug, ''), artifact_id::text) ILIKE $1
		 ORDER BY created_at DESC
		 LIMIT $2`, blockLike(strings.ToLower(pattern)), BlockedMatchLimit)
	if err != nil {
		return nil, fmt.Errorf("pgstore: blocked matches: %w", err)
	}
	defer rows.Close()
	out := []BlockedDoc{}
	for rows.Next() {
		var d BlockedDoc
		var id pgtype.UUID
		if err := rows.Scan(&id, &d.NamedSlug, &d.Version, &d.Title, &d.Creator,
			&d.ArtifactType, &d.ContentType, &d.SizeBytes, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("pgstore: blocked matches scan: %w", err)
		}
		d.ArtifactID = uuidFrom(id).String()
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetBlockedBySlug returns a blocked document's row for the admin review
// route. It refuses a slug that is NOT blocked: an unblocked document is
// readable through the ordinary path under the ordinary rules, so allowing it
// here would turn this into a way to read anything while skipping them.
func (s *Store) GetBlockedBySlug(ctx context.Context, slug string) (sqlc.Artifact, error) {
	blocked, err := s.BlockedSlug(ctx, slug)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	if !blocked {
		return sqlc.Artifact{}, ErrNotFound
	}
	slugPtr := &slug
	row, err := s.q.GetLatestArtifactBySlug(ctx, slugPtr)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Artifact{}, ErrNotFound
	}
	return row, err
}

// GetBlockedByID is GetBlockedBySlug for a document that has no slug. A
// slugless artifact — every ATTACHMENT is one — can only be blocked by its id,
// so without this the review route could not open the documents that are most
// likely to be blocked individually. Same refusal: the row must be blocked.
func (s *Store) GetBlockedByID(ctx context.Context, id uuid.UUID) (sqlc.Artifact, error) {
	row, err := s.q.GetArtifact(ctx, pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Artifact{}, ErrNotFound
	}
	if err != nil {
		return sqlc.Artifact{}, err
	}
	blocked, err := s.Blocked(ctx, row)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	if !blocked {
		return sqlc.Artifact{}, ErrNotFound
	}
	return row, nil
}
