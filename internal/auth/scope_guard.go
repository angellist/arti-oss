package auth

import (
	"net/http"
	"strings"
	"time"
)

// isUploadAllowed is the default-deny allowlist for upload-scoped tokens.
// Allowed: create the artifact, append a version, read artifacts, GET /api/me.
// Everything else (delete, patch, suggest-metadata, admin, mcp) is denied.
func isUploadAllowed(method, path string) bool {
	switch {
	case method == http.MethodGet && path == "/api/me":
		return true
	case method == http.MethodGet && strings.HasPrefix(path, "/api/artifacts"):
		return true
	case method == http.MethodPost && path == "/api/artifacts":
		return true
	case method == http.MethodPost && strings.HasPrefix(path, "/api/artifacts/by-slug/") &&
		strings.HasSuffix(path, "/append"):
		return true
	}
	return false
}

// EnforceUploadScope is a no-op for normal user/service tokens. For an
// upload-scoped token it (a) honors revocation of long-lived families,
// (b) applies the default-deny allowlist — downgrading even an admin's token
// to create/append/read — and (c) caps the request body at maxUploadBytes.
// Slots into the authed group AFTER RequireAuth.
func EnforceUploadScope(store DeviceStore, maxUploadBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := ClaimsFromContext(r.Context())
			if !ok || !c.IsUploadScoped() {
				next.ServeHTTP(w, r) // not an upload token: untouched
				return
			}
			// Long-lived tokens carry a family id; check it isn't revoked or
			// expired. The family expiry matters because the last refresh before
			// the 30d cap can mint a 24h access token that outlives the family —
			// the access JWT's own exp doesn't enforce the family deadline.
			if c.Fam != "" {
				fam, err := store.GetDeviceTokenFamily(r.Context(), c.Fam)
				if err != nil || fam.Revoked || (fam.ExpiresAt.Valid && fam.ExpiresAt.Time.Before(time.Now())) {
					forbidden(w, "upload token revoked or expired")
					return
				}
			}
			if !isUploadAllowed(r.Method, r.URL.Path) {
				forbidden(w, "upload-scoped token: only artifact create/append/read permitted")
				return
			}
			// Tighter body cap than the global limit — upload tokens are minted for
			// modest sandbox output (screenshots, reports, build artifacts). The
			// create/append handler maps MaxBytesReader overflow to 413.
			if r.Method == http.MethodPost && maxUploadBytes > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
