package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CommentCount is the comment activity on ONE artifact version. Comments
// attach to `artifact_id`, which is per-version, so these numbers are about
// the exact version they are reported for — not the slug as a whole. That is
// the same scoping the viewer uses, so a count shown next to a row always
// matches what clicking through will display.
type CommentCount struct {
	// Comments is the number of non-deleted comments across all of the
	// version's threads.
	Comments int32
	// OpenThreads is how many of those threads are still unresolved.
	OpenThreads int32
}

// CommentCounts returns comment activity for the given artifact-version ids,
// keyed by id. Ids with no threads are simply absent from the map (callers
// treat a miss as zero), so the result is at most one entry per id with
// activity — a page of 50 rows costs one round trip.
//
// An empty `ids` short-circuits: `= ANY('{}')` is a legal but pointless query.
func (s *Store) CommentCounts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]CommentCount, error) {
	out := make(map[uuid.UUID]CommentCount, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	pgIDs := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		pgIDs = append(pgIDs, pgUUID(id))
	}
	// LEFT JOIN, not JOIN: a thread whose comments were all deleted still
	// counts as an open thread (it is still on the page, awaiting a reply),
	// and an inner join would drop it entirely. count(c.comment_id) ignores
	// the NULL rows a LEFT JOIN produces, so such a thread contributes 0
	// comments rather than 1.
	const q = `
SELECT t.artifact_id,
       count(c.comment_id) FILTER (WHERE c.deleted_at IS NULL)      AS comments,
       count(DISTINCT t.thread_id) FILTER (WHERE t.status = 'open') AS open_threads
  FROM comment_threads t
  LEFT JOIN comments c ON c.thread_id = t.thread_id
 WHERE t.artifact_id = ANY($1)
 GROUP BY t.artifact_id`
	rows, err := s.pool.Query(ctx, q, pgIDs)
	if err != nil {
		return nil, fmt.Errorf("pgstore: comment counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		var comments, open int64
		if err := rows.Scan(&id, &comments, &open); err != nil {
			return nil, fmt.Errorf("pgstore: comment counts scan: %w", err)
		}
		out[uuidFrom(id)] = CommentCount{Comments: int32(comments), OpenThreads: int32(open)}
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("pgstore: comment counts: %w", err)
	}
	return out, nil
}
