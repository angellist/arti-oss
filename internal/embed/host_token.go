package embed

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/auth"
)

// handleHostToken mints a viewer's app token for an embedding host's server,
// which has already authenticated the viewer itself. There is no consent click:
// the surface's host_key is the whole credential, and the host vouches for the
// email. The response names the version the token is pinned to, so the host can
// load exactly that row with ?version=.
func (s *Service) handleHostToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	surfName := chi.URLParam(r, "surface")
	surf, ok := s.surfaces[surfName]
	if !ok || surf.HostKey == "" || surf.Identity != "user" || s.mint == nil {
		writeJSONError(w, http.StatusNotFound, "unknown surface")
		return
	}
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(presented), []byte(surf.HostKey)) != 1 {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Slug  string `json:"slug"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil ||
		req.Slug == "" || req.Email == "" {
		writeJSONError(w, http.StatusBadRequest, "slug and email required")
		return
	}
	email := strings.TrimSpace(req.Email)
	if !slugAllowed(surf.SlugAllow, req.Slug) || !auth.IsAllowed(email) {
		writeJSONError(w, http.StatusForbidden, "forbidden")
		return
	}
	tok, appID, version, err := s.mint.Minter.MintEmbedHostToken(r.Context(), req.Slug, email)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	s.mint.Logger.Info("embed-user app token minted",
		"email", email, "app", appID, "slug", req.Slug, "surface", surfName, "via", "host", "ip", r.RemoteAddr)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": tok, "app_id": appID, "version": version,
	})
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
