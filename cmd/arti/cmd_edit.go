package main

import (
	"fmt"
	"net/url"
	"strings"
)

// EditCmd edits an artifact's metadata in place — title, description, labels,
// scopes, and the per-document comment switch — WITHOUT minting a new version.
// It is the CLI's half of the same PATCH the web editor and the MCP
// `update_artifact` tool use; the ACL pair lives in its own command
// (`arti access`) because access is document-wide and has its own safety rules.
//
//	arti edit my-doc                                 # show what's editable
//	arti edit my-doc --title 'Q3 treasury review'
//	arti edit my-doc --description 'what changed and why'
//	arti edit my-doc --description ''                # clear the description
//	arti edit my-doc --label report --label treasury # REPLACES the label set
//	arti edit my-doc --clear-labels
//	arti edit my-doc --scope a:bt-auto-route
//	arti edit my-doc --no-comments                   # owner/admin only
//
// Fields you omit are left untouched. --label/--scope replace the whole set
// (the server has no add/remove verb), so carry forward the labels you want to
// keep — including a structural one like `skill` or `kind:*`, which
// type-scoped listings filter on.
//
// Title/description/labels/scopes are PER-VERSION: the edit lands on the one
// version named by IDENT (a slug resolves to its latest version unless you
// pass --version). comments_enabled is per-DOCUMENT and only the slug's owner
// (or an admin) may set it.
type EditCmd struct {
	Ident   string `arg:"" help:"UUID or slug"`
	Version int    `short:"v" help:"version to edit (slug only; default: latest)"`

	// Pointers, not strings: kong leaves them nil when the flag is absent, so
	// `--description ''` is distinguishable from "didn't pass --description"
	// and can clear the field the way the API and MCP both allow.
	Title *string `help:"replace the title"`
	// Kong strips quote characters out of help strings, so the empty-string
	// hint is spelled in words rather than as --description ''.
	Description *string `help:"replace the description; pass an empty string to clear it"`

	// Repeatable set-flags replace rather than merge, mirroring the API. The
	// empty set is the one shape a repeatable flag can't express, so it gets a
	// dedicated --clear-* flag, exactly as --private does for access.
	Label       []string `help:"replace labels (repeatable). Omit to keep current."`
	ClearLabels bool     `name:"clear-labels" help:"remove all labels. Mutually exclusive with --label."`
	Scope       []string `help:"replace scopes (repeatable, e.g. a:bt-auto-route). Omit to keep current."`
	ClearScopes bool     `name:"clear-scopes" help:"remove all scopes. Mutually exclusive with --scope."`

	Comments *bool `negatable:"" help:"turn commenting on/off for the whole document (owner or admin only)"`
}

func (e *EditCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}

	payload := map[string]any{}
	if e.Title != nil {
		t := strings.TrimSpace(*e.Title)
		if t == "" {
			return fmt.Errorf("--title cannot be empty (a title is required; there is nothing to clear it to)")
		}
		payload["title"] = t
	}
	if e.Description != nil {
		payload["description"] = *e.Description
	}
	switch {
	case e.ClearLabels:
		if len(e.Label) > 0 {
			return fmt.Errorf("--clear-labels and --label are mutually exclusive")
		}
		payload["labels"] = []string{}
	case len(e.Label) > 0:
		payload["labels"] = e.Label
	}
	switch {
	case e.ClearScopes:
		if len(e.Scope) > 0 {
			return fmt.Errorf("--clear-scopes and --scope are mutually exclusive")
		}
		payload["scopes"] = []string{}
	case len(e.Scope) > 0:
		payload["scopes"] = e.Scope
	}
	if e.Comments != nil {
		payload["comments_enabled"] = *e.Comments
	}

	// Resolve the target: a UUID edits that exact version, a slug resolves to
	// the version named by --version (latest when unset). PATCH itself is
	// UUID-only, which is why the resolution happens here rather than server-
	// side — the same thing `arti access` does.
	meta, err := fetchMeta(c, e.Ident, e.Version)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", e.Ident, err)
	}
	if meta.ArtifactID == "" {
		return fmt.Errorf("resolve %q: no artifact_id in response", e.Ident)
	}

	// No flags → read-only. Show what this command can change and say plainly
	// that nothing was written, rather than firing an empty PATCH.
	if len(payload) == 0 {
		printEditable(meta)
		fmt.Fprintln(stderr(), "(no flags given — nothing changed; see `arti edit --help`)")
		return nil
	}

	var updated metaResp
	if err := c.DoJSON("PATCH", "/api/artifacts/"+url.PathEscape(meta.ArtifactID), payload, &updated); err != nil {
		return err
	}
	if _, ok := payload["comments_enabled"]; ok {
		fmt.Fprintln(stderr(), "comments setting applies to ALL versions of the document")
	}
	printEditable(updated)
	return nil
}

// printEditable renders the fields `arti edit` can change, so a before/after
// run reads the same way. Access is deliberately absent — `arti access` owns
// that pair and prints it in its own shape.
func printEditable(m metaResp) {
	fmt.Fprintf(stderr(), "artifact_id: %s\n", m.ArtifactID)
	if m.NamedSlug != nil {
		v := "-"
		if m.Version != nil {
			v = fmt.Sprintf("v%d", *m.Version)
		}
		fmt.Fprintf(stderr(), "slug/ver:    %s (%s)\n", *m.NamedSlug, v)
	}
	fmt.Fprintf(stderr(), "title:       %s\n", m.Title)
	desc := "(none)"
	if m.Description != nil && *m.Description != "" {
		desc = *m.Description
	}
	fmt.Fprintf(stderr(), "description: %s\n", desc)
	fmt.Fprintf(stderr(), "labels:      %s\n", joinOrNone(m.Labels))
	fmt.Fprintf(stderr(), "scopes:      %s\n", joinOrNone(m.Scopes))
	if m.CommentsEnabled != nil {
		state := "on"
		if !*m.CommentsEnabled {
			state = "off"
		}
		fmt.Fprintf(stderr(), "comments:    %s\n", state)
	}
}

func joinOrNone(v []string) string {
	if len(v) == 0 {
		return "(none)"
	}
	return strings.Join(v, ", ")
}
