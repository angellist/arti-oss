package artifacts

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ViewStatsDTO reports slug-scoped views, unlike comment counts which attach
// to one version's artifact_id.
type ViewStatsDTO struct {
	ViewKey       string       `json:"view_key"`
	Total         int64        `json:"total"`
	Last7d        int64        `json:"last_7d"`
	Last30d       int64        `json:"last_30d"`
	UniqueViewers int64        `json:"unique_viewers"`
	LastViewedAt  *time.Time   `json:"last_viewed_at"`
	CanSeeViewers bool         `json:"can_see_viewers"`
	Recent        []ViewRowDTO `json:"recent"`
}

type ViewRowDTO struct {
	Viewer  string    `json:"viewer"`
	Surface string    `json:"surface"`
	Version *int32    `json:"version"`
	At      time.Time `json:"at"`
}

// viewKeyOf is the sole normalization point for slug-scoped view identity.
func viewKeyOf(row sqlc.Artifact) string {
	if row.NamedSlug != nil && *row.NamedSlug != "" {
		return *row.NamedSlug
	}
	return pgstore.UUIDFromPG(row.ArtifactID).String()
}

func (s *Service) recordView(r *http.Request, row sqlc.Artifact, viewer, surface string) {
	if err := s.store.RecordArtifactView(r.Context(),
		pgstore.UUIDFromPG(row.ArtifactID), viewKeyOf(row), row.Version, viewer, surface); err != nil {
		slog.Warn("artifact view not recorded", "err", err, "surface", surface)
	}
}

func (s *Service) ViewStats(ctx context.Context, id uuid.UUID, caller string) (ViewStatsDTO, error) {
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return ViewStatsDTO{}, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return ViewStatsDTO{}, err
	}
	key := viewKeyOf(row)
	stats, err := s.store.ArtifactViewStats(ctx, key)
	if err != nil {
		return ViewStatsDTO{}, err
	}
	out := ViewStatsDTO{
		ViewKey:       key,
		Total:         stats.Total,
		Last7d:        stats.Last7d,
		Last30d:       stats.Last30d,
		UniqueViewers: stats.UniqueViewers,
		LastViewedAt:  stats.LastViewedAt,
	}
	if s.isDocOwner(ctx, row, caller) == nil {
		out.CanSeeViewers = true
		rows, err := s.store.ListArtifactViews(ctx, key, 200)
		if err != nil {
			return ViewStatsDTO{}, err
		}
		out.Recent = make([]ViewRowDTO, 0, len(rows))
		for _, r := range rows {
			out.Recent = append(out.Recent, ViewRowDTO{
				Viewer: r.Viewer, Surface: r.Surface, Version: r.Version, At: r.At,
			})
		}
	}
	return out, nil
}

func (s *Service) RecordViewForID(ctx context.Context, id uuid.UUID, caller string) error {
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return err
	}
	return s.store.RecordArtifactView(ctx, id, viewKeyOf(row), row.Version, caller, "viewer")
}

func (s *Service) httpRecordView(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := s.RecordViewForID(r.Context(), id, auth.EmailFromContext(r.Context())); writeMaybeNotFound(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) httpViewStats(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	stats, err := s.ViewStats(r.Context(), id, auth.EmailFromContext(r.Context()))
	if writeMaybeNotFound(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// viewKeyOfInfo is viewKeyOf for a DTO, so the catalog annotation resolves
// keys the same way the recording path does.
func viewKeyOfInfo(a ArtifactInfo) string {
	if a.NamedSlug != nil && *a.NamedSlug != "" {
		return *a.NamedSlug
	}
	return a.ArtifactID
}
