package auth

import (
	"strings"
	"sync"
)

// adminEmails is the configured set of admin email addresses. Stored
// lower-cased for case-insensitive comparison. Populated at startup
// from ARTI_ADMIN_EMAILS via SetAdminEmails. There is deliberately no
// baked-in default: with nothing configured there are no admins, which
// fails closed (admin-gated routes deny everyone).
var (
	adminEmailsMu sync.RWMutex
	adminEmails   = map[string]struct{}{}
)

// SetAdminEmails replaces the admin allowlist. Pass a comma-separated
// list of addresses; whitespace and empty entries are dropped. An empty
// list is treated as "no admins" — call SetAdminEmails(nil) to disable
// admin-gated routes entirely.
func SetAdminEmails(emails []string) {
	next := make(map[string]struct{}, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(strings.ToLower(e))
		if e != "" {
			next[e] = struct{}{}
		}
	}
	adminEmailsMu.Lock()
	defer adminEmailsMu.Unlock()
	adminEmails = next
}

// IsAdmin reports whether the given email is in the admin allowlist.
// Comparison is case-insensitive.
func IsAdmin(email string) bool {
	if email == "" {
		return false
	}
	e := strings.ToLower(email)
	adminEmailsMu.RLock()
	defer adminEmailsMu.RUnlock()
	_, ok := adminEmails[e]
	return ok
}

// AdminEmails returns a copy of the configured admin allowlist.
// Useful for /api/me and tests; do not mutate the result.
func AdminEmails() []string {
	adminEmailsMu.RLock()
	defer adminEmailsMu.RUnlock()
	out := make([]string, 0, len(adminEmails))
	for e := range adminEmails {
		out = append(out, e)
	}
	return out
}
