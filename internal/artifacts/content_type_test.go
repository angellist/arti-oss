package artifacts

import "testing"

// isTextualContentType gates which content types may be stored as a TEXT
// artifact. Non-textual types (pdf/image/zip/binary) must be rejected so they
// don't render as garbage in the viewer.
func TestIsTextualContentType(t *testing.T) {
	textual := []string{
		"text/markdown", "text/html", "text/plain", "text/csv",
		"text/x-python", "text/markdown; charset=utf-8", "TEXT/HTML",
		"application/json", "application/yaml", "application/javascript",
	}
	for _, ct := range textual {
		if !isTextualContentType(ct) {
			t.Errorf("isTextualContentType(%q) = false, want true", ct)
		}
	}
	nonTextual := []string{
		"application/pdf", "image/png", "image/svg+xml", "application/zip",
		"application/octet-stream", "video/mp4", "", "application/pdf; x=1",
	}
	for _, ct := range nonTextual {
		if isTextualContentType(ct) {
			t.Errorf("isTextualContentType(%q) = true, want false", ct)
		}
	}
}

// extForContentType is the source-of-truth for download filenames.
// The curated cases beat mime.ExtensionsByType for the common ones
// (e.g. image/jpeg → .jpg, not .jfif) so the user-visible filename is
// what you'd expect from "save as".
func TestExtForContentType(t *testing.T) {
	cases := []struct {
		ct, want string
	}{
		{"text/plain", ".txt"},
		{"text/markdown", ".md"},
		{"text/html", ".html"},
		{"text/html; charset=utf-8", ".html"}, // params stripped
		{"TEXT/MARKDOWN", ".md"},              // case-insensitive
		{"application/json", ".json"},
		{"application/pdf", ".pdf"},
		{"application/zip", ".zip"},
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"image/svg+xml", ".svg"},
		{"audio/mpeg", ".mp3"},
		{"video/mp4", ".mp4"},
	}
	for _, c := range cases {
		if got := extForContentType(c.ct); got != c.want {
			t.Errorf("extForContentType(%q) = %q, want %q", c.ct, got, c.want)
		}
	}
}

// deriveDownloadFilename composes (stem, ext). The contract:
//  1. empty title → empty result
//  2. title gets path separators stripped
//  3. extension auto-appended UNLESS title already ends in it (case-insensitive)
func TestDeriveDownloadFilename(t *testing.T) {
	cases := []struct {
		title, ct, want string
	}{
		{"alex-6.5.26-interview", "image/png", "alex-6.5.26-interview.png"},
		{"alex-6.5.26-interview.png", "image/png", "alex-6.5.26-interview.png"}, // no double-suffix
		{"alex-6.5.26-interview.PNG", "image/png", "alex-6.5.26-interview.PNG"}, // case-insensitive suffix check
		{"notes", "text/markdown", "notes.md"},
		{"notes.md", "text/markdown", "notes.md"},
		{"some/path/segment", "text/plain", "some_path_segment.txt"}, // path-sep stripped
		{"  spaced  ", "text/plain", "spaced.txt"},                   // trimmed
		{"", "text/plain", ""},                                       // empty title
	}
	for _, c := range cases {
		if got := deriveDownloadFilename(c.title, c.ct); got != c.want {
			t.Errorf("deriveDownloadFilename(%q, %q) = %q, want %q", c.title, c.ct, got, c.want)
		}
	}
}

// sanitizeASCIIFilename strips bytes that would break Content-Disposition's
// ASCII `filename="..."` parameter: control chars, quote, backslash, and
// non-ASCII. Non-ASCII clients should use the `filename*` (RFC 5987)
// parameter we also emit; the ASCII fallback gets underscores instead.
func TestSanitizeASCIIFilename(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello.md", "hello.md"},
		{"with space.txt", "with space.txt"},
		{`he"llo`, `he_llo`},
		{`back\slash`, `back_slash`},
		{"weird\x01ctrl.txt", "weird_ctrl.txt"},
		{"naïve.txt", "na_ve.txt"}, // non-ASCII → underscore
		{"日本語.png", "___.png"},     // three CJK chars → three underscores
		{"", "download"},           // empty → fallback
		{"\x00\x01\x02", "___"},    // all control → underscores
	}
	for _, c := range cases {
		if got := sanitizeASCIIFilename(c.in); got != c.want {
			t.Errorf("sanitizeASCIIFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// isInlineRenderable picks `inline` vs `attachment` in Content-Disposition.
// Keep narrow: anything we're unsure about should fall through to `attachment`
// so the browser saves it rather than trying (and failing) to render it.
func TestIsInlineRenderable(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"text/html", true},
		{"text/plain", true},
		{"image/png", true},
		{"application/pdf", true},
		{"application/json", true},
		{"audio/mpeg", true},
		{"video/mp4", true},
		{"application/zip", false},
		{"application/octet-stream", false},
		{"application/x-something-weird", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isInlineRenderable(c.ct); got != c.want {
			t.Errorf("isInlineRenderable(%q) = %v, want %v", c.ct, got, c.want)
		}
	}
}
