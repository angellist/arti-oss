package artifacts

import (
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// goldmark's stock GFM strikethrough (extension.Strikethrough) treats a run of
// EITHER one or two tildes as a strikethrough delimiter. That means ordinary
// prose using "~" to mean "approximately" — e.g. "~1000× cheaper ... (~$5)" —
// gets parsed as an opening/closing pair and the text between the two tildes is
// wrapped in <del>. goldmark v1.8.2 exposes no option to require "~~", so we
// register our own inline parser that only opens on a run of exactly two
// tildes, and drop extension.Strikethrough from the GFM set (see embed_serve.go).
//
// Everything else (the AST node kind and the <del> HTML renderer) is reused from
// goldmark so double-tilde strikethrough keeps rendering identically.

type strictStrikethroughDelimiterProcessor struct{}

func (p *strictStrikethroughDelimiterProcessor) IsDelimiter(b byte) bool { return b == '~' }

func (p *strictStrikethroughDelimiterProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}

func (p *strictStrikethroughDelimiterProcessor) OnMatch(consumes int) gast.Node {
	return extast.NewStrikethrough()
}

var strictStrikethroughDelimiterProc = &strictStrikethroughDelimiterProcessor{}

type strictStrikethroughParser struct{}

func (s *strictStrikethroughParser) Trigger() []byte { return []byte{'~'} }

func (s *strictStrikethroughParser) Parse(parent gast.Node, block text.Reader, pc parser.Context) gast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	node := parser.ScanDelimiter(line, before, 1, strictStrikethroughDelimiterProc)
	// Require exactly two tildes ("~~text~~"). A single "~" (or a run of 3+) is
	// left as literal text, so "~1000×" and "(~$5)" render verbatim.
	if node == nil || node.OriginalLength != 2 || before == '~' {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

func (s *strictStrikethroughParser) CloseBlock(parent gast.Node, pc parser.Context) {}

type strictStrikethrough struct{}

// doubleTildeStrikethrough is a drop-in replacement for extension.Strikethrough
// that only recognizes the double-tilde form ("~~text~~").
var doubleTildeStrikethrough goldmark.Extender = &strictStrikethrough{}

func (e *strictStrikethrough) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(&strictStrikethroughParser{}, 500),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(extension.NewStrikethroughHTMLRenderer(), 500),
	))
}
