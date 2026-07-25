package auth_test

import (
	"os"
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
)

// TestMain gives the suite an explicit email-domain allowlist. The binary
// ships with none (empty = deny everyone, fail closed), so tests that mint
// or verify identities use the reserved example domains configured here.
func TestMain(m *testing.M) {
	auth.SetAllowedDomains([]string{"example.com", "example.org"})
	os.Exit(m.Run())
}
