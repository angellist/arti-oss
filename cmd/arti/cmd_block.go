package main

import (
	"fmt"
	"net/url"
	"time"
)

// BlockCmd is the admin stop-gap: it takes a document, or a family of them,
// away from every reader without changing anything about the document. The
// versions, owner and access list stay exactly as they were, so `block rm`
// restores what was there.
//
//	arti block ls
//	arti block add leaked-doc --reason "shared externally"
//	arti block add 'mem--couch--*'
//	arti block rm leaked-doc
//
// A pattern matches a slug (or the artifact id of a slug-less artifact), with
// `*` as the only wildcard and case ignored. Requires MANAGE_ARTIFACTS.
type BlockCmd struct {
	Ls  BlockLsCmd  `cmd:"" default:"1" help:"list blocked patterns"`
	Add BlockAddCmd `cmd:"" help:"block everything matching a pattern"`
	Rm  BlockRmCmd  `cmd:"" help:"lift a blocked pattern"`
}

type BlockLsCmd struct{}

type blockEntry struct {
	Pattern   string    `json:"pattern"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

func (b *BlockLsCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	var out struct {
		Blocks []blockEntry `json:"blocks"`
	}
	if err := c.DoJSON("GET", "/api/admin/blocks", nil, &out); err != nil {
		return err
	}
	if len(out.Blocks) == 0 {
		fmt.Println("no blocked patterns")
		return nil
	}
	for _, e := range out.Blocks {
		fmt.Printf("%s\t%s\t%s\t%s\n", e.Pattern, e.CreatedBy, e.CreatedAt.Format(time.RFC3339), e.Reason)
	}
	return nil
}

type BlockAddCmd struct {
	Pattern string `arg:"" help:"slug, slug glob, or artifact UUID"`
	Reason  string `help:"why, for whoever reads the list later"`
}

func (b *BlockAddCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	body := map[string]any{"pattern": b.Pattern, "reason": b.Reason}
	if err := c.DoJSON("POST", "/api/admin/blocks", body, nil); err != nil {
		return err
	}
	fmt.Printf("blocked %s\n", b.Pattern)
	return nil
}

type BlockRmCmd struct {
	Pattern string `arg:"" help:"the pattern to lift, exactly as block ls prints it"`
}

func (b *BlockRmCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	if err := c.DoJSON("DELETE", "/api/admin/blocks?pattern="+url.QueryEscape(b.Pattern), nil, nil); err != nil {
		return err
	}
	fmt.Printf("lifted %s\n", b.Pattern)
	return nil
}
