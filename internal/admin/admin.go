// Package admin exposes admin-only, read-only operational endpoints — a small
// surface for debugging prod without reaching the database directly. Every
// handler is gated by the MANAGE_ARTIFACTS permission.
package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

type Service struct {
	pool  *pgxpool.Pool
	store *pgstore.Store
}

func NewService(pool *pgxpool.Pool, store *pgstore.Store) *Service {
	return &Service{pool: pool, store: store}
}

// Mount attaches admin routes. The outer group already applies auth
// middleware; each handler additionally enforces admin membership.
func (s *Service) Mount(r chi.Router) {
	r.Get("/api/admin/stats", s.stats)
}

// Stats is a read-only operational snapshot.
type Stats struct {
	Artifacts ArtifactStats `json:"artifacts"`
	Comments  CommentStats  `json:"comments"`
}

type ArtifactStats struct {
	Active   int            `json:"active"`
	Archived int            `json:"archived"`
	ByType   map[string]int `json:"by_type"` // active artifacts, keyed by artifact_type
}

type CommentStats struct {
	Threads         int `json:"threads"`
	ThreadsOpen     int `json:"threads_open"`
	ThreadsResolved int `json:"threads_resolved"`
	Live            int `json:"live"`    // non-deleted comments
	Last7Days       int `json:"last_7d"` // non-deleted comments created in the last 7 days
}

func (s *Service) stats(w http.ResponseWriter, r *http.Request) {
	ok, err := s.store.HasPermission(r.Context(), auth.EmailFromContext(r.Context()), rbac.ManageArtifacts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		// 404 (not 403) so the endpoint isn't discoverable, consistent with
		// the rest of the API.
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	out, err := s.collect(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) collect(ctx context.Context) (Stats, error) {
	st := Stats{Artifacts: ArtifactStats{ByType: map[string]int{}}}

	// Artifacts: active/archived totals + active counts by type.
	rows, err := s.pool.Query(ctx, `
		SELECT artifact_type,
		       count(*) FILTER (WHERE deleted_at IS NULL)     AS active,
		       count(*) FILTER (WHERE deleted_at IS NOT NULL) AS archived
		FROM artifacts
		GROUP BY artifact_type`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var typ string
		var active, archived int
		if err := rows.Scan(&typ, &active, &archived); err != nil {
			rows.Close()
			return st, err
		}
		st.Artifacts.Active += active
		st.Artifacts.Archived += archived
		if active > 0 {
			st.Artifacts.ByType[typ] = active
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	// Comment threads by status.
	trows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM comment_threads GROUP BY status`)
	if err != nil {
		return st, err
	}
	for trows.Next() {
		var status string
		var n int
		if err := trows.Scan(&status, &n); err != nil {
			trows.Close()
			return st, err
		}
		st.Comments.Threads += n
		switch status {
		case "open":
			st.Comments.ThreadsOpen += n
		case "resolved":
			st.Comments.ThreadsResolved += n
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return st, err
	}

	// Comments: live total + last-7-days activity.
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM comments WHERE deleted_at IS NULL`).Scan(&st.Comments.Live); err != nil {
		return st, err
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM comments WHERE deleted_at IS NULL AND created_at > now() - interval '7 days'`).
		Scan(&st.Comments.Last7Days); err != nil {
		return st, err
	}
	return st, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"detail": msg, "code": http.StatusText(code)})
}
