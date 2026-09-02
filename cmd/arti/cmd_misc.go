package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// GetCmd fetches an artifact (or a single file inside a PACKAGE).
//
//	arti get UUID                  # content to stdout, meta to stderr
//	arti get UUID -q               # content only (pipe-friendly)
//	arti get UUID -m               # meta only
//	arti get UUID -v 3             # specific version (slug only)
//	arti get UUID --extract ./out  # PACKAGE → unzipped to ./out
//	arti get UUID/path/inside.md   # single entry from a PACKAGE
type GetCmd struct {
	Ident       string `arg:"" help:"UUID or slug; UUID/path/in/zip for single entry"`
	Version     int    `short:"v" help:"version (slug only)"`
	ContentOnly bool   `short:"q" name:"content-only" help:"skip meta header"`
	MetaOnly    bool   `short:"m" name:"meta-only"`
	Extract     string `name:"extract" help:"PACKAGE: unzip to DIR"`
}

func (g *GetCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}

	ident, sub := splitIdentPath(g.Ident)
	if sub != "" {
		return getPackageEntry(c, ident, sub, g.Version)
	}

	meta, err := fetchMeta(c, ident, g.Version)
	if err != nil {
		return err
	}
	if !g.ContentOnly {
		printMetaToStderr(meta)
	}
	if g.MetaOnly {
		return nil
	}

	// Extract path: PACKAGE only.
	if g.Extract != "" {
		if meta.ArtifactType != "PACKAGE" {
			return fmt.Errorf("--extract requires PACKAGE artifact")
		}
		return extractPackage(c, meta.ArtifactID, g.Extract)
	}

	rawPath := "/api/artifacts/" + meta.ArtifactID
	body, _, err := c.GetRaw(rawPath)
	if err != nil {
		return err
	}
	_, _ = os.Stdout.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		fmt.Println()
	}
	return nil
}

func getPackageEntry(c *Client, ident, sub string, version int) error {
	var path string
	if isUUID(ident) {
		path = "/api/artifacts/" + ident + "/files/" + sub
	} else {
		qs := ""
		if version > 0 {
			qs = fmt.Sprintf("?version=%d", version)
		}
		path = "/api/artifacts/by-slug/" + url.PathEscape(ident) + "/files/" + sub + qs
	}
	body, _, err := c.GetRaw(path)
	if err != nil {
		return err
	}
	_, _ = os.Stdout.Write(body)
	return nil
}

type metaResp struct {
	ArtifactID   string   `json:"artifact_id"`
	ArtifactType string   `json:"artifact_type"`
	Title        string   `json:"title"`
	Description  *string  `json:"description"`
	ContentType  string   `json:"content_type"`
	Version      *int32   `json:"version"`
	NamedSlug    *string  `json:"named_slug"`
	SizeBytes    *int64   `json:"size_bytes"`
	SHA256       *string  `json:"sha256"`
	Scopes       []string `json:"scopes"`
	Labels       []string `json:"labels"`
	// Pointer so "the server didn't say" is distinguishable from "off" —
	// `arti edit` reports the comment switch and shouldn't claim it is
	// disabled when talking to a server that omits the field.
	CommentsEnabled *bool  `json:"comments_enabled"`
	CreatedAt       string `json:"created_at"`
	Creator         string `json:"creator"`
	URL             string `json:"url"`
}

func fetchMeta(c *Client, ident string, version int) (metaResp, error) {
	var m metaResp
	if isUUID(ident) {
		return m, c.DoJSON("GET", "/api/artifacts/"+ident+"/meta", nil, &m)
	}
	qs := ""
	if version > 0 {
		qs = fmt.Sprintf("?version=%d", version)
	}
	return m, c.DoJSON("GET", "/api/artifacts/by-slug/"+url.PathEscape(ident)+qs, nil, &m)
}

func printMetaToStderr(m metaResp) {
	fmt.Fprintf(stderr(), "artifact_id: %s\n", m.ArtifactID)
	fmt.Fprintf(stderr(), "title:       %s\n", m.Title)
	if m.NamedSlug != nil {
		v := "-"
		if m.Version != nil {
			v = fmt.Sprintf("v%d", *m.Version)
		}
		fmt.Fprintf(stderr(), "slug/ver:    %s (%s)\n", *m.NamedSlug, v)
	}
	fmt.Fprintf(stderr(), "type:        %s (%s)\n", m.ArtifactType, m.ContentType)
	fmt.Fprintf(stderr(), "creator:     %s\n", m.Creator)
	if len(m.Scopes) > 0 {
		fmt.Fprintf(stderr(), "scope:       %s\n", strings.Join(m.Scopes, ", "))
	}
	if len(m.Labels) > 0 {
		fmt.Fprintf(stderr(), "labels:      %s\n", strings.Join(m.Labels, ", "))
	}
	if m.SizeBytes != nil {
		fmt.Fprintf(stderr(), "size:        %d bytes\n", *m.SizeBytes)
	}
	fmt.Fprintf(stderr(), "created:     %s\n", m.CreatedAt)
	fmt.Fprintf(stderr(), "url:         %s\n", m.URL)
	fmt.Fprintln(stderr(), "---")
}

func extractPackage(c *Client, uuid, dir string) error {
	body, _, err := c.GetRaw("/api/artifacts/" + uuid)
	if err != nil {
		return err
	}
	r, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range r.File {
		dest := filepath.Join(dir, f.Name) // #nosec G305 — caller chose dir; we sanitize next
		if !strings.HasPrefix(filepath.Clean(dest), filepath.Clean(dir)) {
			return fmt.Errorf("zip slip: %s", f.Name)
		}
		if strings.HasSuffix(f.Name, "/") {
			_ = os.MkdirAll(dest, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		out, err := os.Create(dest) // #nosec G304
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			_ = out.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		_ = rc.Close()
		_ = out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	fmt.Fprintf(stderr(), "extracted %d entries to %s\n", len(r.File), dir)
	return nil
}

// ─── ls / search / versions / url / rm ───────────────────────────────

type LsCmd struct {
	Ident    string   `arg:"" optional:"" help:"UUID → list entries inside a PACKAGE; empty → catalog"`
	Type     string   `help:"TEXT|PACKAGE"`
	Creator  string   `help:"creator email"`
	Scope    string   `help:"scope"`
	Label    []string `help:"label (repeatable)"`
	Limit    int      `default:"50"`
	Offset   int      `default:"0"`
	Archived bool     `name:"include-archived"`
}

func (l *LsCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	if l.Ident != "" && isUUID(l.Ident) {
		var m map[string]any
		if err := c.DoJSON("GET", "/api/artifacts/"+l.Ident+"/files", nil, &m); err != nil {
			return err
		}
		entries, _ := m["entries"].([]any)
		fmt.Fprintf(stderr(), "%-40s %10s  type\n", "path", "size")
		for _, e := range entries {
			ent, _ := e.(map[string]any)
			fmt.Printf("%-40s %10.0f  %s\n", ent["path"], ent["size"], ent["content_type"])
		}
		return nil
	}
	qs := QS(
		"type", l.Type, "creator", l.Creator, "scope", l.Scope,
		"label", l.Label, "limit", l.Limit, "offset", l.Offset,
		"include_archived", l.Archived,
	)
	var resp struct {
		Artifacts []metaResp `json:"artifacts"`
		Total     int64      `json:"total"`
	}
	if err := c.DoJSON("GET", "/api/artifacts"+qs, nil, &resp); err != nil {
		return err
	}
	printRows(resp.Artifacts, resp.Total)
	return nil
}

type SearchCmd struct {
	Query    string   `arg:""`
	Scope    string   `help:"scope filter"`
	Label    []string `help:"label filter"`
	Limit    int      `default:"50"`
	Offset   int      `default:"0"`
	Archived bool     `name:"include-archived"`
}

func (s *SearchCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	qs := QS("q", s.Query, "scope", s.Scope, "label", s.Label,
		"limit", s.Limit, "offset", s.Offset, "include_archived", s.Archived)
	var resp struct {
		Artifacts []metaResp `json:"artifacts"`
		Total     int64      `json:"total"`
	}
	if err := c.DoJSON("GET", "/api/artifacts/search"+qs, nil, &resp); err != nil {
		return err
	}
	printRows(resp.Artifacts, resp.Total)
	return nil
}

type VersionsCmd struct {
	Slug string `arg:""`
}

func (v *VersionsCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	var resp struct {
		Versions []metaResp `json:"versions"`
	}
	if err := c.DoJSON("GET", "/api/artifacts/by-slug/"+url.PathEscape(v.Slug)+"/versions", nil, &resp); err != nil {
		return err
	}
	printRows(resp.Versions, int64(len(resp.Versions)))
	return nil
}

type URLCmd struct {
	Ident string `arg:""`
}

func (u *URLCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	m, err := fetchMeta(c, u.Ident, 0)
	if err != nil {
		return err
	}
	fmt.Println(m.URL)
	return nil
}

type RmCmd struct {
	Ident string `arg:""`
	Yes   bool   `short:"y" help:"skip confirmation"`
}

func (r *RmCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	if !r.Yes {
		if isUUID(r.Ident) {
			if !confirm(fmt.Sprintf("Archive artifact %s…?", r.Ident[:8])) {
				return fmt.Errorf("aborted")
			}
		} else {
			if !confirmTyped("delete", fmt.Sprintf("This archives ALL versions under %q. Type 'delete' to confirm: ", r.Ident)) {
				return fmt.Errorf("aborted")
			}
		}
	}
	if isUUID(r.Ident) {
		if err := c.Delete("/api/artifacts/" + r.Ident); err != nil {
			return err
		}
		fmt.Fprintf(stderr(), "archived %s\n", r.Ident)
		return nil
	}
	var resp map[string]int64
	if err := c.DoJSON("DELETE", "/api/artifacts/by-slug/"+url.PathEscape(r.Ident), nil, &resp); err != nil {
		return err
	}
	fmt.Fprintf(stderr(), "archived %d version(s) of %s\n", resp["archived_count"], r.Ident)
	return nil
}

func confirm(prompt string) bool {
	fmt.Fprintf(stderr(), "%s [y/N] ", prompt)
	var s string
	_, _ = fmt.Fscanln(os.Stdin, &s)
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

func confirmTyped(expect, prompt string) bool {
	fmt.Fprint(stderr(), prompt)
	var s string
	_, _ = fmt.Fscanln(os.Stdin, &s)
	return strings.TrimSpace(s) == expect
}

func printRows(rows []metaResp, total int64) {
	if len(rows) == 0 {
		fmt.Fprintln(stderr(), "(no results)")
		return
	}
	fmt.Fprintf(stderr(), "%-19s  %-8s  %-24s  %-4s  %-12s  title\n",
		"created (UTC)", "id", "slug", "ver", "type")
	fmt.Fprintf(stderr(), "%s\n", strings.Repeat("-", 90))
	for _, r := range rows {
		slug := "-"
		if r.NamedSlug != nil {
			slug = *r.NamedSlug
		}
		ver := "-"
		if r.Version != nil {
			ver = fmt.Sprintf("v%d", *r.Version)
		}
		ts := r.CreatedAt
		if len(ts) > 19 {
			ts = strings.Replace(ts[:19], "T", " ", 1)
		}
		fmt.Printf("%-19s  %-8s  %-24s  %-4s  %-12s  %s\n",
			ts, idShort(r.ArtifactID), trunc(slug, 24), ver, r.ArtifactType, r.Title)
	}
	if total > int64(len(rows)) {
		fmt.Fprintf(stderr(), "(%d more — paginate with --offset)\n", total-int64(len(rows)))
	}
}

func idShort(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// keep encoding/json imported even when unused above
var _ = json.RawMessage(nil)
