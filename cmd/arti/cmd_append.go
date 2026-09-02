package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
)

// AppendCmd appends text to an existing slug, atomically (the server
// reads the latest version, concatenates separator + new content, and
// writes a new version under a (slug, version) unique constraint).
// Auto-creates v1 if the slug doesn't exist; in that path --title and
// --content-type are required to seed the artifact.
//
// Usage:
//
//	arti append --slug foo file.md            # append file contents
//	arti append --slug foo -                  # append stdin
//	echo "entry" | arti append --slug foo     # append stdin (shorthand)
//	arti append --slug foo file.md --idempotency-key $(uuidgen)
//
// Recommended for agent-driven writes where retries may double-fire:
// pass --idempotency-key with a stable hash of the logical event (e.g.
// a Front comment id) and the server will dedupe within a 24h window.
type AppendCmd struct {
	File           string `arg:"" optional:"" help:"file to append, or '-' / omitted for stdin"`
	Slug           string `required:"" help:"named slug to append to"`
	Separator      string `help:"separator between prior body and new content (default \"\\n\\n\"); pass --separator='' for none"`
	IdempotencyKey string `name:"idempotency-key" help:"dedup key (24h TTL on the server); recommended for agent retries"`
	AutoKey        bool   `name:"auto-key" help:"compute idempotency-key = sha256(slug + content) (mutually exclusive with --idempotency-key)"`
	// Compress gzips the request body so the Cloudflare WAF can't false-match
	// its <script> rule on appended HTML/JS. Append bodies are always UTF-8
	// text, so "auto" (the default) always compresses; "none" disables it.
	Compress string `name:"compress" enum:"auto,gzip,none" default:"auto" help:"gzip upload body: auto | gzip | none"`

	// Seed-only fields. Required if the slug doesn't exist yet (auto-
	// create v1); ignored on subsequent appends unless --override is set.
	Title       string `help:"title (required on auto-create; optional override)"`
	Description string `help:"description (optional override)"`
	ContentType string `name:"content-type" help:"MIME (required on auto-create; optional override; text/* and structured-text only)"`

	// Metadata overrides for the new version. Mirror `arti add`.
	Scope  []string `help:"scope (repeatable). Inherited from prior version if omitted."`
	Label  []string `help:"label (repeatable). Inherited from prior version if omitted."`
	Access []string `help:"access pattern (repeatable; glob-on-email / 'group:<name>'; '*' = everyone). Inherited if omitted."`
	// Private / write flags mirror `arti add`; note an ACL that differs from
	// the slug's current pair applies to ALL versions (owner/admin only).
	Private      bool     `help:"creator-only reads (explicit empty list). Mutually exclusive with --access."`
	WriteAccess  []string `name:"write-access" help:"write-access pattern (repeatable; subset of readers). Inherited if omitted."`
	WritePrivate bool     `name:"write-private" help:"creator-only writes; readers stay read-only. Mutually exclusive with --write-access."`
}

func (a *AppendCmd) Run(cli *CLI) error {
	if a.AutoKey && a.IdempotencyKey != "" {
		return fmt.Errorf("--auto-key and --idempotency-key are mutually exclusive")
	}

	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}

	var content []byte
	if a.File == "" || a.File == "-" {
		b, rerr := io.ReadAll(os.Stdin)
		if rerr != nil {
			return rerr
		}
		content = b
	} else {
		b, rerr := os.ReadFile(a.File) // #nosec G304 — user input
		if rerr != nil {
			return rerr
		}
		content = b
	}
	if len(content) == 0 {
		return fmt.Errorf("nothing to append (empty content)")
	}

	idemKey := a.IdempotencyKey
	if a.AutoKey {
		sum := sha256.Sum256(append([]byte(a.Slug+"\x00"), content...))
		idemKey = hex.EncodeToString(sum[:])
	}

	payload := map[string]any{"content": string(content)}
	// Separator is optional — only emit if user provided one explicitly,
	// even if empty. Kong's zero value is "" which we can't distinguish
	// from "user passed --separator=''", so for now: --separator must be
	// non-empty to take effect. The default ("\n\n") is server-side.
	if a.Separator != "" {
		payload["separator"] = a.Separator
	}
	if idemKey != "" {
		payload["idempotency_key"] = idemKey
	}
	if a.Title != "" {
		payload["title"] = a.Title
	}
	if a.Description != "" {
		payload["description"] = a.Description
	}
	if a.ContentType != "" {
		payload["content_type"] = a.ContentType
	}
	if len(a.Scope) > 0 {
		payload["scopes"] = a.Scope
	}
	if len(a.Label) > 0 {
		payload["labels"] = a.Label
	}
	switch {
	case a.Private:
		if len(a.Access) > 0 {
			return fmt.Errorf("--private and --access are mutually exclusive")
		}
		payload["allowed_access"] = []string{} // creator-only
	case len(a.Access) > 0:
		payload["allowed_access"] = a.Access
	}
	switch {
	case a.WritePrivate:
		if len(a.WriteAccess) > 0 {
			return fmt.Errorf("--write-private and --write-access are mutually exclusive")
		}
		payload["allowed_write"] = []string{} // creator-only writes
	case len(a.WriteAccess) > 0:
		payload["allowed_write"] = a.WriteAccess
	}

	var resp map[string]any
	endpoint := "/api/artifacts/by-slug/" + url.PathEscape(a.Slug) + "/append"
	// Append bodies are always UTF-8 text; treat an unset content type as text
	// so "auto" compresses (the WAF rule fires on any body carrying <script>).
	ct := a.ContentType
	if ct == "" {
		ct = "text/plain"
	}
	post := c.DoJSON
	if gzipUpload(a.Compress, ct) {
		post = c.DoJSONGzip
	}
	if err := post("POST", endpoint, payload, &resp); err != nil {
		return err
	}

	// metadata to stderr, URL to stdout (pipe-friendly), same convention as `arti add`.
	fmt.Fprintf(stderr(), "artifact_id: %v\n", resp["artifact_id"])
	if slug, ok := resp["named_slug"].(string); ok && slug != "" {
		fmt.Fprintf(stderr(), "slug:        %s (v%v)\n", slug, resp["version"])
	}
	if u, ok := resp["url"].(string); ok {
		fmt.Println(u)
	}
	if idemKey != "" {
		fmt.Fprintf(stderr(), "idempotency_key: %s\n", idemKey)
	}
	return nil
}
