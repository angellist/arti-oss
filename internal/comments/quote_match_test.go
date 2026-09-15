package comments

import (
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/mdtext"
)

// A caller is told to quote prose as the page renders it. Every rendered form
// here is what a reader sees for the markdown beside it.
func TestPageTextMatchesRenderedProse(t *testing.T) {
	cases := []struct{ name, source, rendered string }{
		{"italic", "An *italic* word here.", "An italic word here."},
		{"underscore italic", "An _italic_ word here.", "An italic word here."},
		{"bold", "A **bold** word here.", "A bold word here."},
		{"code span", "Call `pageText` first.", "Call pageText first."},
		{"strikethrough", "That was ~~wrong~~ right.", "That was wrong right."},
		{"inline link", "See [the docs](https://example.com/x) for more.", "See the docs for more."},
		{"link with title", `See [docs](https://example.com "T") now.`, "See docs now."},
		{"image is not page text", "Look ![a chart](chart.png) here.", "Look here."},
		{"heading", "## A section title", "A section title"},
		{"blockquote", "> quoted line here", "quoted line here"},
		{"list item", "- a bullet item", "a bullet item"},
		{"table row", "| a | b |\n| - | - |\n| alpha | beta |", "alpha beta"},
		{"html tag", "<em>emphatic</em> prose", "emphatic prose"},
		{"autolink email", "Contact: Jack D <jack@example.com> today", "Contact: Jack D jack@example.com today"},
		{"autolink url", "See <https://example.com/x> for more", "See https://example.com/x for more"},
		{"wrapped line", "a phrase broken\nacross two lines", "a phrase broken across two lines"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if body := mdtext.PageText(c.source); !strings.Contains(body, normalizePlainText(c.rendered)) {
				t.Errorf("page text %q does not contain the rendered quote %q", body, c.rendered)
			}
		})
	}
}

// Approximation tildes are ordinary prose in these documents. The viewer's
// renderer keeps a lone tilde and strikes a tight pair, and this must agree
// with it in both directions or a correct page quote gets refused.
func TestPageTextTreatsTildesLikeTheViewer(t *testing.T) {
	// The first two are the cases web/lib/markdown.tsx cites: stock GFM pairs
	// their two lone tildes and swallows everything between them.
	kept := []string{
		"re-skinning ~12 components (~+1 wk).",
		"Unit economics: ~1000× cheaper (~$5).",
		"grew ~5% to ~10% last year",
		"due ~06:57 PT",
	}
	for _, q := range kept {
		if body := mdtext.PageText(q); !strings.Contains(body, normalizePlainText(q)) {
			t.Errorf("%q survived as %q", q, body)
		}
	}
	// The viewer opens strikethrough only on "~~" (web/lib/markdown.tsx), so a
	// tight single-tilde pair is literal text on the page and must stay literal
	// here. Stock GFM pairs it, which is why this side does not use stock GFM.
	if body := mdtext.PageText("a ~single~ pair here"); !strings.Contains(body, "a ~single~ pair here") {
		t.Errorf("tight tilde pair = %q, want it left literal as the viewer leaves it", body)
	}
	if body := mdtext.PageText("that was ~~wrong~~ right"); !strings.Contains(body, "that was wrong right") {
		t.Errorf("double tilde = %q, want it struck", body)
	}
}

// An HTML artifact is not markdown, and a renderer that drops raw HTML leaves
// it with no quotable text at all.
func TestPageTextReadsAnHTMLArtifact(t *testing.T) {
	body := mdtext.PageText("<h1>Title</h1><p>A <b>bold</b> phrase in an HTML doc.</p>")
	if !strings.Contains(body, "A bold phrase in an HTML doc.") {
		t.Errorf("html page text = %q", body)
	}
}

// Characters a renderer leaves alone stay quotable exactly as they appear.
// Stripping them wholesale rejected quotes that a reader had copied correctly.
func TestPageTextKeepsLiteralPunctuation(t *testing.T) {
	for _, q := range []string{"a snake_case_name here", "see issue #288 for that", "when a > b holds", "the a|b form"} {
		if body := mdtext.PageText(q); !strings.Contains(body, normalizePlainText(q)) {
			t.Errorf("%q survived as %q", q, body)
		}
	}
}

func TestPageTextStillRejectsAbsentText(t *testing.T) {
	body := mdtext.PageText("Alpha line one.\n\nBeta line two.\n")
	for _, q := range []string{"Gamma line three.", "a phrase from another document", "Alpha Beta"} {
		if strings.Contains(body, normalizePlainText(q)) {
			t.Errorf("%q must not match", q)
		}
	}
}

// A quote in source form names real text, but its markers are not on the page,
// so the viewer could never seat a highlight for it. That is a distinct answer
// from "this text is not here".
func TestSourceFormQuoteIsItsOwnFailure(t *testing.T) {
	body := mdtext.PageText("A **bold** word here.")
	if strings.Contains(body, normalizePlainText("A **bold** word here.")) {
		t.Fatal("source form must not read as page text")
	}
	if !strings.Contains(body, mdtext.PageText("A **bold** word here.")) {
		t.Fatal("source form must still be recognized as naming real text")
	}
}
