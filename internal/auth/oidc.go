package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ErrForbidden is returned by OIDCVerifier when the user is authenticated
// but lacks the required group membership or fails the domain allowlist.
// The middleware translates this into HTTP 403.
var ErrForbidden = errors.New("auth: forbidden")

// OIDCConfig configures the Dex OIDC verifier. Mirrors ace's
// AUTH_DEX_ISSUER_URL / AUTH_JWT_AUDIENCE / AUTH_REQUIRED_GROUPS.
type OIDCConfig struct {
	IssuerURL      string   // e.g. https://dex.example.com
	Audience       string   // e.g. "auth"
	RequiredGroups []string // empty = no group check
}

// OIDCVerifier wraps a go-oidc IDTokenVerifier with arti's claim shape
// + group-membership policy. Construct with NewOIDCVerifier; calls to
// Verify are concurrency-safe.
type OIDCVerifier struct {
	verifier *oidc.IDTokenVerifier
	required []string
}

// NewOIDCVerifier fetches Dex's JWKS at startup (one network call) and
// returns a verifier ready for per-request use. Returns nil + nil if
// IssuerURL is empty — callers can treat the OIDC path as optional.
func NewOIDCVerifier(ctx context.Context, cfg OIDCConfig) (*OIDCVerifier, error) {
	if cfg.IssuerURL == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prov, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: provider discovery: %w", err)
	}
	v := prov.Verifier(&oidc.Config{ClientID: cfg.Audience})
	return &OIDCVerifier{verifier: v, required: cfg.RequiredGroups}, nil
}

// Verify checks the supplied JWT against Dex's issuer + signing keys,
// enforces the email domain + group policy, and returns the verified
// claims. Returns ErrForbidden when the token is valid but the caller
// is outside the allowed groups / domain.
func (v *OIDCVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	if v == nil || v.verifier == nil {
		return Claims{}, errors.New("oidc: not configured")
	}
	tok, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return Claims{}, fmt.Errorf("oidc: verify: %w", err)
	}
	var inner struct {
		Email    string   `json:"email"`
		Username string   `json:"preferred_username"`
		Groups   []string `json:"groups"`
		Sub      string   `json:"sub"`
		Name     string   `json:"name"`
		Picture  string   `json:"picture"`
	}
	if err := tok.Claims(&inner); err != nil {
		return Claims{}, fmt.Errorf("oidc: claims: %w", err)
	}
	if inner.Email == "" {
		return Claims{}, fmt.Errorf("oidc: token has no email")
	}
	if !IsAllowed(inner.Email) {
		return Claims{}, ErrForbidden
	}
	if len(v.required) > 0 && !hasAnyGroup(inner.Groups, v.required) {
		return Claims{}, ErrForbidden
	}
	return Claims{
		Email:   inner.Email,
		Sub:     inner.Sub,
		Name:    inner.Name,
		Picture: inner.Picture,
		Scopes:  inner.Groups, // ace uses groups as "scopes" downstream
	}, nil
}

func hasAnyGroup(have, required []string) bool {
	for _, g := range required {
		for _, h := range have {
			if strings.EqualFold(g, h) {
				return true
			}
		}
	}
	return false
}

// HashEqual compares a runtime-provided string to a pre-computed SHA-256
// using constant time. Used for the service-secret header path.
func HashEqual(provided string, expected [32]byte) bool {
	if provided == "" {
		return false
	}
	h := sha256Of([]byte(provided))
	return subtle.ConstantTimeCompare(h[:], expected[:]) == 1
}

// sha256Of is a tiny helper to keep the stdlib import local to this file.
func sha256Of(b []byte) [32]byte { return _sha(b) }
