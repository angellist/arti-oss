// Package mdtext holds the one markdown configuration the server renders with.
// The viewer overrides marked's strikethrough to open only on "~~" so prose can
// use "~" for "approximately" (web/lib/markdown.tsx); anything here that parses
// markdown must agree with it, or the same document reads differently depending
// on which side looked at it.
package mdtext

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// New returns goldmark configured as the viewer is. Renderer options are the
// caller's: output that will be served must not enable unsafe raw HTML.
func New(opts ...goldmark.Option) goldmark.Markdown {
	return goldmark.New(append([]goldmark.Option{goldmark.WithExtensions(
		extension.Table,
		extension.Linkify,
		extension.TaskList,
		doubleTildeStrikethrough,
	)}, opts...)...)
}

var (
	tagRE = regexp.MustCompile(`<[^>]+>`)
	// Only a block boundary puts space between words on the page. An inline tag
	// does not: linkify wraps a bare address in an anchor, and a space injected
	// there would break the text either side of it.
	blockEndRE = regexp.MustCompile(`(?i)</(p|h[1-6]|li|tr|td|th|div|blockquote|pre|table|thead|tbody)>|<br\s*/?>|<hr\s*/?>`)
	entRE      = regexp.MustCompile(`&(amp|lt|gt|quot|#39|nbsp);`)
	entMap     = map[string]string{"&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": `"`, "&#39;": "'", "&nbsp;": " "}
	// WithUnsafe passes raw HTML through instead of dropping it. This output is
	// never served, only reduced to words, and an HTML artifact whose markup was
	// dropped would have no text left to read.
	pageMD = New(goldmark.WithRendererOptions(gmhtml.WithUnsafe()))
)

// PageText returns the words a reader sees for a markdown or HTML document,
// with runs of whitespace collapsed to single spaces.
func PageText(src string) string {
	var buf bytes.Buffer
	if err := pageMD.Convert([]byte(src), &buf); err != nil {
		return Normalize(src)
	}
	out := blockEndRE.ReplaceAllString(buf.String(), " ")
	out = tagRE.ReplaceAllString(out, "")
	return Normalize(entRE.ReplaceAllStringFunc(out, func(e string) string { return entMap[e] }))
}

// Normalize collapses whitespace runs so text that differs only in wrapping
// compares equal.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
