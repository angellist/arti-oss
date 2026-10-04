package pgstore

import (
	"context"
	"fmt"
)

type BookmarkState struct {
	Count      int64
	Bookmarked bool
}

// SetBookmark adds or removes one person's bookmark on a slug. Both are
// idempotent, so a double click cannot count twice.
func (s *Store) SetBookmark(ctx context.Context, viewKey, email string, on bool) error {
	q := `DELETE FROM artifact_bookmarks WHERE view_key = $1 AND email = lower($2)`
	if on {
		q = `INSERT INTO artifact_bookmarks (view_key, email) VALUES ($1, lower($2)) ON CONFLICT DO NOTHING`
	}
	if _, err := s.pool.Exec(ctx, q, viewKey, email); err != nil {
		return fmt.Errorf("pgstore: set bookmark: %w", err)
	}
	return nil
}

func (s *Store) Bookmarks(ctx context.Context, viewKey, email string) (BookmarkState, error) {
	const q = `
SELECT count(*), bool_or(email = lower($2)) IS TRUE
FROM artifact_bookmarks
WHERE view_key = $1`
	var out BookmarkState
	if err := s.pool.QueryRow(ctx, q, viewKey, email).Scan(&out.Count, &out.Bookmarked); err != nil {
		return BookmarkState{}, fmt.Errorf("pgstore: bookmarks: %w", err)
	}
	return out, nil
}

// BookmarkStates returns, for each key that has bookmarks, the count and
// whether email is one of them. Keys with no bookmarks are absent.
func (s *Store) BookmarkStates(ctx context.Context, keys []string, email string) (map[string]BookmarkState, error) {
	out := make(map[string]BookmarkState, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT view_key, count(*), bool_or(email = lower($2)) IS TRUE
FROM artifact_bookmarks
WHERE view_key = ANY($1)
GROUP BY view_key`
	rows, err := s.pool.Query(ctx, q, keys, email)
	if err != nil {
		return nil, fmt.Errorf("pgstore: bookmark states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var st BookmarkState
		if err := rows.Scan(&key, &st.Count, &st.Bookmarked); err != nil {
			return nil, fmt.Errorf("pgstore: bookmark states scan: %w", err)
		}
		out[key] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: bookmark states: %w", err)
	}
	return out, nil
}
