package admin

import (
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// The blocked-document review surface.
//
// A block takes a document away from everyone, admins included, so an admin
// who wants to know whether to lift one has nothing to look at. These routes
// are the deliberate exception, and they are the whole of it: the ordinary
// viewer, catalog, search and API stay closed to every caller. Nothing here
// shares a URL with a normal read, so no existing path gains an admin branch.
//
// Two things keep the exception from widening. The store reads behind these
// routes can only ever return a document that is currently BLOCKED, so they
// are useless as a general bypass. And a body is served as plain text under
// nosniff, never as its own content type, so a blocked HTML or SVG document
// cannot execute on an admin's session while being reviewed.

// blockedBodyMaxBytes bounds what the review route will return inline. This is
// a page for reading a document, not for exporting one; past the cap an admin
// who still needs the bytes lifts the block.
const blockedBodyMaxBytes = 1 << 20 // 1 MiB

func (s *Service) mountBlocked(r chi.Router) {
	r.Get("/api/admin/blocks/matches", s.blockMatches)
	r.Get("/api/admin/blocked/{ident}", s.blockedDoc)
	r.Get("/api/admin/blocked/{ident}/raw", s.blockedBody)
}

// blockMatches answers what one pattern currently hides. The pattern comes
// from the block list the caller just read, so this adds no way to enumerate
// anything that is not already blocked.
func (s *Service) blockMatches(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	pattern := r.URL.Query().Get("pattern")
	if strings.TrimSpace(pattern) == "" {
		writeErr(w, http.StatusBadRequest, "pattern is required")
		return
	}
	matches, err := s.store.ListBlockedMatches(r.Context(), pattern)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pattern": pattern,
		"matches": matches,
		"limit":   pgstore.BlockedMatchLimit,
	})
}

func (s *Service) blockedDoc(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	row, ok := s.resolveBlocked(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifact_id":   pgstore.UUIDFromPG(row.ArtifactID).String(),
		"named_slug":    row.NamedSlug,
		"version":       row.Version,
		"title":         row.Title,
		"description":   row.Description,
		"creator":       row.Creator,
		"artifact_type": row.ArtifactType,
		"content_type":  row.ContentType,
		"size_bytes":    row.SizeBytes,
		"labels":        row.Labels,
		"scopes":        row.Scopes,
		"created_at":    row.CreatedAt.Time,
		"archived":      row.DeletedAt.Valid,
		"body_readable": !pgstore.IsPackageLike(row.ArtifactType) && row.ArtifactType != pgstore.TypeMap,
		"max_bytes":     blockedBodyMaxBytes,
	})
}

// blockedBody returns the document's bytes as PLAIN TEXT, whatever the
// document actually is. A blocked HTML page rendered at its own content type
// would run in an admin's session on arti's origin, which is a strange way to
// review something that was blocked for being dangerous.
func (s *Service) blockedBody(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	row, ok := s.resolveBlocked(w, r)
	if !ok {
		return
	}
	// A zip is not reviewable as text, and unpacking one here would rebuild
	// the whole package-serving path on a surface that exists to read one
	// document. An admin who needs to open a package lifts the block.
	if pgstore.IsPackageLike(row.ArtifactType) || row.ArtifactType == pgstore.TypeMap {
		writeErr(w, http.StatusUnsupportedMediaType,
			"a "+row.ArtifactType+" cannot be reviewed as text; lift the block to open it")
		return
	}
	rc, err := s.store.Content(r.Context(), row)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, blockedBodyMaxBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	truncated := len(body) > blockedBodyMaxBytes
	if truncated {
		body = body[:blockedBodyMaxBytes]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Arti-Blocked-Truncated", map[bool]string{true: "1", false: "0"}[truncated])
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// resolveBlocked loads the blocked document named in the URL, or writes the
// same 404 an unblocked or missing document gets. The identifier is a slug, or
// an artifact id for a document that has no slug — a slugless artifact can
// only be blocked by its id, so resolving by slug alone would leave exactly
// those documents unreviewable. Either way the store read refuses anything
// that is not currently blocked, so this route cannot read an ordinary
// document even for an admin.
func (s *Service) resolveBlocked(w http.ResponseWriter, r *http.Request) (row sqlc.Artifact, ok bool) {
	ident := chi.URLParam(r, "ident")
	if ident == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return row, false
	}
	var (
		found sqlc.Artifact
		err   error
	)
	if id, perr := uuid.Parse(ident); perr == nil {
		found, err = s.store.GetBlockedByID(r.Context(), id)
	} else {
		found, err = s.store.GetBlockedBySlug(r.Context(), ident)
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return row, false
	}
	return found, true
}
