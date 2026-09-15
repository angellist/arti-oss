package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type ViewCount struct {
	Total   int64
	Last30d int64
}

type ViewStats struct {
	Total         int64
	Last7d        int64
	Last30d       int64
	UniqueViewers int64
	LastViewedAt  *time.Time
}

type ArtifactViewRow struct {
	Viewer  string
	Surface string
	Version *int32
	At      time.Time
}

// RecordArtifactView stores one human-facing delivery. Named viewers are
// deduplicated in SQL so concurrent requests cannot race a read-before-write
// check; anonymous deliveries deliberately all count.
func (s *Store) RecordArtifactView(
	ctx context.Context,
	artifactID uuid.UUID,
	viewKey string,
	version *int32,
	viewer string,
	surface string,
) error {
	const q = `
INSERT INTO artifact_views (artifact_id, view_key, version, viewer, surface)
SELECT $1, $2, $3, lower($4), $5
WHERE $4 = ''
   OR NOT EXISTS (
       SELECT 1
       FROM artifact_views
       WHERE view_key = $2
         AND lower(viewer) = lower($4)
         AND surface = $5
         AND at > now() - interval '30 seconds'
   )`
	if _, err := s.pool.Exec(ctx, q, pgUUID(artifactID), viewKey, version, viewer, surface); err != nil {
		return fmt.Errorf("pgstore: record artifact view: %w", err)
	}
	return nil
}

func (s *Store) ArtifactViewCounts(ctx context.Context, keys []string) (map[string]ViewCount, error) {
	out := make(map[string]ViewCount, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT view_key,
       count(*) AS total,
       count(*) FILTER (WHERE at > now() - interval '30 days') AS last_30d
FROM artifact_views
WHERE view_key = ANY($1)
GROUP BY view_key`
	rows, err := s.pool.Query(ctx, q, keys)
	if err != nil {
		return nil, fmt.Errorf("pgstore: artifact view counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var total, last30d int64
		if err := rows.Scan(&key, &total, &last30d); err != nil {
			return nil, fmt.Errorf("pgstore: artifact view counts scan: %w", err)
		}
		out[key] = ViewCount{Total: total, Last30d: last30d}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: artifact view counts: %w", err)
	}
	return out, nil
}

func (s *Store) ArtifactViewStats(ctx context.Context, key string) (ViewStats, error) {
	const q = `
SELECT count(*),
       count(*) FILTER (WHERE at > now() - interval '7 days'),
       count(*) FILTER (WHERE at > now() - interval '30 days'),
       count(DISTINCT NULLIF(lower(viewer), '')),
       max(at)
FROM artifact_views
WHERE view_key = $1`
	var out ViewStats
	var last pgtype.Timestamptz
	if err := s.pool.QueryRow(ctx, q, key).Scan(
		&out.Total, &out.Last7d, &out.Last30d, &out.UniqueViewers, &last,
	); err != nil {
		return ViewStats{}, fmt.Errorf("pgstore: artifact view stats: %w", err)
	}
	if last.Valid {
		t := last.Time
		out.LastViewedAt = &t
	}
	return out, nil
}

func (s *Store) ListArtifactViews(ctx context.Context, key string, limit int) ([]ArtifactViewRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	const q = `
SELECT viewer, surface, version, at
FROM artifact_views
WHERE view_key = $1
ORDER BY at DESC
LIMIT $2`
	rows, err := s.pool.Query(ctx, q, key, limit)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list artifact views: %w", err)
	}
	defer rows.Close()
	out := make([]ArtifactViewRow, 0, limit)
	for rows.Next() {
		var row ArtifactViewRow
		var at pgtype.Timestamptz
		if err := rows.Scan(&row.Viewer, &row.Surface, &row.Version, &at); err != nil {
			return nil, fmt.Errorf("pgstore: list artifact views scan: %w", err)
		}
		if at.Valid {
			row.At = at.Time
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: list artifact views: %w", err)
	}
	return out, nil
}
