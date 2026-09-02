// Package auth handles authentication for arti. Production uses
// Dex OIDC (via oauth2-proxy in front of the pod or a direct Bearer
// token); see OIDCVerifier in oidc.go. The in-process HS256 JWT
// signer in this file is used by the `arti login --email` test-mode
// CLI flow and integration tests, never as the prod path.
//
// HS256 token format: compact JWT, with claims:
//
//	{ "sub": "<sha256(email)>", "email": "<email>",
//	  "scope": ["user"], "iat": ..., "exp": ..., "jti": "<uuid>" }
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrExpired      = errors.New("auth: token expired")
	ErrBadSignature = errors.New("auth: bad signature")
	ErrMalformed    = errors.New("auth: malformed token")
)

// EmbedScopePrefix marks a token minted for the in-page comments overlay
// (scope `comment-embed:<artifactID>`). Such tokens are exposed in served-page
// source, so they must NOT be accepted as session credentials on the regular
// API/MCP routes — only by the embed middleware, which enforces the scope.
const EmbedScopePrefix = "comment-embed:"

// AppScopePrefix marks a token minted for an APP artifact's in-page bridge
// (scope `app:<artifactID>`). Like embed tokens, these are exposed in
// served-page source, so they must NOT be accepted as session credentials on
// the regular API/MCP routes — only by the apps proxy, which pins them to a
// single app + its tool allowlist.
const AppScopePrefix = "app:"

// IsEmbedScoped reports whether any scope is an embed (comment-overlay) scope.
func (c Claims) IsEmbedScoped() bool {
	for _, s := range c.Scopes {
		if strings.HasPrefix(s, EmbedScopePrefix) {
			return true
		}
	}
	return false
}

// APIKeyPrefix marks an opaque, self-serve API key (see internal/apikeys). A
// bearer with this prefix is authenticated against the api_keys registry by the
// APIKeys rung in RequireAuth, not by JWT verification. (JWTs start "eyJ", so
// no collision.)
const APIKeyPrefix = "arti_"

// APIKeyAuthenticator validates an opaque arti_ bearer and returns the scoped
// claims for its owner. Implemented by internal/apikeys (one-way import keeps the
// DB dependency out of internal/auth).
type APIKeyAuthenticator interface {
	Authenticate(ctx context.Context, token string) (Claims, error)
}

// UploadScope marks a device-flow token: create + append + read only. The
// EnforceUploadScope middleware downgrades even an admin holding such a token
// to that allowlist, so a leaked 30-day bearer can never delete or reach admin.
const UploadScope = "upload"

// Token flow types (Claims.Typ) bind a refresh token to the flow that issued
// it, so a refresh token minted at one endpoint can't be redeemed at another —
// e.g. a device-flow upload refresh token laundered into a full `user` session
// at /auth/cli/refresh or /oauth/token. Device-flow refresh tokens are
// identified by their family id (Claims.Fam) rather than a Typ. An empty Typ is
// a legacy (pre-H7) cli/mcp refresh token, still accepted at those endpoints.
const (
	TokenTypeCLI = "cli"
	TokenTypeMCP = "mcp"
	// TokenTypeAPIKey marks claims minted from an opaque arti_ API key
	// (internal/apikeys) — a service credential, not a person at a browser.
	TokenTypeAPIKey = "api-key"
)

// IsUploadScoped reports whether the token carries the device-flow upload scope.
func (c Claims) IsUploadScoped() bool { return containsString(c.Scopes, UploadScope) }

// IsSessionCredential reports whether these claims may stand in for an
// interactive browser session, as the arti_session cookie does. A valid
// signature is not enough on its own: every token this service mints is
// signed by the same key, and a caller can put any of them in that cookie
// by hand, so accepting one would launder a narrower or longer-lived
// token into a full session.
//
// This states what a session IS rather than listing what it is not, so a
// token type added later fails closed. loginFinisher.finish is the only
// mint of arti_session and it sets exactly the `user` scope, with no flow
// type and no token family. The negative clauses are the rejections
// RequireAuth applies on its HS256 rung (embed, app) and the flow-binding
// guard on the refresh path (family, upload), kept explicit so the
// intent survives a change to what the positive clauses admit.
func (c Claims) IsSessionCredential() bool {
	return c.Typ == "" && c.Fam == "" &&
		containsString(c.Scopes, "user") && !containsString(c.Scopes, "refresh") &&
		!c.IsEmbedScoped() && !c.IsAppScoped() && !c.IsUploadScoped()
}

// IsAppScoped reports whether any scope is an APP-bridge scope. Such tokens are
// exposed in served-page source and must only be honored by the apps proxy,
// never as a session credential on the regular API/MCP routes.
func (c Claims) IsAppScoped() bool {
	for _, s := range c.Scopes {
		if strings.HasPrefix(s, AppScopePrefix) {
			return true
		}
	}
	return false
}

// Claims is the JWT payload. TTL is a sender-side convenience; it is
// turned into Exp at Sign() time and is never set on a parsed token.
type Claims struct {
	Sub     string        `json:"sub"`
	Email   string        `json:"email"`
	Name    string        `json:"name,omitempty"`
	Picture string        `json:"picture,omitempty"`
	Scopes  []string      `json:"scope,omitempty"`
	IAT     int64         `json:"iat"`
	EXP     int64         `json:"exp,omitempty"`
	JTI     string        `json:"jti,omitempty"`
	Fam     string        `json:"fam,omitempty"` // device-token family id (upload tokens only)
	Typ     string        `json:"typ,omitempty"` // refresh-token flow binding (H7): TokenTypeCLI / TokenTypeMCP
	TTL     time.Duration `json:"-"`
}

type JWTSigner struct{ key []byte }

func NewJWTSigner(key []byte) *JWTSigner { return &JWTSigner{key: key} }

// DevDefaultSigningKey is the in-repo placeholder for JWT_SIGNING_KEY. It is
// publicly known (it lives in the source tree), so it must never sign tokens in
// a deployed env. Keep in sync with the `default:` struct tag on
// ServeCmd.JWTSigningKey in cmd/arti-server/cmd_serve.go.
const DevDefaultSigningKey = "dev-key-not-for-prod"

// MinSigningKeyLen is the production floor for JWT_SIGNING_KEY: 32 bytes
// (a 256-bit key for the HS256 HMAC).
const MinSigningKeyLen = 32

// ValidateSigningKey reports why a JWT_SIGNING_KEY is unfit to sign tokens in a
// deployed environment: empty, equal to the publicly-known dev default, or
// shorter than MinSigningKeyLen — any of which would let anyone forge admin
// tokens. When auth is disabled (local dev, ARTI_AUTH_DISABLED=true) no tokens
// are trusted, so every key is accepted, including the dev default.
func ValidateSigningKey(key string, authDisabled bool) error {
	if authDisabled {
		return nil
	}
	switch {
	case key == "":
		return errors.New("JWT_SIGNING_KEY is empty")
	case key == DevDefaultSigningKey:
		return errors.New("JWT_SIGNING_KEY is the in-repo dev default; set a real secret")
	case len(key) < MinSigningKeyLen:
		return fmt.Errorf("JWT_SIGNING_KEY is too short (%d bytes; need at least %d)", len(key), MinSigningKeyLen)
	}
	return nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

func (s *JWTSigner) Sign(c Claims) (string, error) {
	if c.IAT == 0 {
		c.IAT = time.Now().Unix()
	}
	if c.TTL != 0 {
		c.EXP = time.Now().Add(c.TTL).Unix()
	}
	if c.Sub == "" && c.Email != "" {
		h := sha256.Sum256([]byte(strings.ToLower(c.Email)))
		c.Sub = base64.RawURLEncoding.EncodeToString(h[:])
	}
	if c.JTI == "" {
		c.JTI = uuid.NewString()
	}
	hdr, _ := json.Marshal(jwtHeader{Alg: "HS256", Typ: "JWT"})
	body, _ := json.Marshal(c)
	h := base64.RawURLEncoding.EncodeToString(hdr)
	b := base64.RawURLEncoding.EncodeToString(body)
	signing := h + "." + b
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(signing))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signing + "." + sig, nil
}

func (s *JWTSigner) Verify(tok string) (Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Claims{}, ErrMalformed
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return Claims{}, ErrBadSignature
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: body decode: %v", ErrMalformed, err)
	}
	var c Claims
	if err := json.Unmarshal(body, &c); err != nil {
		return Claims{}, fmt.Errorf("%w: body json: %v", ErrMalformed, err)
	}
	if c.EXP != 0 && time.Now().Unix() > c.EXP {
		// The signature was already verified above, so the claims are
		// authentic — just stale. Return them alongside the error so callers
		// can attribute the rejection (log WHO is presenting an expired
		// token). Callers MUST still treat the token as unauthenticated.
		return c, ErrExpired
	}
	return c, nil
}
