package main

import (
	"fmt"
	"net/url"
)

// OwnerCmd shows or transfers a document's owner. Ownership is claimed by a
// slug's first version and never moves on its own — not when someone else
// publishes a version, not when v1 is archived. It is what every document-level
// authority check resolves to: changing access, the comment switch, minting a
// share link.
//
//	arti owner my-doc                            # who owns it
//	arti owner my-doc --to alice@example.com     # hand it over
//	arti owner my-doc --to alice@… --no-keep-access
//
// Transferring grants the new owner read access on every version, so they are
// never locked out of what they now own, and grants the outgoing owner read and
// write unless --no-keep-access is passed. Current owner or an admin only.
type OwnerCmd struct {
	Slug string `arg:"" help:"slug"`
	To   string `help:"transfer ownership to this email address"`
	// Write access answers to the owner and has no creator fallback, so a
	// hand-off silently costs the outgoing owner write on a document they may
	// still be working in. Default on for that reason; it only ever adds.
	KeepAccess bool `name:"keep-access" negatable:"" default:"true" help:"also grant the outgoing owner read and write"`
}

func (o *OwnerCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}

	if o.To == "" {
		var meta map[string]any
		if err := c.DoJSON("GET", "/api/artifacts/by-slug/"+url.PathEscape(o.Slug), nil, &meta); err != nil {
			return err
		}
		owner, _ := meta["owner"].(string)
		if owner == "" {
			return fmt.Errorf("no owner reported for %q", o.Slug)
		}
		fmt.Fprintf(stderr(), "slug:  %s\nowner: %s\n", o.Slug, owner)
		return nil
	}

	var resp struct {
		Owner         string `json:"owner"`
		PreviousOwner string `json:"previous_owner"`
	}
	if err := c.DoJSON("POST", "/api/artifacts/by-slug/"+url.PathEscape(o.Slug)+"/owner",
		map[string]any{"owner": o.To, "keep_access": o.KeepAccess}, &resp); err != nil {
		return err
	}
	fmt.Fprintf(stderr(), "owner: %s (was %s)\n", resp.Owner, resp.PreviousOwner)
	return nil
}
