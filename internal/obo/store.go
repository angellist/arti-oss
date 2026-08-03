package obo

import (
	"context"
	"time"
)

// ClientReg is arti's dynamically-registered OAuth client at one authorization
// server (RFC 7591). One per AS issuer, shared by every pod and every resource
// behind that issuer.
type ClientReg struct {
	ClientID     string
	ClientSecret string
}

// TokenRec is a per-user on-behalf-of token for one upstream resource.
type TokenRec struct {
	Access  string
	Refresh string
	Expiry  time.Time
}

// Store persists the OAuth state that must be shared across pods: the DCR client
// per AS issuer, and per-user OBO tokens. The PKCE state is deliberately NOT
// here — it's a self-contained encrypted token (see Cipher.SealState). A
// production implementation encrypts the secret fields at rest.
type Store interface {
	GetClient(ctx context.Context, issuer string) (ClientReg, bool, error)
	// PutClient registers the client if absent and is idempotent under races
	// (INSERT … ON CONFLICT (issuer) DO NOTHING): a concurrent registration by
	// another pod wins and is preserved, so callers re-read after PutClient.
	PutClient(ctx context.Context, issuer string, c ClientReg) error
	GetToken(ctx context.Context, email, resource string) (TokenRec, bool, error)
	// PutToken upserts the per-user token (ON CONFLICT (email,resource) DO UPDATE).
	PutToken(ctx context.Context, email, resource string, t TokenRec) error
}
