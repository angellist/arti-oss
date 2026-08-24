package main

import (
	"fmt"
	"net/url"
	"strings"
)

// AccessCmd shows or edits an artifact's access control without minting a new
// version. Access is a property of the DOCUMENT (DD-0055): an edit made
// through any version applies to every version of the slug, and only the
// slug's owner (its earliest version's creator) or an admin may change it.
//
//	arti access my-doc                                   # show current access
//	arti access my-doc --access '*@example.com'          # domain-readable
//	arti access my-doc --access alice@x --access group:eng
//	arti access my-doc --private                         # creator-only
//	arti access my-doc --write-access group:eng          # readers stay, group writes
//	arti access my-doc --write-private                   # creator-only writes
//
// Flags you omit leave that side untouched (a --write-access edit doesn't
// resend read access, and vice versa).
type AccessCmd struct {
	Ident string `arg:"" help:"UUID or slug"`

	// Read side. Mirrors `arti add`: repeatable patterns, or --private for
	// the explicit-empty (creator-only) shape the repeatable flag can't say.
	Access  []string `help:"replace read access (repeatable; glob-on-email, 'group:<name>', '*' = everyone). Omit to keep current."`
	Private bool     `help:"creator-only reads (sends an explicit empty list). Mutually exclusive with --access."`

	// Write side. The subset of readers who may push versions / append /
	// edit; the server unions it into read access (a writer can always read).
	WriteAccess  []string `name:"write-access" help:"replace write access (repeatable; subset of readers). Omit to keep current."`
	WritePrivate bool     `name:"write-private" help:"creator-only writes; readers stay read-only. Mutually exclusive with --write-access."`
}

func (a *AccessCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}

	payload := map[string]any{}
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

	// Resolve a slug to its latest version's UUID; a UUID PATCHes directly.
	// Either way the server applies the ACL slug-wide.
	id := a.Ident
	var meta map[string]any
	if !isUUID(a.Ident) {
		if err := c.DoJSON("GET", "/api/artifacts/by-slug/"+url.PathEscape(a.Ident), nil, &meta); err != nil {
			return fmt.Errorf("resolve slug %q: %w", a.Ident, err)
		}
		s, _ := meta["artifact_id"].(string)
		if s == "" {
			return fmt.Errorf("resolve slug %q: no artifact_id in response", a.Ident)
		}
		id = s
	}

	// No flags → read-only: show the current pair and exit. By UUID the
	// metadata lives on the /meta route — the bare /api/artifacts/{id}
	// route streams the artifact's CONTENT.
	if len(payload) == 0 {
		if meta == nil {
			if err := c.DoJSON("GET", "/api/artifacts/"+url.PathEscape(id)+"/meta", nil, &meta); err != nil {
				return err
			}
		}
		printAccess(meta)
		return nil
	}

	var updated map[string]any
	if err := c.DoJSON("PATCH", "/api/artifacts/"+url.PathEscape(id), payload, &updated); err != nil {
		return err
	}
	fmt.Fprintln(stderr(), "access updated — applies to ALL versions of the document")
	printAccess(updated)
	return nil
}

// printAccess renders the (allowed_access, allowed_write) pair the way the
// API reports it: read patterns, then either the explicit write list or the
// mirror-mode note (write follows read).
func printAccess(meta map[string]any) {
	slug, _ := meta["named_slug"].(string)
	if slug != "" {
		fmt.Fprintf(stderr(), "slug:  %s\n", slug)
	}
	fmt.Fprintf(stderr(), "read:  %s\n", renderPatterns(meta["allowed_access"]))
	if meta["allowed_write"] == nil {
		fmt.Fprintf(stderr(), "write: (follows read)\n")
	} else {
		fmt.Fprintf(stderr(), "write: %s\n", renderPatterns(meta["allowed_write"]))
	}
}

func renderPatterns(v any) string {
	raw, _ := v.([]any)
	if len(raw) == 0 {
		return "(creator-only)"
	}
	parts := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}
