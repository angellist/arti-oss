package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// In-place text editing of a standalone HTML artifact. Edit mode serves the
// stored page with every start tag numbered (data-arti-eid) plus a bridge
// script that makes leaf text blocks editable. Saving sends {eid, before,
// html} per changed block; the server splices each block's new inner HTML into
// the ORIGINAL bytes, so script-made DOM changes never leak into the new
// version. Publishing goes through the normal create path.

const htmlEditEIDAttr = "data-arti-eid"

// maxHTMLEditRequestBytes caps the edits JSON (the page itself is not sent).
const maxHTMLEditRequestBytes = 4 << 20

// htmlEditInline is the set of elements an editable block may contain. The
// bridge script carries the same list; a block holding anything else is not
// offered for editing.
var htmlEditInline = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true, "br": true, "cite": true,
	"code": true, "data": true, "del": true, "dfn": true, "em": true, "font": true, "i": true,
	"ins": true, "kbd": true, "mark": true, "q": true, "s": true, "samp": true, "small": true,
	"span": true, "strong": true, "sub": true, "sup": true, "time": true, "u": true, "var": true,
	"wbr": true,
}

// htmlEditDrop elements are removed with their content when sanitizing an edit.
var htmlEditDrop = map[string]bool{
	"script": true, "style": true, "template": true, "iframe": true, "object": true, "embed": true,
	"noscript": true, "svg": true, "math": true, "link": true, "meta": true, "base": true,
	"form": true, "input": true, "button": true, "textarea": true, "select": true,
}

// htmlEditNotBlock elements are never edit targets: raw-text containers and
// document structure.
var htmlEditNotBlock = map[string]bool{
	"html": true, "head": true, "body": true, "title": true, "script": true, "style": true,
	"textarea": true, "noscript": true, "template": true, "iframe": true, "xmp": true,
	"plaintext": true, "noembed": true, "noframes": true,
}

var htmlVoid = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "param": true, "source": true, "track": true,
	"wbr": true,
}

// tagHTMLForEdit inserts data-arti-eid="N" into every start tag, numbered in
// source order. applyHTMLTextEdits relies on numbering the same bytes the same way.
func tagHTMLForEdit(src []byte) []byte {
	z := html.NewTokenizer(bytes.NewReader(src))
	out := make([]byte, 0, len(src)+len(src)/20)
	n := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// Raw() at EOF holds any unterminated tail; keep it verbatim.
			out = append(out, z.Raw()...)
			return out
		}
		raw := z.Raw()
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			out = append(out, raw...)
			continue
		}
		cut := tagNameEnd(raw)
		out = append(out, raw[:cut]...)
		out = append(out, ` `+htmlEditEIDAttr+`="`...)
		out = strconv.AppendInt(out, int64(n), 10)
		out = append(out, '"')
		out = append(out, raw[cut:]...)
		n++
	}
}

func tagNameEnd(raw []byte) int {
	for i := 1; i < len(raw); i++ {
		switch raw[i] {
		case ' ', '\t', '\n', '\f', '\r', '/', '>':
			return i
		}
	}
	return len(raw)
}

type htmlTextEdit struct {
	EID    int    `json:"eid"`
	Before string `json:"before"`
	HTML   string `json:"html"`
}

type htmlBlockSpan struct {
	start, end int // byte range of the block's inner content
}

var errEditNotLocated = errors.New("edit target not found in source")

// applyHTMLTextEdits splices each edit's sanitized inner HTML into src. An edit
// is rejected (its eid returned, src left untouched there) when its block cannot
// be bounded in the source or the source text differs from the text the editor
// started from — the sign of content rendered by the page's own script.
func applyHTMLTextEdits(src []byte, edits []htmlTextEdit) ([]byte, []int) {
	want := make(map[int]htmlTextEdit, len(edits))
	for _, e := range edits {
		want[e.EID] = e
	}
	type located struct {
		edit htmlTextEdit
		span htmlBlockSpan
	}
	var found []located
	var rejected []int

	z := html.NewTokenizer(bytes.NewReader(src))
	off, n := 0, 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		raw := z.Raw()
		off += len(raw)
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		eid := n
		n++
		e, ok := want[eid]
		if !ok {
			continue
		}
		delete(want, eid)
		name, _ := z.TagName()
		tag := strings.ToLower(string(name))
		if tt == html.SelfClosingTagToken || htmlVoid[tag] || htmlEditNotBlock[tag] {
			rejected = append(rejected, eid)
			continue
		}
		span, text, err := boundBlock(src, off, tag)
		if err != nil || normalizeEditText(text) != normalizeEditText(e.Before) {
			rejected = append(rejected, eid)
			continue
		}
		found = append(found, located{edit: e, span: span})
	}
	for eid := range want {
		rejected = append(rejected, eid)
	}
	sort.Ints(rejected)

	sort.Slice(found, func(i, j int) bool { return found[i].span.start < found[j].span.start })
	var out bytes.Buffer
	out.Grow(len(src))
	prev := 0
	for _, f := range found {
		if f.span.start < prev { // nested edits: keep the outer one
			rejected = append(rejected, f.edit.EID)
			continue
		}
		out.Write(src[prev:f.span.start])
		out.WriteString(sanitizeEditHTML(f.edit.HTML))
		prev = f.span.end
	}
	out.Write(src[prev:])
	sort.Ints(rejected)
	return out.Bytes(), rejected
}

// boundBlock finds the end of the element whose start tag ends at start,
// requiring well-nested inline-only markup with an explicit closing tag.
func boundBlock(src []byte, start int, tag string) (htmlBlockSpan, string, error) {
	z := html.NewTokenizer(bytes.NewReader(src[start:]))
	off := start
	var stack []string
	var text strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return htmlBlockSpan{}, "", errEditNotLocated
		}
		raw := z.Raw()
		tokStart := off
		off += len(raw)
		switch tt {
		case html.TextToken:
			text.Write(z.Text())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			t := strings.ToLower(string(name))
			if !htmlEditInline[t] {
				return htmlBlockSpan{}, "", errEditNotLocated
			}
			if tt == html.StartTagToken && !htmlVoid[t] {
				stack = append(stack, t)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			t := strings.ToLower(string(name))
			if len(stack) == 0 {
				if t == tag {
					return htmlBlockSpan{start: start, end: tokStart}, text.String(), nil
				}
				return htmlBlockSpan{}, "", errEditNotLocated
			}
			if stack[len(stack)-1] != t {
				return htmlBlockSpan{}, "", errEditNotLocated
			}
			stack = stack[:len(stack)-1]
		}
	}
}

func normalizeEditText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sanitizeEditHTML keeps text and inline formatting from an edited block. The
// frame is untrusted, so anything that could add script or new structure is
// stripped: unknown elements are unwrapped, dangerous ones dropped, and only
// presentational attributes survive.
func sanitizeEditHTML(in string) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(in), ctx)
	if err != nil {
		return html.EscapeString(in)
	}
	var b strings.Builder
	for _, n := range nodes {
		writeSanitized(&b, n)
	}
	return b.String()
}

func writeSanitized(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(textEscaper.Replace(n.Data))
	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if n.Namespace != "" || htmlEditDrop[tag] {
			return
		}
		if !htmlEditInline[tag] {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeSanitized(b, c)
			}
			return
		}
		b.WriteString("<" + tag)
		for _, a := range n.Attr {
			if a.Namespace != "" || !keepEditAttr(tag, strings.ToLower(a.Key), a.Val) {
				continue
			}
			b.WriteString(" " + strings.ToLower(a.Key) + `="` + html.EscapeString(a.Val) + `"`)
		}
		b.WriteString(">")
		if htmlVoid[tag] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeSanitized(b, c)
		}
		b.WriteString("</" + tag + ">")
	}
}

// textEscaper leaves quotes as typed; they need no escaping in text content.
var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\u00a0", "&nbsp;")

var htmlEditAttrs = map[string]bool{
	"class": true, "style": true, "title": true, "lang": true, "dir": true, "id": true,
	"datetime": true, "color": true, "face": true, "size": true, "target": true, "rel": true,
	"role": true, "cite": true, "value": true, "name": true, "download": true, "hreflang": true,
}

func keepEditAttr(tag, key, val string) bool {
	switch {
	case strings.HasPrefix(key, "data-arti-"):
		return false
	case htmlEditAttrs[key], strings.HasPrefix(key, "data-"), strings.HasPrefix(key, "aria-"):
		return true
	case key == "href" && tag == "a":
		return safeEditHref(val)
	}
	return false
}

// safeEditHref keeps relative links and http, https and mailto URLs. Bytes the
// URL parser ignores (C0 controls, space, tab, newline) are removed first so
// they cannot hide a scheme.
func safeEditHref(val string) bool {
	v := strings.Map(func(r rune) rune {
		if r <= 0x20 {
			return -1
		}
		return r
	}, val)
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https", "mailto":
		return true
	}
	return false
}

// SetHTMLEditEnabled turns on in-place text editing of HTML artifacts.
func (s *Service) SetHTMLEditEnabled(on bool) { s.htmlEdit = on }

// htmlEditMode reports whether this content request asks for the edit-mode page.
func (s *Service) htmlEditMode(r *http.Request, row htmlEditRow) bool {
	return s.htmlEdit && r.URL.Query().Get("edit") == "1" && row.isEditableHTML()
}

type htmlEditRow struct {
	artifactType string
	contentType  string
}

func (h htmlEditRow) isEditableHTML() bool {
	return h.artifactType == pgstore.TypeText && strings.HasPrefix(strings.ToLower(h.contentType), "text/html")
}

// writeHTMLEditPage serves the numbered page plus the edit bridge. The comments
// overlay is left out so selecting text while typing never opens a comment draft.
func (s *Service) writeHTMLEditPage(w http.ResponseWriter, rc io.Reader, ct, artifactID string) {
	body, err := io.ReadAll(rc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.setContentSecurityMaybeFullPage(w, ct, false)
	w.Header().Set("Cache-Control", "no-store")
	body = injectHTMLEditBridge(tagHTMLForEdit(body))
	_, _ = w.Write(injectExternalLinkHandler(body, ct, "/api/artifacts/"+artifactID))
}

func injectHTMLEditBridge(body []byte) []byte {
	snippet := []byte("<script>" + htmlEditBridgeJS + "</script>")
	if i := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		return append(out, body[i:]...)
	}
	return append(body, snippet...)
}

// httpApplyHTMLTextEdits returns the stored page with the edits applied, for
// the caller to publish as a new version through POST /api/artifacts. It
// writes nothing itself, so it needs only read access. Rejected eids come back
// in X-Arti-Rejected-Edits.
func (s *Service) httpApplyHTMLTextEdits(w http.ResponseWriter, r *http.Request) {
	if !s.htmlEdit {
		writeError(w, http.StatusNotFound, "not-found", "html editing is not enabled")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHTMLEditRequestBytes)
	var req struct {
		Edits []htmlTextEdit `json:"edits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "invalid edits body: "+err.Error())
		return
	}
	if len(req.Edits) == 0 {
		writeError(w, http.StatusBadRequest, "bad-request", "no edits")
		return
	}
	caller := auth.EmailFromContext(r.Context())
	rc, ct, row, err := s.Content(r.Context(), id, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	defer rc.Close()
	if !(htmlEditRow{artifactType: row.ArtifactType, contentType: ct}).isEditableHTML() {
		writeError(w, http.StatusBadRequest, "bad-request", "only TEXT text/html artifacts support text edits")
		return
	}
	src, err := io.ReadAll(rc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out, rejected := applyHTMLTextEdits(src, req.Edits)
	if len(rejected) == len(req.Edits) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    "edits-not-applied",
			"detail":   "none of the edited text could be found in the stored page; it may be generated by the page's script",
			"rejected": rejected,
		})
		return
	}
	ids := make([]string, len(rejected))
	for i, e := range rejected {
		ids[i] = strconv.Itoa(e)
	}
	w.Header().Set("X-Arti-Rejected-Edits", strings.Join(ids, ","))
	setContentSecurity(w, "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}
