package comments

import (
	"strings"
	"testing"
)

func TestParseMentions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"none", "just a comment", nil},
		{"one", "@alice@example.com take a look", []string{"alice@example.com"}},
		{"at start of body", "@alice@example.com", []string{"alice@example.com"}},
		{"two", "@alice@example.com and @bob@example.com", []string{"alice@example.com", "bob@example.com"}},
		{"lower-cased", "@Alice.B@Example.COM", []string{"alice.b@example.com"}},
		{"deduped, first order kept", "@bob@x.com @alice@x.com @bob@x.com", []string{"bob@x.com", "alice@x.com"}},
		{"plus and dots in local part", "@a.b+tag_c@sub.example.co.uk", []string{"a.b+tag_c@sub.example.co.uk"}},

		// A bare address is not a mention. Requiring the marker is what keeps a
		// comment that merely QUOTES an email ("forwarded from bob@x.com") from
		// DMing that person — and, in the other direction, keeps the second `@`
		// of a bare address from reading as a mention of its domain.
		{"bare address is not a mention", "forwarded from bob@example.com", nil},
		{"no boundary before the marker", "x@bob@example.com", nil},

		// Sentence punctuation must not be swallowed into the address: the
		// mention is alice@example.com, and "example.com." is not a domain.
		{"trailing period", "ping @alice@example.com.", []string{"alice@example.com"}},
		{"trailing comma", "@alice@example.com, thoughts?", []string{"alice@example.com"}},
		{"parenthesised", "(@alice@example.com)", []string{"alice@example.com"}},

		{"needs a dotted domain", "@alice@localhost please", nil},
		{"marker alone", "email me @ home", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMentions(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("parseMentions(%q) = %v, want %v", tc.body, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseMentions(%q) = %v, want %v", tc.body, got, tc.want)
				}
			}
		})
	}
}

// A pasted address block must not turn one comment into an unbounded fan-out of
// Slack lookups and DMs.
func TestParseMentionsCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxMentions*3; i++ {
		b.WriteString("@user")
		b.WriteByte(byte('a' + i%26))
		b.WriteString(string(rune('0'+i/26)) + "@example.com ")
	}
	if got := parseMentions(b.String()); len(got) != maxMentions {
		t.Fatalf("got %d mentions, want the %d cap", len(got), maxMentions)
	}
}
