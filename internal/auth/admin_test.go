package auth_test

import (
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
)

func TestIsAdmin_Default(t *testing.T) {
	// Restore the default at the end of the test so order doesn't matter.
	t.Cleanup(func() { auth.SetAdminEmails([]string{"admin@example.com"}) })

	auth.SetAdminEmails([]string{"admin@example.com"})

	cases := []struct {
		email string
		want  bool
	}{
		{"admin@example.com", true},
		{"Admin@example.com", true}, // case-insensitive
		{"someone.else@example.com", false},
		{"", false},
		{"someone@example.org", false},
	}
	for _, c := range cases {
		t.Run(c.email, func(t *testing.T) {
			if got := auth.IsAdmin(c.email); got != c.want {
				t.Errorf("IsAdmin(%q) = %v, want %v", c.email, got, c.want)
			}
		})
	}
}

func TestSetAdminEmails_Replaces(t *testing.T) {
	t.Cleanup(func() { auth.SetAdminEmails([]string{"admin@example.com"}) })

	auth.SetAdminEmails([]string{"alice@example.com", "bob@example.com", ""})
	if !auth.IsAdmin("alice@example.com") {
		t.Error("expected alice to be admin after SetAdminEmails")
	}
	if !auth.IsAdmin("bob@example.com") {
		t.Error("expected bob to be admin after SetAdminEmails")
	}
	if auth.IsAdmin("admin@example.com") {
		t.Error("expected tian to NOT be admin after replacement")
	}
	emails := auth.AdminEmails()
	if len(emails) != 2 {
		t.Errorf("expected 2 emails, got %d: %v", len(emails), emails)
	}
}

func TestSetAdminEmails_Empty(t *testing.T) {
	t.Cleanup(func() { auth.SetAdminEmails([]string{"admin@example.com"}) })

	auth.SetAdminEmails(nil)
	if auth.IsAdmin("admin@example.com") {
		t.Error("expected no admins after SetAdminEmails(nil)")
	}
}
