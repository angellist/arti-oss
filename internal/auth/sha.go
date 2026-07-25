package auth

import "crypto/sha256"

// _sha is a 1-line wrapper extracted to its own file so the
// crypto/sha256 import doesn't have to land alongside the other
// stdlib imports in oidc.go (keeps the auth package re-export tidy).
func _sha(b []byte) [32]byte { return sha256.Sum256(b) }
