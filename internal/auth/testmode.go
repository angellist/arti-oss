package auth

import (
	"encoding/json"
	"net/http"
	"time"
)

// TestRoutes mounts a /auth/test endpoint that mints arti JWTs without
// the Google round-trip. Intended for local dev + integration tests.
// Production deployments MUST NOT set ARTI_TEST_MODE=1 (the binary
// refuses to mount this handler unless the flag is set).
func TestRoutes(signer *JWTSigner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Email == "" {
			body.Email = "test@example.com"
		}
		if !IsAllowed(body.Email) {
			writeErr(w, 403, "email domain not allowed", "forbidden")
			return
		}
		tok, _ := signer.Sign(Claims{
			Email: body.Email, Scopes: []string{"user"}, TTL: 24 * time.Hour,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": tok,
			"email":        body.Email,
		})
	})
}
