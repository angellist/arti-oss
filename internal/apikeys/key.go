// Package apikeys implements self-serve, scope-generic API keys: an opaque
// "arti_{scope}_…" bearer a user mints to give a tool/agent scoped, non-
// interactive access to arti. arti stores only sha256(key); plaintext is shown
// once. v1 supports only the "upload" scope.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/angellist/arti-oss/internal/auth"
)

const keyBytes = 32

// SupportedScopes is the mint + auth allowlist. Adding a scope requires shipping
// its enforcement too (see design note B). Order-insensitive membership only.
var SupportedScopes = []string{auth.UploadScope}

// IsSupportedScope reports whether s is in SupportedScopes.
func IsSupportedScope(s string) bool {
	for _, v := range SupportedScopes {
		if v == s {
			return true
		}
	}
	return false
}

// GenerateKey returns a new key for a single scope: plaintext (give to the user
// once), sha256(plaintext) (store), and a display prefix (store + show).
func GenerateKey(scope string) (plaintext string, hash []byte, prefix string, err error) {
	b := make([]byte, keyBytes)
	if _, err = rand.Read(b); err != nil {
		return "", nil, "", err
	}
	rnd := base64.RawURLEncoding.EncodeToString(b)
	plaintext = auth.APIKeyPrefix + scope + "_" + rnd
	h := sha256.Sum256([]byte(plaintext))
	prefix = auth.APIKeyPrefix + scope + "_" + rnd[:5]
	return plaintext, h[:], prefix, nil
}

// HashKey returns sha256(token). Use this to look up a key in the registry.
func HashKey(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
