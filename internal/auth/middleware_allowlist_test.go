package auth

import "testing"

// The allowlist is the gate the docs point self-hosters at, but it used to
// match domains only. Against a consumer IdP the smallest thing you could
// express was `gmail.com` — every Google account on the internet — so a
// single-user deployment had no way to say "just me". Full addresses are
// accepted alongside domains; a domain can never contain "@", so entries
// that were previously inert now mean exactly one person.
func TestIsAllowed_AddressesAndDomains(t *testing.T) {
	prev := AllowedDomains()
	t.Cleanup(func() { SetAllowedDomains(prev) })

	SetAllowedDomains([]string{"me@gmail.com", "Example.COM", "@example.org"})

	for _, tc := range []struct {
		email string
		want  bool
		why   string
	}{
		{"me@gmail.com", true, "exact address entry"},
		{"ME@Gmail.com", true, "address match is case-insensitive"},
		{"someone-else@gmail.com", false, "an address entry must not admit the whole provider"},
		{"anyone@example.com", true, "domain entry still admits the domain"},
		{"anyone@EXAMPLE.com", true, "domain match is case-insensitive"},
		{"anyone@example.org", true, "a leading @ on an entry is stripped"},
		{"nobody@elsewhere.com", false, "unlisted domain"},
		{"not-an-email", false, "no @ at all"},
		{"", false, "empty"},
	} {
		if got := IsAllowed(tc.email); got != tc.want {
			t.Errorf("IsAllowed(%q) = %v, want %v — %s", tc.email, got, tc.want, tc.why)
		}
	}
}

// IsAllowed is the one gate all fourteen auth paths share, but only the two
// interactive login handlers trimmed the email before calling it — the rest
// pass a claim, a DB column, or a token field straight through. Normalizing
// inside the gate keeps a domain entry and an address entry agreeing about
// the same identity, which they did not while trimming was the caller's job.
func TestIsAllowed_NormalizesUntrimmedInput(t *testing.T) {
	prev := AllowedDomains()
	t.Cleanup(func() { SetAllowedDomains(prev) })

	SetAllowedDomains([]string{"alice@example.com", "other.example"})

	for _, email := range []string{
		" alice@example.com",
		"alice@example.com ",
		"\talice@example.com\n",
		"  ALICE@Example.COM  ",
	} {
		if !IsAllowed(email) {
			t.Errorf("IsAllowed(%q) = false, want true — surrounding whitespace is not an identity", email)
		}
	}
	// The domain half has to agree with the address half on the same input.
	if !IsAllowed(" bob@other.example ") {
		t.Error("untrimmed email must still match a domain entry")
	}
	// Normalizing must not turn a non-match into a match.
	if IsAllowed("  eve@elsewhere.test  ") {
		t.Error("trimming must not admit an unlisted identity")
	}
}

// Entries match exactly — never as a suffix, a prefix, or a pattern. This
// is the property that makes the allowlist safe to widen, so it is pinned
// here rather than left as an emergent consequence of using "==": a suffix
// match on example.com would admit evil-example.com, which is the standard
// way origin and cookie-domain allowlists get broken. If someone ever needs
// subdomains or wildcards, that has to be a new, explicitly-named mechanism
// — not a loosening of this comparison.
func TestIsAllowed_MatchesExactlyNeverBySuffix(t *testing.T) {
	prev := AllowedDomains()
	t.Cleanup(func() { SetAllowedDomains(prev) })

	SetAllowedDomains([]string{"example.com", "alice@example.com"})

	for _, tc := range []struct {
		email string
		why   string
	}{
		{"eve@evil-example.com", "a domain ending in the allowed one is a different domain"},
		{"eve@notexample.com", "no leading separator is still a different domain"},
		{"eve@example.com.evil.test", "the allowed domain as a prefix is a different domain"},
		{"eve@sub.example.com", "subdomains are not implied by the parent domain"},
		{"eve@example.co", "a truncation is a different domain"},
		{"alice@example.com.evil.test", "an allowed address as a prefix is a different address"},
	} {
		if IsAllowed(tc.email) {
			t.Errorf("IsAllowed(%q) = true, want false — %s", tc.email, tc.why)
		}
	}

	// The exact forms still work, so the above is not passing by accident.
	for _, ok := range []string{"anyone@example.com", "alice@example.com"} {
		if !IsAllowed(ok) {
			t.Errorf("IsAllowed(%q) = false, want true", ok)
		}
	}
}

// Fail-closed is the documented default and the reason an empty allowlist
// is safe to ship: it admits nobody rather than everybody.
func TestIsAllowed_EmptyAllowlistAdmitsNobody(t *testing.T) {
	prev := AllowedDomains()
	t.Cleanup(func() { SetAllowedDomains(prev) })

	SetAllowedDomains(nil)
	if IsAllowed("anyone@example.com") {
		t.Error("empty allowlist admitted an email")
	}
	SetAllowedDomains([]string{"  ", "@", ""})
	if IsAllowed("anyone@example.com") {
		t.Error("blank entries must normalize away, leaving nobody admitted")
	}
}

// Doctor classifies entries by whether they carry an "@", so it has to see
// exactly what IsAllowed matches against — hence the shared normalizer.
func TestNormalizeAllowlist(t *testing.T) {
	got := NormalizeAllowlist([]string{" Me@Gmail.com ", "@Example.COM", "", "   ", "example.org"})
	want := []string{"me@gmail.com", "example.com", "example.org"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeAllowlist() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}
