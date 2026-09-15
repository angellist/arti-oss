package artifacts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// DenialInfo is the one response in this service that confirms an artifact
// exists to a caller who cannot read it. Every other read collapses "no such
// artifact" and "not for you" into the same 404 (see checkAccess), because a
// 403 on the ordinary read path lets anyone probe the catalog for slugs.
//
// The denial route exists because that uniformity has a cost paid by people
// who were handed a link in good faith: the page they open is indistinguishable
// from a typo, so they cannot tell whether to fix the URL or ask someone for
// access, and the search ends in a Slack thread aimed at whoever shared it.
// What it discloses is the smallest set that ends that search — the document's
// name, its slug, and the address that can grant access. Never its content,
// its description, its labels or its ACL.
//
// Owner is the slug's owner, NOT the row's creator: versioning reassigns
// creator to whoever pushed the latest version, so a delegated writer would be
// named here as the person to ask while having no authority to change the ACL.
type DenialInfo struct {
	NamedSlug *string `json:"named_slug"`
	Version   *int32  `json:"version"`
	Title     string  `json:"title"`
	Owner     string  `json:"owner"`
}

func (s *Service) httpDenialBySlug(w http.ResponseWriter, r *http.Request) {
	caller, ok := denialCaller(r)
	if !ok {
		writeNotDenied(w)
		return
	}
	row, err := s.store.GetBySlug(r.Context(), chi.URLParam(r, "slug"), parseVersion(r))
	s.answerDenial(r.Context(), w, row, caller, err)
}

func (s *Service) httpDenialByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller, ok := denialCaller(r)
	if !ok {
		writeNotDenied(w)
		return
	}
	row, err := s.store.GetByID(r.Context(), id)
	s.answerDenial(r.Context(), w, row, caller, err)
}

// denialCaller returns the caller's address when their credential is an
// interactive session. A service credential (an arti_ API key), an embed or
// app page token, and a device upload token all get nothing: this route
// exists to unstick a person at a browser, and existence is not something a
// long-lived automated credential should be able to sweep the catalog for.
// IsSessionCredential states what a session IS, so a token type added later
// is refused here without anyone remembering to come back.
func denialCaller(r *http.Request) (string, bool) {
	c, ok := auth.ClaimsFromContext(r.Context())
	if !ok || !c.IsSessionCredential() {
		return "", false
	}
	email := auth.EmailFromContext(r.Context())
	return email, email != ""
}

// answerDenial discloses the artifact only on the one branch that means "this
// exists and you are the one being refused". Everything else answers with the
// same 404 the ordinary read path gives, so the route adds no signal beyond
// that single case.
func (s *Service) answerDenial(ctx context.Context, w http.ResponseWriter, row sqlc.Artifact, caller string, lookupErr error) {
	if lookupErr != nil {
		if errors.Is(lookupErr, pgstore.ErrNotFound) {
			writeNotDenied(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", lookupErr.Error())
		return
	}
	// An archived document is one its owner took down. Naming it here would
	// undo that decision, so it keeps the 404 every other read gives it.
	if row.DeletedAt.Valid {
		writeNotDenied(w)
		return
	}
	switch err := s.checkAccess(ctx, row, caller); {
	case err == nil:
		// The caller can read it after all — a grant that landed between the
		// page's failed read and this call, or an admin arriving by hand.
		// There is no denial to explain.
		writeNotDenied(w)
	case errors.Is(err, pgstore.ErrNotFound):
		shutOut, serr := s.deniedWholeSlug(ctx, row, caller)
		if serr != nil {
			writeError(w, http.StatusInternalServerError, "internal", serr.Error())
			return
		}
		if !shutOut {
			writeNotDenied(w)
			return
		}
		owner, oerr := s.denialOwner(ctx, row)
		if oerr != nil {
			writeError(w, http.StatusInternalServerError, "internal", oerr.Error())
			return
		}
		slog.Info("served access-denied details",
			"caller", caller,
			"artifact_id", pgstore.UUIDFromPG(row.ArtifactID).String(),
			"slug", row.NamedSlug)
		writeJSON(w, http.StatusOK, DenialInfo{
			NamedSlug: row.NamedSlug,
			Version:   row.Version,
			Title:     row.Title,
			Owner:     owner,
		})
	default:
		// A group-lookup failure must not read as a denial, which would
		// disclose an artifact this caller may well be allowed to read.
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// deniedWholeSlug reports whether `caller` is shut out of the slug entirely,
// rather than merely off the newest version. answerDenial resolves the latest
// row unfiltered, but the ordinary slug read resolves through
// GetLatestBySlugForCaller and silently serves the newest version the caller
// CAN read — so a caller with an older version would otherwise be handed a
// restricted newer version's title and number by this route. Reusing that same
// access-filtered lookup keeps "can read something here" meaning exactly what
// it means on the read path.
func (s *Service) deniedWholeSlug(ctx context.Context, row sqlc.Artifact, caller string) (bool, error) {
	if row.NamedSlug == nil || *row.NamedSlug == "" {
		return true, nil
	}
	_, err := s.store.GetLatestBySlugForCaller(ctx, *row.NamedSlug, caller)
	if errors.Is(err, pgstore.ErrNotFound) {
		return true, nil
	}
	return false, err
}

// denialOwner resolves the address the page tells the reader to ask. Access
// changes answer to the document's owner, which is what IsDocOwner gates every
// ACL write on; row.Creator names whoever pushed the latest version and can be
// a delegated writer with no such authority.
func (s *Service) denialOwner(ctx context.Context, row sqlc.Artifact) (string, error) {
	owner, err := s.store.DocOwner(ctx, row)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return row.Creator, nil
	}
	return owner, nil
}

func writeNotDenied(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not-found", "not found")
}
