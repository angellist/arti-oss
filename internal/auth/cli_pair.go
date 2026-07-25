package auth

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// PairStore maps a CLI exchange code (generated server-side after Google
// callback or by /auth/test) to the verified email. Codes are single-use,
// expire after a short window.
type PairStore interface {
	Put(code, email string)
	Take(code string) (email string, ok bool)
}

type InMemPairStore struct {
	mu   sync.Mutex
	data map[string]pair
	ttl  time.Duration
}

type pair struct {
	email string
	exp   time.Time
}

func NewInMemPairStore() *InMemPairStore {
	return &InMemPairStore{data: map[string]pair{}, ttl: 5 * time.Minute}
}

func (s *InMemPairStore) Put(code, email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[code] = pair{email: email, exp: time.Now().Add(s.ttl)}
}

func (s *InMemPairStore) Take(code string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data[code]
	if !ok || time.Now().After(p.exp) {
		return "", false
	}
	delete(s.data, code)
	return p.email, true
}

// CLIExchangeHandler returns POST /auth/cli/exchange — single-use code →
// access + refresh tokens.
func CLIExchangeHandler(signer *JWTSigner, store PairStore, accessTTL, refreshTTL time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
			writeErr(w, 400, "bad request", "bad-request")
			return
		}
		email, ok := store.Take(body.Code)
		if !ok {
			writeErr(w, 404, "unknown or expired code", "not-found")
			return
		}
		issueTokens(w, signer, email, accessTTL, refreshTTL)
	}
}

// CLIRefreshHandler returns POST /auth/cli/refresh — refresh token →
// fresh access + refresh tokens.
func CLIRefreshHandler(signer *JWTSigner, accessTTL, refreshTTL time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RefreshToken == "" {
			writeErr(w, 400, "bad request", "bad-request")
			return
		}
		c, err := signer.Verify(body.RefreshToken)
		if err != nil {
			writeErr(w, 401, "invalid refresh: "+err.Error(), "unauthorized")
			return
		}
		if !containsString(c.Scopes, "refresh") {
			writeErr(w, 401, "not a refresh token", "unauthorized")
			return
		}
		// Flow-binding (H7): only CLI-issued (or legacy untyped) refresh tokens
		// are valid here. A device-flow refresh token (Fam set, or upload scope)
		// must be refreshed at /auth/device/refresh — accepting it here would
		// launder an upload-scoped token into a full `user` session. An MCP
		// refresh token belongs at /oauth/token.
		if c.Fam != "" || c.IsUploadScoped() || c.Typ == TokenTypeMCP {
			writeErr(w, 401, "refresh token not valid for this endpoint", "unauthorized")
			return
		}
		if !IsAllowed(c.Email) {
			writeErr(w, 403, "email domain not allowed", "forbidden")
			return
		}
		issueTokens(w, signer, c.Email, accessTTL, refreshTTL)
	}
}

func issueTokens(w http.ResponseWriter, s *JWTSigner, email string, access, refresh time.Duration) {
	at, _ := s.Sign(Claims{Email: email, Scopes: []string{"user"}, TTL: access})
	// Typ binds the refresh token to the CLI flow (H7) so it can't be redeemed
	// at /oauth/token; device tokens (Fam set) are rejected at both.
	rt, _ := s.Sign(Claims{Email: email, Scopes: []string{"refresh"}, Typ: TokenTypeCLI, TTL: refresh})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  at,
		"refresh_token": rt,
		"expires_at":    time.Now().Add(access).Unix(),
		"email":         email,
	})
}

func writeErr(w http.ResponseWriter, code int, detail, codeStr string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"detail": detail, "code": codeStr})
}

func containsString(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}
