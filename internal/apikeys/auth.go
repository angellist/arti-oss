package apikeys

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
)

type authStore interface {
	GetAPIKeyByHash(ctx context.Context, hash []byte) (sqlc.ApiKey, error)
	TouchAPIKey(ctx context.Context, id pgtype.UUID) error
}

// Authenticator implements auth.APIKeyAuthenticator against the api_keys registry.
type Authenticator struct{ store authStore }

// NewAuthenticator returns an Authenticator backed by s.
func NewAuthenticator(s authStore) *Authenticator { return &Authenticator{store: s} }

// Authenticate validates an arti_ bearer and returns scoped claims. The query
// already filters revoked/expired rows. Fail-closed: a key must carry at least
// one scope, and every scope must be one the running code enforces — otherwise
// the key grants nothing. (An empty scope set would leave IsUploadScoped false,
// silently bypassing EnforceUploadScope and acting as a full user.)
func (a *Authenticator) Authenticate(ctx context.Context, token string) (auth.Claims, error) {
	if !strings.HasPrefix(token, auth.APIKeyPrefix) {
		return auth.Claims{}, auth.ErrMalformed
	}
	row, err := a.store.GetAPIKeyByHash(ctx, HashKey(token))
	if err != nil {
		return auth.Claims{}, auth.ErrBadSignature
	}
	if len(row.Scopes) == 0 {
		return auth.Claims{}, auth.ErrBadSignature // fail-closed: no scope ⇒ grants nothing
	}
	for _, s := range row.Scopes {
		if !IsSupportedScope(s) {
			return auth.Claims{}, auth.ErrBadSignature // fail-closed
		}
	}
	go func(id pgtype.UUID) {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.store.TouchAPIKey(bg, id)
	}(row.ID)
	return auth.Claims{Email: row.OwnerEmail, Scopes: row.Scopes, Typ: "api-key"}, nil
}
