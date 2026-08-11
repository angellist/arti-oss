// Package pkgzip handles zip artifacts: building manifests on upload
// and reading single entries on fetch.
package pkgzip

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

// Entry describes one file inside a PACKAGE artifact's zip.
type Entry struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256,omitempty"`
	ContentType string `json:"content_type"`
}

// Manifest is the JSON shape persisted under artifacts.metadata.package.
type Manifest struct {
	Format     string  `json:"format"` // always "zip" in v1
	Entries    []Entry `json:"entries"`
	EntryPoint string  `json:"entry_point,omitempty"`
}

// BuildManifest reads the central directory of `zipBytes` and produces
// a structural manifest (per-entry path/size/sha + a guessed entry_point).
func BuildManifest(zipBytes []byte) (Manifest, error) {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return Manifest{}, fmt.Errorf("pkgzip: open: %w", err)
	}
	out := Manifest{Format: "zip"}
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue // skip directory entries
		}
		if isMacOSXJunk(f.Name) {
			continue // macOS metadata sidecars; never user content
		}
		body, err := readFile(f)
		if err != nil {
			return Manifest{}, fmt.Errorf("pkgzip: read %s: %w", f.Name, err)
		}
		sum := sha256.Sum256(body)
		out.Entries = append(out.Entries, Entry{
			Path:        f.Name,
			Size:        int64(len(body)),
			SHA256:      hex.EncodeToString(sum[:]),
			ContentType: ContentTypeOfBytes(f.Name, body),
		})
	}
	out.EntryPoint = pickDefaultEntry(out.Entries)
	return out, nil
}

// FlattenSingleRoot normalizes a "tarbomb"-style package: when every real
// file in the zip lives under ONE shared top-level directory (e.g. someone
// ran `zip -r app.zip app/` instead of zipping the contents), it rewrites
// the zip stripping that directory so the package is flat — index.html and
// arti-app.json land at the root where landing-page detection and the APP
// tool-manifest reader expect them, and the file rail isn't cluttered with a
// repeated prefix.
//
// It returns the original bytes UNCHANGED when there isn't exactly one
// wrapping directory: any file already at the root, or two-or-more top-level
// directories (e.g. src/ + dist/), leaves the layout intact. Directory
// entries and macOS junk (__MACOSX, .DS_Store) are dropped from the rewrite.
func FlattenSingleRoot(zipBytes []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("pkgzip: open: %w", err)
	}

	// Real content files only — the wrapping-dir decision ignores directory
	// entries and macOS sidecars (otherwise __MACOSX/ would read as a second
	// top-level dir and suppress every flatten).
	var files []*zip.File
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") || isMacOSXJunk(f.Name) {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return zipBytes, nil
	}

	root := ""
	for i, f := range files {
		idx := strings.IndexByte(f.Name, '/')
		if idx <= 0 {
			return zipBytes, nil // a root-level file → not wrapped
		}
		seg := f.Name[:idx]
		if i == 0 {
			root = seg
		} else if seg != root {
			return zipBytes, nil // multiple top-level dirs → leave alone
		}
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		stripped := f.Name[len(root)+1:]
		if stripped == "" {
			continue
		}
		body, err := readFile(f)
		if err != nil {
			return nil, fmt.Errorf("pkgzip: read %s: %w", f.Name, err)
		}
		hdr := &zip.FileHeader{Name: stripped, Method: zip.Deflate}
		hdr.SetMode(f.Mode())
		fw, err := w.CreateHeader(hdr)
		if err != nil {
			return nil, fmt.Errorf("pkgzip: write %s: %w", stripped, err)
		}
		if _, err := fw.Write(body); err != nil {
			return nil, fmt.Errorf("pkgzip: write %s: %w", stripped, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("pkgzip: close: %w", err)
	}
	return buf.Bytes(), nil
}

// ReadEntry returns the bytes + content-type for a single entry. Lookup
// is case-sensitive and mirrors how static hosts resolve clean URLs (see
// resolveCandidates): an exact match wins, then `<path>.html`, then
// `<path>/index.html`. This lets static-site exports (e.g. `next export`)
// whose internal links are extensionless — `href="/page"` for a file
// stored as `page.html` — navigate correctly. Returns an error if no
// candidate exists.
func ReadEntry(zipBytes []byte, path string) ([]byte, string, error) {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, "", err
	}
	byName := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		if isMacOSXJunk(f.Name) {
			continue
		}
		byName[f.Name] = f
	}
	for _, cand := range resolveCandidates(path) {
		f, ok := byName[cand]
		if !ok {
			continue
		}
		body, err := readFile(f)
		if err != nil {
			return nil, "", err
		}
		return body, ContentTypeOfBytes(f.Name, body), nil
	}
	return nil, "", fmt.Errorf("pkgzip: entry %q not found", path)
}

// resolveCandidates returns the ordered list of zip entry names to try
// for a requested package path, replicating static-host clean-URL
// behavior (Vercel/Netlify, `next export`):
//
//	"wallet-ia-v1" → ["wallet-ia-v1", "wallet-ia-v1.html", "wallet-ia-v1/index.html"]
//	"guide/"       → ["guide/index.html"]
//	"" or "/"      → ["index.html"]
//
// Exact match is tried first so a file that genuinely has no extension
// always wins over a synthesized `.html` fallback. Paths that already
// resolve exactly (e.g. `app.js`, `index.html`) match on the first
// candidate; the extra candidates simply never hit.
func resolveCandidates(path string) []string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return []string{"index.html"}
	}
	if strings.HasSuffix(path, "/") {
		return []string{path + "index.html"}
	}
	return []string{path, path + ".html", path + "/index.html"}
}

func readFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// ContentTypeOfBytes types a zip entry from its NAME first and its BYTES only
// as a fallback. Extensionless files are normal inside real packages — Runlayer
// skill files (`course-of-action-playbooks`), `LICENSE`, `Dockerfile` — and
// name-only typing lands every one of them on `application/octet-stream`, which
// the viewer can only offer as a download. So when the extension tells us
// nothing, we look at the content: recognized magic bytes win (an extensionless
// PNG is `image/png`), text becomes markdown-or-plain, and only genuinely
// unrecognized bytes stay octet-stream.
//
// A known extension always beats the sniff — `.md` is markdown even if the file
// is empty, and we never let a stray `<html` opening line retype a `.txt`.
func ContentTypeOfBytes(name string, body []byte) string {
	ct := ContentTypeOf(name)
	if ct != octetStream {
		return ct
	}
	// http.DetectContentType reads at most the first 512 bytes and returns
	// octet-stream itself when nothing matches, so this can only ever improve
	// on the name-based answer.
	sniffed := http.DetectContentType(body)
	if strings.HasPrefix(sniffed, "text/plain") {
		return SniffTextContentType(body)
	}
	return sniffed
}

// ContentTypeOf guesses a MIME type from a filename's extension. Falls
// back through stdlib `mime` and then to `application/octet-stream`.
// Prefer ContentTypeOfBytes wherever the entry's bytes are at hand.
func ContentTypeOf(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".md", ".markdown":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".txt":
		return "text/plain"
	case ".sh":
		return "text/x-shellscript"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".py":
		return "text/x-python"
	case ".go":
		return "text/x-go"
	case ".js", ".ts":
		return "application/javascript"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return octetStream
}

const octetStream = "application/octet-stream"

// markdownLine matches a line carrying a strong, unambiguous markdown signal:
// an ATX heading, an unordered or ordered list item, or a blockquote. The
// trailing space matters — it's what distinguishes `- item` from the
// `-rw-r--r--` of an `ls -l` dump.
var markdownLine = regexp.MustCompile(`(?m)^\s{0,3}(#{1,6} |[-*+] |\d+\. |> )`)

// markdownInline matches a fenced code block or a `[text](url)` link. Bold
// (`**`) is deliberately excluded — too common in plain console output.
var markdownInline = regexp.MustCompile("```|\\[[^\\]]+\\]\\([^)]+\\)")

// SniffTextContentType splits text bytes into markdown vs plain. Plain text is
// the safe default — it still renders inline in the viewer, just in a <pre> —
// so we upgrade to markdown only on a clear structural signal.
//
// cmd/arti's sniffStdinMIME answers the same question for piped stdin and
// MIRRORS this rather than importing it, because cmd/arti deliberately depends
// on no internal package. That mirror is not left to trust: cmd/arti's
// TestSniffStdinMIME_MatchesPkgzip (a test-only import, so the CLI binary's
// dependency graph is unaffected) fails if the two ever disagree. Exported for
// exactly that assertion.
func SniffTextContentType(body []byte) string {
	s := string(body)
	if markdownLine.MatchString(s) || markdownInline.MatchString(s) {
		return "text/markdown"
	}
	return "text/plain"
}

// IsJunk reports whether a zip entry is macOS-only metadata that
// shouldn't be exposed as artifact content. The Finder and `zip` on
// macOS bundle a parallel `__MACOSX/` directory with `._<name>` AppleDouble
// resource forks; users never want to see them in the package rail.
// Exported so the API layer can also filter older uploads whose stored
// manifest predates this filter.
func IsJunk(name string) bool { return isMacOSXJunk(name) }

func isMacOSXJunk(name string) bool {
	if strings.HasPrefix(name, "__MACOSX/") {
		return true
	}
	// AppleDouble files at any path: prefix `._` on the basename.
	base := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		base = name[i+1:]
	}
	if strings.HasPrefix(base, "._") {
		return true
	}
	if base == ".DS_Store" {
		return true
	}
	return false
}

// pickDefaultEntry chooses a natural entry_point without prescribing a zip
// layout. Preference order:
//  1. skill.md at the root          — explicit skill packages
//  2. the best HTML landing page    — anywhere in the zip (see pickHTMLLanding),
//     so a static site renders on open even when its index lives under out/,
//     dist/, build/, or a nested folder
//  3. README.md at the root         — doc packages
//  4. any markdown at the root
//  5. empty
func pickDefaultEntry(entries []Entry) string {
	for _, e := range entries {
		if !strings.Contains(e.Path, "/") && strings.EqualFold(e.Path, "skill.md") {
			return e.Path
		}
	}
	if h := pickHTMLLanding(entries); h != "" {
		return h
	}
	for _, e := range entries {
		if !strings.Contains(e.Path, "/") && strings.EqualFold(e.Path, "readme.md") {
			return e.Path
		}
	}
	for _, e := range entries {
		if !strings.Contains(e.Path, "/") && e.ContentType == "text/markdown" {
			return e.Path
		}
	}
	return ""
}

// pickHTMLLanding finds the most likely landing page among HTML entries. It
// ranks by (1) how index-like the filename is, (2) shallowest depth, (3)
// shortest path, (4) alphabetical — so `index.html` at the root beats a deep
// `sub/page.html`, but a nested `out/index.html` still wins when there's no
// shallower candidate. Error/partial pages (404, _not-found, leading `_`) are
// skipped. Returns "" when the package has no usable HTML.
func pickHTMLLanding(entries []Entry) string {
	base := func(p string) string {
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			return p[i+1:]
		}
		return p
	}
	// Lower rank = better landing name.
	nameRank := func(b string) int {
		lb := strings.ToLower(b)
		switch {
		case lb == "index.html" || lb == "index.htm":
			return 0
		case strings.HasSuffix(lb, "index.html") || strings.HasSuffix(lb, "index.htm"):
			return 1 // e.g. wallet-index.html
		case lb == "home.html" || lb == "main.html" || lb == "app.html":
			return 2
		default:
			return 3
		}
	}
	skip := func(b string) bool {
		lb := strings.ToLower(b)
		return strings.HasPrefix(lb, "_") || lb == "404.html" || lb == "error.html"
	}

	best := ""
	var bn, bd, bl int
	for _, e := range entries {
		lp := strings.ToLower(e.Path)
		if !strings.HasSuffix(lp, ".html") && !strings.HasSuffix(lp, ".htm") {
			continue
		}
		b := base(e.Path)
		if skip(b) {
			continue
		}
		n, d, l := nameRank(b), strings.Count(e.Path, "/"), len(e.Path)
		// Tuple compare: (nameRank, depth, len, path), all ascending.
		better := best == "" || n < bn ||
			(n == bn && d < bd) ||
			(n == bn && d == bd && l < bl) ||
			(n == bn && d == bd && l == bl && e.Path < best)
		if better {
			best, bn, bd, bl = e.Path, n, d, l
		}
	}
	return best
}
