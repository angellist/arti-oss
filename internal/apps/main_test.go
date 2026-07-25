package apps_test

import (
	"os"
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
)

// TestMain configures an explicit email-domain allowlist: the proxy re-checks
// auth.IsAllowed per call, and the binary ships with an empty (deny-all)
// allowlist, so the suite grants the reserved example domain.
func TestMain(m *testing.M) {
	auth.SetAllowedDomains([]string{"example.com"})
	os.Exit(m.Run())
}
