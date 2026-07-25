package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AddCmd creates or versions an artifact. Input:
//
//	arti add FILE                # auto-MIME by ext
//	arti add ./dir               # auto-zips, type=PACKAGE
//	arti add -                   # stdin; sniffs markdown vs plain (override with --type)
//	arti add file --type package # force PACKAGE for a zip already on disk
type AddCmd struct {
	File        string   `arg:"" optional:"" help:"file, dir, or '-' for stdin"`
	Slug        string   `help:"named slug"`
	Title       string   `help:"title (defaults: stem of filename or 'stdin')"`
	Description string   `help:"description"`
	Type        string   `help:"text|package|app — usually inferred"`
	ContentType string   `name:"content-type" help:"MIME override"`
	EntryPoint  string   `name:"entry-point" help:"PACKAGE entry_point override"`
	Scope       []string `help:"scope (repeatable, e.g. a:bt-auto-route)"`
	Label       []string `help:"label (repeatable)"`
	EnsureNew   bool     `name:"ensure-new" help:"with --slug: fail if the slug already exists (no auto-versioning)"`
	// Access defaults: nil at flag-parse time means "not provided" —
	// server then inherits from prior version, or falls back to
	// per-user default (CLI config file) or finally server default '*'.
	// Pass --access * to make a doc public explicitly, --access alice@al.com
	// for a single user, repeat for multiple patterns.
	Access []string `help:"access pattern (repeatable; glob-on-email; '*' = everyone). Defaults to client config or server '*'"`
	// Private sends an explicit empty allowed_access ([]) so the artifact
	// is readable only by its creator. This is the one access shape the
	// repeatable --access flag can't express (no way to pass an empty
	// list), so it gets a dedicated flag. Mutually exclusive with --access.
	Private bool `help:"make this artifact readable only by you (creator-only). Mutually exclusive with --access"`
	// WriteAccess is the subset of readers allowed to write (push new versions
	// / append / edit). nil (flag absent) → writers follow readers (today's
	// behavior). Server unions these into allowed_access. Repeat for multiple.
	WriteAccess []string `name:"write-access" help:"write-access pattern (repeatable; subset of --access). Absent = writers follow readers"`
	// WritePrivate sends an explicit empty allowed_write ([]) = creator-only
	// writes, while reads stay as --access/--private. Mutually exclusive with
	// --write-access (which can't express the empty list).
	WritePrivate bool `name:"write-private" help:"only you (the creator) may write; readers stay read-only. Mutually exclusive with --write-access"`
}

func (a *AddCmd) Run(cli *CLI) error {
	c, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}

	var (
		content     []byte
		title       = a.Title
		contentType = a.ContentType
		// Empty = "let the server default to TEXT". Stays empty for text
		// uploads; the inference below sets PACKAGE/ATTACHMENT where needed,
		// and an explicit --type wins. Defaulting to "" (not "TEXT") is what
		// lets the non-text → ATTACHMENT auto-pick fire.
		artifactType = ""
	)

	switch {
	case a.File == "" || a.File == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		content = b
		if contentType == "" {
			contentType = sniffStdinMIME(b)
		}
		if title == "" {
			title = "stdin"
		}
	default:
		info, err := os.Stat(a.File)
		if err != nil {
			return err
		}
		if info.IsDir() {
			z, err := zipDir(a.File)
			if err != nil {
				return err
			}
			content = z
			contentType = "application/zip"
			artifactType = "PACKAGE"
			if title == "" {
				title = filepath.Base(filepath.Clean(a.File))
			}
		} else {
			b, err := os.ReadFile(a.File) // #nosec G304 — user input
			if err != nil {
				return err
			}
			content = b
			if strings.HasSuffix(strings.ToLower(a.File), ".zip") {
				artifactType = "PACKAGE"
				if contentType == "" {
					contentType = "application/zip"
				}
			}
			if contentType == "" {
				contentType = guessMIME(a.File)
			}
			if title == "" {
				title = inferTitle(a.File, content)
			}
			// Non-textual content uploaded with --slug: don't auto-promote
			// here (ATTACHMENT can't have a slug); let the server return
			// the helpful "not textual; use ATTACHMENT or PACKAGE" 400 so
			// the user can decide which they actually wanted. The
			// slugless-default-to-ATTACHMENT promotion below handles the
			// `arti add foo.png` case.
		}
	}

	switch strings.ToLower(a.Type) {
	case "package":
		artifactType = "PACKAGE"
		if contentType == "" {
			contentType = "application/zip"
		}
	case "app":
		artifactType = "APP"
		if contentType == "" {
			contentType = "application/zip"
		}
	case "text":
		artifactType = "TEXT"
	case "attachment", "file":
		artifactType = "ATTACHMENT"
	case "md", "markdown":
		contentType = "text/markdown"
	case "html":
		contentType = "text/html"
	case "plain", "txt", "text/plain":
		contentType = "text/plain"
	}

	// Non-text single files (pdf, images, binaries) can't be a TEXT artifact —
	// the server rejects that. When the caller didn't force a type and isn't
	// naming a slug, default them to ATTACHMENT (which is slugless + creator-
	// only) so `arti add foo.pdf` just works. A slugged non-text upload is
	// left as-is so the server's reject guides the user to a PACKAGE instead
	// of silently dropping their slug.
	if artifactType == "" && a.Slug == "" && contentType != "" && !isTextualCT(contentType) {
		artifactType = "ATTACHMENT"
	}

	if title == "" {
		return fmt.Errorf("title required")
	}

	payload := map[string]any{
		"title":          title,
		"content_type":   contentType,
		"artifact_type":  artifactType,
		"content_base64": base64.StdEncoding.EncodeToString(content),
	}
	if a.Slug != "" {
		payload["named_slug"] = a.Slug
	}
	if a.Description != "" {
		payload["description"] = a.Description
	}
	if a.EntryPoint != "" {
		payload["entry_point"] = a.EntryPoint
	}
	if len(a.Scope) > 0 {
		payload["scopes"] = a.Scope
	}
	if len(a.Label) > 0 {
		payload["labels"] = a.Label
	}
	if a.EnsureNew {
		payload["ensure_new"] = true
	}
	// Access patterns. --private forces creator-only by sending an
	// explicit empty array; it overrides client config and prior-version
	// inheritance and can't be combined with --access. Otherwise the
	// --access flag wins; if absent, fall back to the user's
	// ~/.config/arti/config.json default_access; if that's also absent,
	// omit the field so the server inherits-from-prior-version or uses
	// its default '*'.
	switch {
	case a.Private:
		if len(a.Access) > 0 {
			return fmt.Errorf("--private and --access are mutually exclusive")
		}
		payload["allowed_access"] = []string{} // creator-only
	default:
		access := a.Access
		if access == nil {
			access = loadDefaultAccess()
		}
		if access != nil {
			payload["allowed_access"] = access
		}
	}

	// Write access: --write-private sends [] (creator-only writes); otherwise
	// --write-access sets the write subset; absent = omit so writers follow
	// readers (the server's back-compat default).
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
	if err := c.DoJSON("POST", "/api/artifacts", payload, &resp); err != nil {
		return err
	}

	// metadata to stderr, URL to stdout (pipe-friendly)
	fmt.Fprintf(stderr(), "artifact_id: %v\n", resp["artifact_id"])
	if slug, ok := resp["named_slug"].(string); ok && slug != "" {
		fmt.Fprintf(stderr(), "slug:        %s (v%v)\n", slug, resp["version"])
	}
	if u, ok := resp["url"].(string); ok {
		fmt.Println(u)
	}
	return nil
}

// loadDefaultAccess returns the user's `default_access` setting from
// ~/.config/arti/config.json, or nil if the file is absent / unreadable
// / malformed. Format:
//
//	{ "default_access": ["*@example.com", "alice@al.com"] }
//
// nil from here means "no client default" — the upload omits the field
// and the server falls back to inherit-then-server-default.
func loadDefaultAccess() []string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	p := filepath.Join(cfg, "arti", "config.json")
	b, err := os.ReadFile(p) // #nosec G304 — user config dir, intentional
	if err != nil {
		return nil
	}
	var c struct {
		DefaultAccess []string `json:"default_access"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		fmt.Fprintf(stderr(), "warning: ignoring %s (parse error: %v)\n", p, err)
		return nil
	}
	if len(c.DefaultAccess) == 0 {
		return nil
	}
	return c.DefaultAccess
}

// maxInferredTitleLen caps the length of a title pulled out of a
// markdown heading or other first-line content — anything past 80
// characters is almost always a sentence, not a title.
const maxInferredTitleLen = 80

// inferTitle picks a sensible title when the user didn't pass --title.
// For markdown files we look at the first non-empty line and strip the
// leading `#` markers; otherwise we fall back to the file's basename.
// Anything that exceeds maxInferredTitleLen runes is truncated with an
// ellipsis so titles stay headline-shaped.
func inferTitle(path string, content []byte) string {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") {
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Drop a leading run of `#` (markdown heading) + any spaces
			// that follow; if the line had no heading marker we still
			// use the bare text.
			t := strings.TrimLeft(line, "#")
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			return truncateRunes(t, maxInferredTitleLen)
		}
	}
	return filepath.Base(path)
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// zipDir packs a directory deterministically: sorted entries, fixed mtime,
// 0644 mode, deflate. Re-runs produce identical bytes (and thus sha256).
func zipDir(root string) ([]byte, error) {
	root = filepath.Clean(root)
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("zipDir: %s has no files", root)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fixed := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		hdr := &zip.FileHeader{
			Name:     filepath.ToSlash(rel),
			Method:   zip.Deflate,
			Modified: fixed,
		}
		hdr.SetMode(0o644)
		fw, err := w.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(p) // #nosec G304 — walked input
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
