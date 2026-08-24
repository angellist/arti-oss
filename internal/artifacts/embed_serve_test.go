package artifacts

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsMarkdown(t *testing.T) {
	for _, ct := range []string{"text/markdown", "text/markdown; charset=utf-8", "text/x-markdown"} {
		if !isMarkdown(ct) {
			t.Errorf("isMarkdown(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{"text/html", "text/plain", "application/pdf", ""} {
		if isMarkdown(ct) {
			t.Errorf("isMarkdown(%q) = true, want false", ct)
		}
	}
}

func TestRenderMarkdownDoc(t *testing.T) {
	out := string(renderMarkdownDoc([]byte("# Title\n\nA [link](https://e.com) and `code`.\n"), "My Doc"))
	for _, want := range []string{
		"<!doctype html",
		`<base target="_blank"`, // links open a real tab, not the panel
		"<title>My Doc</title>",
		"<h1", "Title",
		`href="https://e.com"`, // links are real, clickable anchors
		"<code>code</code>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered doc missing %q\n---\n%s", want, out)
		}
	}
}

// goldmark runs without WithUnsafe, so raw HTML embedded in markdown must be
// escaped — a single-doc artifact can't smuggle live markup past the renderer
// (the sandbox is only defense-in-depth on top of this).
func TestRenderMarkdownDoc_EscapesRawHTML(t *testing.T) {
	out := string(renderMarkdownDoc([]byte("hi\n\n<script>alert(1)</script>\n"), "x"))
	if strings.Contains(out, "<script>alert(1)") {
		t.Errorf("raw <script> survived markdown rendering:\n%s", out)
	}
}

// A single "~" means "approximately" in prose and must NOT trigger GFM
// strikethrough — goldmark's stock extension.Strikethrough would pair the two
// lone tildes below and wrap everything between them in <del>.
func TestRenderMarkdownDoc_SingleTildeIsNotStrikethrough(t *testing.T) {
	src := "Unit economics: ~1000× cheaper per unit than a fin conversation (~$5).\n"
	out := string(renderMarkdownDoc([]byte(src), "x"))
	if strings.Contains(out, "<del>") {
		t.Errorf("single tilde produced a strikethrough:\n%s", out)
	}
	for _, want := range []string{"~1000×", "(~$5)"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered doc missing literal %q\n---\n%s", want, out)
		}
	}
}

// Double-tilde strikethrough must still render as <del>.
func TestRenderMarkdownDoc_DoubleTildeStrikethrough(t *testing.T) {
	out := string(renderMarkdownDoc([]byte("this is ~~struck~~ out\n"), "x"))
	if !strings.Contains(out, "<del>struck</del>") {
		t.Errorf("double tilde did not render strikethrough:\n%s", out)
	}
}

// Emphasis delimiters are often the only separator between two words (an email
// subject quoted verbatim: "***PAST DUE***Document Request"). goldmark consumes
// them, so the renderer injects the optical gap — but only at a tight boundary,
// never before punctuation or an existing space. Twin of the web test in
// web/lib/markdown.test.ts.
func TestRenderMarkdownDoc_TightEmphasisGap(t *testing.T) {
	out := string(renderMarkdownDoc([]byte("Re: ***PAST DUE***Document Request\n"), "x"))
	if !strings.Contains(out, `</em><span class="arti-emph-gap"></span>Document`) {
		t.Errorf("tight emphasis boundary did not get a gap:\n%s", out)
	}
	if !strings.Contains(out, ".arti-emph-gap{") {
		t.Errorf("stylesheet is missing the .arti-emph-gap rule:\n%s", out)
	}
	// Scripts without inter-word spaces (CJK, Thai…) write emphasis flush
	// against the next character by design — a gap there invents a word break.
	for _, src := range []string{"a *word*, b\n", "a *word*. b\n", "a *word* b\n", "(*word*)\n",
		"**粗体**文字\n", "**太字**です\n", "**볼드**텍스트\n", "**หนา**ตัวอักษร\n"} {
		if got := string(renderMarkdownDoc([]byte(src), "x")); strings.Contains(got, `<span class="arti-emph-gap">`) {
			t.Errorf("%q got a gap it does not need:\n%s", src, got)
		}
	}
}

func TestSetContentSecurityEmbed_HTML(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Frame-Options", "SAMEORIGIN") // simulate the global middleware
	setContentSecurityEmbed(w, "text/html; charset=utf-8", "'self' https://app.frontapp.com")

	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts allow-popups") {
		t.Errorf("html CSP missing app-grade sandbox: %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'self' https://app.frontapp.com") {
		t.Errorf("html CSP missing frame-ancestors: %q", csp)
	}
	if w.Header().Get("X-Frame-Options") != "" {
		t.Error("X-Frame-Options not dropped for embed HTML")
	}
}

func TestSetContentSecurityEmbed_NonHTML(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	setContentSecurityEmbed(w, "application/pdf", "'self' https://dash.x")

	csp := w.Header().Get("Content-Security-Policy")
	if csp != "frame-ancestors 'self' https://dash.x" {
		t.Errorf("non-html CSP = %q, want just frame-ancestors", csp)
	}
	if strings.Contains(csp, "sandbox") {
		t.Error("sandbox applied to non-HTML (only documents need it)")
	}
	if w.Header().Get("X-Frame-Options") != "" {
		t.Error("X-Frame-Options not dropped for embed PDF")
	}
	if w.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
}
