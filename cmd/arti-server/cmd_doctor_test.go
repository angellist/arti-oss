package main

import (
	"context"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/config"
)

// The access gate has to say something true about who can actually sign in,
// for any allowlist, without a hardcoded notion of which domains are
// "public" — such a list is incomplete by construction, and the domains it
// missed would get a clean pass that reads as "checked, fine".
func TestCheckAccessGate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      config.Auth
		wantAttn bool     // printed with "-" (attention) rather than "✓"
		contains []string // every fragment must appear
		absent   string   // must NOT appear
	}{
		{
			name:     "empty admits nobody",
			cfg:      config.Auth{},
			wantAttn: true,
			contains: []string{"AUTH_ALLOWED_EMAILS", "no interactive login can succeed"},
		},
		{
			name:     "a domain entry says it admits everyone there",
			cfg:      config.Auth{AllowedEmails: []string{"gmail.com"}},
			contains: []string{"1 domain(s)", "EVERY account", "gmail.com"},
		},
		{
			// The same sentence has to land for a domain no curated list
			// would ever contain — that is the whole point of not curating.
			name:     "an obscure provider gets the identical treatment",
			cfg:      config.Auth{AllowedEmails: []string{"seznam.cz"}},
			contains: []string{"EVERY account", "seznam.cz"},
		},
		{
			name:     "addresses are reported as exactly those people",
			cfg:      config.Auth{AllowedEmails: []string{"me@gmail.com"}},
			contains: []string{"1 address(es)", "exactly me@gmail.com"},
			absent:   "EVERY account", // no domain entry, so no blanket admission
		},
		{
			name:     "mixed list reports both halves",
			cfg:      config.Auth{AllowedEmails: []string{"example.com", "guest@partner.example"}},
			contains: []string{"1 address(es)", "guest@partner.example", "1 domain(s)", "example.com"},
		},
		{
			name:     "auth disabled is called out, not passed over",
			cfg:      config.Auth{Disabled: true},
			wantAttn: true,
			contains: []string{"auth disabled"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, attn, err := checkAccessGate(context.Background(), &config.Config{Auth: tc.cfg})
			if err != nil {
				t.Fatalf("checkAccessGate: %v", err)
			}
			if attn != tc.wantAttn {
				t.Errorf("needs-attention = %v, want %v (detail=%q)", attn, tc.wantAttn, detail)
			}
			for _, want := range tc.contains {
				if !strings.Contains(detail, want) {
					t.Errorf("detail = %q, want it to mention %q", detail, want)
				}
			}
			if tc.absent != "" && strings.Contains(detail, tc.absent) {
				t.Errorf("detail = %q, must not mention %q", detail, tc.absent)
			}
		})
	}
}

// Every standard issuer puts the client ID in `aud`, so a divergence is
// almost always a misconfiguration — but only for raw ID-token bearer auth,
// and the note must say so rather than implying login is broken.
func TestCheckIssuerFlagsAudienceMismatch(t *testing.T) {
	cfg := &config.Config{Auth: config.Auth{
		Mode: "oidc", ClientID: "abc.apps.googleusercontent.com",
		ClientSecret: "shh", Audience: "auth",
	}}
	detail := issuerAudienceNote(cfg)
	if !strings.Contains(detail, "AUTH_JWT_AUDIENCE") || !strings.Contains(detail, "AUTH_OIDC_CLIENT_ID") {
		t.Errorf("note = %q, want both variables named", detail)
	}
	if !strings.Contains(detail, "interactive login is unaffected") {
		t.Errorf("note = %q, want the blast radius stated", detail)
	}

	cfg.Auth.Audience = cfg.Auth.ClientID
	if got := issuerAudienceNote(cfg); got != "" {
		t.Errorf("matching audience must produce no note, got %q", got)
	}
}
