package artifacts

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// The authenticated half of external share links: mint, list, revoke, and read
// the access log. All four are owner-or-admin, enforced in the service layer.

// MountShareAdmin registers the owner-facing share endpoints. Registered only
// when the feature is enabled, so with the flag off they are absent rather
// than forbidden — a disabled feature should look like it does not exist.
//
// mintLimiter is applied to the mint route alone: it is the only one that
// creates a credential. Pass nil to skip it.
func MountShareAdmin(r chi.Router, svc *Service, mintLimiter func(http.Handler) http.Handler) {
	if !svc.ShareEnabled() {
		return
	}
	mint := http.HandlerFunc(svc.httpMintShare)
	if mintLimiter != nil {
		r.Method(http.MethodPost, "/api/artifacts/{id}/shares", mintLimiter(mint))
	} else {
		r.Method(http.MethodPost, "/api/artifacts/{id}/shares", mint)
	}
	r.Get("/api/artifacts/{id}/shares", svc.httpListShares)
	r.Delete("/api/shares/{shareID}", svc.httpRevokeShare)
	r.Get("/api/shares/{shareID}/opens", svc.httpShareOpens)
}

// writeShareErr maps a service error to a status. Kept in one place so the
// four endpoints cannot drift on the 403-vs-404 distinction, which is a
// security property rather than a formatting choice: ErrNotFound means the
// caller may not know the document exists.
func writeShareErr(w http.ResponseWriter, err error) {
	var br badRequest
	if errors.As(err, &br) {
		writeError(w, http.StatusBadRequest, "bad-request", br.msg)
		return
	}
	var fb forbidden
	if errors.As(err, &fb) {
		writeError(w, http.StatusForbidden, "forbidden", fb.msg)
		return
	}
	if errors.Is(err, pgstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not-found", "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", err.Error())
}

// httpMintShare creates an external link. POST, never GET: a GET that mints a
// credential turns one clicked link into a token issued as the victim, which
// this repository has already learned once (PR #256, /oauth/authorize).
func (s *Service) httpMintShare(w http.ResponseWriter, r *http.Request) {
	var body MintShareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "decode body: "+err.Error())
		return
	}
	res, err := s.MintShare(r.Context(), chi.URLParam(r, "id"), auth.EmailFromContext(r.Context()), body)
	if err != nil {
		writeShareErr(w, err)
		return
	}
	// The only time the URL is ever returned. It is not recoverable afterwards.
	writeJSON(w, http.StatusCreated, res)
}

func (s *Service) httpListShares(w http.ResponseWriter, r *http.Request) {
	rows, err := s.ListShares(r.Context(), chi.URLParam(r, "id"), auth.EmailFromContext(r.Context()))
	if err != nil {
		writeShareErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": rows})
}

func (s *Service) httpRevokeShare(w http.ResponseWriter, r *http.Request) {
	if err := s.RevokeShare(r.Context(), chi.URLParam(r, "shareID"), auth.EmailFromContext(r.Context())); err != nil {
		writeShareErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) httpShareOpens(w http.ResponseWriter, r *http.Request) {
	rows, err := s.ShareOpens(r.Context(), chi.URLParam(r, "shareID"), auth.EmailFromContext(r.Context()))
	if err != nil {
		writeShareErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"opens": rows})
}
