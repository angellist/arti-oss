package artifacts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/auth"
)

// BookmarksDTO is slug-scoped like ViewStatsDTO. Count is public; Bookmarked
// is the caller's own state and never anyone else's.
type BookmarksDTO struct {
	Count      int64 `json:"count"`
	Bookmarked bool  `json:"bookmarked"`
}

var errNoBookmarker = errors.New("bookmarks need a signed-in user")

func (s *Service) Bookmarks(ctx context.Context, id uuid.UUID, caller string) (BookmarksDTO, error) {
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return BookmarksDTO{}, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return BookmarksDTO{}, err
	}
	st, err := s.store.Bookmarks(ctx, viewKeyOf(row), caller)
	if err != nil {
		return BookmarksDTO{}, err
	}
	return BookmarksDTO{Count: st.Count, Bookmarked: st.Bookmarked}, nil
}

func (s *Service) SetBookmark(ctx context.Context, id uuid.UUID, caller string, on bool) (BookmarksDTO, error) {
	if caller == "" {
		return BookmarksDTO{}, errNoBookmarker
	}
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return BookmarksDTO{}, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return BookmarksDTO{}, err
	}
	if err := s.store.SetBookmark(ctx, viewKeyOf(row), caller, on); err != nil {
		return BookmarksDTO{}, err
	}
	return s.Bookmarks(ctx, id, caller)
}

func (s *Service) httpBookmarks(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	out, err := s.Bookmarks(r.Context(), id, auth.EmailFromContext(r.Context()))
	if writeMaybeNotFound(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// httpSetBookmark serves PUT (add) and DELETE (remove) on /bookmark.
func (s *Service) httpSetBookmark(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	out, err := s.SetBookmark(r.Context(), id, auth.EmailFromContext(r.Context()), r.Method == http.MethodPut)
	if errors.Is(err, errNoBookmarker) {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	if writeMaybeNotFound(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// fillBookmarks annotates a catalog page with slug-scoped bookmark counts and
// the signed-in caller's own bookmarks in one query. Best-effort, like
// fillViewCounts.
func (s *Service) fillBookmarks(ctx context.Context, infos []ArtifactInfo) {
	if len(infos) == 0 {
		return
	}
	keys := make([]string, 0, len(infos))
	for _, a := range infos {
		keys = append(keys, viewKeyOfInfo(a))
	}
	states, err := s.store.BookmarkStates(ctx, keys, auth.EmailFromContext(ctx))
	if err != nil {
		slog.Warn("bookmarks for catalog page failed", "err", err, "rows", len(infos))
		return
	}
	for i := range infos {
		st := states[viewKeyOfInfo(infos[i])]
		n, on := st.Count, st.Bookmarked
		infos[i].BookmarkCount = &n
		infos[i].Bookmarked = &on
	}
}
