package artifacts

import (
	"bytes"
	"context"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ServeForEmbed renders the artifact at ident (slug or UUID) full-page into an
// iframe, as identity `caller`, framable by `frameAncestors` (a CSP source list,
// e.g. "'self' https://app.frontapp.com"). surface is the embed surface name,
// used only to build the public sibling-files base path for packaged types.
//
// It applies the app-grade sandbox to EVERY type — so a single doc is as
// interactive (links, popups) as an APP — and renders by type:
//   - APP: the app entry + bridge (reuses serveAppRow).
//   - PACKAGE: the package entry HTML, sibling assets via the embed files path.
//   - single HTML: served as-is.
//   - markdown: rendered to a styled HTML document with <base target=_blank>.
//   - anything else (pdf/image/…): inline.
//
// Returns false (writing nothing) when the artifact doesn't resolve or isn't
// readable by caller, so the handler can render its own placeholder.
func (s *Service) ServeForEmbed(w http.ResponseWriter, r *http.Request, surface, ident string, ver *int32, caller, frameAncestors string) (served bool) {
	row, err := s.resolveIdent(r.Context(), ident, ver, caller)
	if err != nil {
		return false
	}
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()

	switch row.ArtifactType {
	case pgstore.TypeApp:
		// serveAppRow returns a non-nil error (writing nothing) if the entry can't
		// be read; for the embed we ignore the class and fall through to the
		// placeholder rather than surfacing a JSON error.
		// nil stale notice: an embed pins a version deliberately (surface
		// config), and its host owns the surrounding chrome — a "newer version"
		// strip inside someone else's iframe is arti chrome in the wrong place.
		return s.serveAppRow(w, r, row, caller, frameAncestors, s.embedFilesBase(surface, caller, aid), "", nil, nil) == nil
	case pgstore.TypePackage:
		return s.serveEmbedPackageEntry(w, r, row, caller, frameAncestors, s.embedFilesBase(surface, caller, aid))
	}

	return s.serveEmbedBody(w, r, row, frameAncestors)
}

// ServeForEmbedPinned is ServeForEmbed for a viewer-mode surface. It resolves
// as the real signed-in viewer, so resolveIdent's checkAccess is the access gate
// — exactly the rule that applies at /s/<slug>. pinnedArtifactID is the UUID the
// viewer's token was minted for; if the requested ident resolves to a different
// artifact the request is refused, so a token for artifact A cannot serve
// artifact B (which matters because slug_allow may be a wide glob).
//
// Returns false (writing nothing) on any miss, so the handler renders its own
// placeholder and never distinguishes "absent" from "not yours".
func (s *Service) ServeForEmbedPinned(w http.ResponseWriter, r *http.Request, surface, ident string, ver *int32, viewer, frameAncestors, pinnedArtifactID string) (served, pinMismatch bool) {
	row, err := s.resolveIdent(r.Context(), ident, ver, viewer)
	if err != nil {
		return false, false
	}
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()
	if pinnedArtifactID == "" || !strings.EqualFold(aid, pinnedArtifactID) {
		return false, true
	}
	switch row.ArtifactType {
	case pgstore.TypeApp:
		return s.serveAppRow(w, r, row, viewer, frameAncestors, s.embedFilesBase(surface, viewer, aid), "", nil, nil) == nil, false
	case pgstore.TypePackage:
		return s.serveEmbedPackageEntry(w, r, row, viewer, frameAncestors, s.embedFilesBase(surface, viewer, aid)), false
	}
	return s.serveEmbedBody(w, r, row, frameAncestors), false
}

// ServeForEmbedUser is ServeForEmbed for a user-mode surface: there is NO
// serve-time caller — the route's secret + slug_allow checks are the whole
// gate for the static content (so any secret-holder can fetch the page's
// HTML/JS pre-auth; the plan's §7 documents this), and the viewer's identity
// only enters per tool call via the /auth/embed/app-token popup handshake. An
// APP page is therefore served with the needs-user bridge (no token) and its
// sibling files resolve through an email-less, artifact-scoped files token.
// origins (the surface's embedder allowlist) reaches the bridge so a minted
// token may be relayed to the embedding host — see injectAppBridgeUser.
func (s *Service) ServeForEmbedUser(w http.ResponseWriter, r *http.Request, surface, ident string, ver *int32, frameAncestors string, origins []string) (served bool) {
	row, err := s.resolveIdentAny(r.Context(), ident, ver)
	if err != nil {
		return false
	}
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()

	switch row.ArtifactType {
	case pgstore.TypeApp:
		return s.serveAppRow(w, r, row, "", frameAncestors, s.embedFilesBaseUser(surface, aid), surface, origins, nil) == nil
	case pgstore.TypePackage:
		return s.serveEmbedPackageEntry(w, r, row, "", frameAncestors, s.embedFilesBaseUser(surface, aid))
	}

	return s.serveEmbedBody(w, r, row, frameAncestors)
}

// serveEmbedBody renders a single-body artifact (TEXT / ATTACHMENT) for an
// embed surface — the identity-free tail shared by both surface modes.
func (s *Service) serveEmbedBody(w http.ResponseWriter, r *http.Request, row sqlc.Artifact, frameAncestors string) bool {
	rc, err := s.store.Content(r.Context(), row)
	if err != nil {
		return false
	}
	defer rc.Close()
	ct := row.ContentType

	if isMarkdown(ct) {
		src, rerr := io.ReadAll(rc)
		if rerr != nil {
			return false
		}
		setContentSecurityEmbed(w, "text/html; charset=utf-8", frameAncestors)
		_, _ = w.Write(renderMarkdownDoc(src, row.Title))
		return true
	}

	// HTML and inline-renderable bytes (pdf/image/json/…) stream as-is with the
	// embed framing. HTML additionally gets the sandbox (set inside the helper).
	setContentSecurityEmbed(w, ct, frameAncestors)
	_, _ = io.Copy(w, rc)
	return true
}

// resolveIdentAny resolves an ident (slug or UUID) with NO caller access check
// — user-mode embed surfaces only, where the surface secret already gated the
// request. Soft-deleted artifacts stay hidden: archiving must still pull a doc
// off every surface.
func (s *Service) resolveIdentAny(ctx context.Context, ident string, ver *int32) (sqlc.Artifact, error) {
	var row sqlc.Artifact
	var err error
	if id, perr := uuid.Parse(ident); perr == nil {
		row, err = s.store.GetByID(ctx, id)
	} else {
		row, err = s.store.GetBySlug(ctx, ident, ver)
	}
	if err != nil {
		return row, err
	}
	if row.DeletedAt.Valid {
		return sqlc.Artifact{}, pgstore.ErrNotFound
	}
	return row, nil
}

// serveEmbedPackageEntry serves a PACKAGE artifact's entry HTML full-page (no app
// bridge — it's a plain HTML package, not an APP), with sibling assets resolving
// through filesBase.
func (s *Service) serveEmbedPackageEntry(w http.ResponseWriter, r *http.Request, row sqlc.Artifact, caller, frameAncestors, filesBase string) bool {
	entry := ""
	if m, e := s.ListPackageFiles(r.Context(), row); e == nil && m.EntryPoint != "" {
		entry = m.EntryPoint
	}
	if entry == "" {
		entry = "index.html"
	}
	body, ct, err := s.ReadPackageFile(r.Context(), row, entry)
	if err != nil {
		return false
	}
	body = injectBaseHref(body, ct, filesBase)
	setContentSecurityEmbed(w, ct, frameAncestors)
	_, _ = w.Write(body)
	return true
}

// ServeEmbedFile serves one file from a PACKAGE/APP artifact (identified by UUID
// string) as `caller`, for the embed sibling-asset path. Subresources (js/css/
// img) need no framing; a navigated sibling HTML page gets the app-grade sandbox
// + frameAncestors so it stays framable. Returns false on miss.
func (s *Service) ServeEmbedFile(w http.ResponseWriter, r *http.Request, artifactID, path, caller, frameAncestors, filesRoot string) bool {
	return s.serveEmbedFile(w, r, artifactID, path, caller, frameAncestors, filesRoot, true)
}

// ServeEmbedFilePublic is ServeEmbedFile with no caller access check — for
// user-mode surfaces, where the route's verified files token (scoped to
// exactly this artifact, minted only after the surface secret passed) is the
// credential, mirroring how the doc itself was served.
func (s *Service) ServeEmbedFilePublic(w http.ResponseWriter, r *http.Request, artifactID, path, frameAncestors, filesRoot string) bool {
	return s.serveEmbedFile(w, r, artifactID, path, "", frameAncestors, filesRoot, false)
}

func (s *Service) serveEmbedFile(w http.ResponseWriter, r *http.Request, artifactID, path, caller, frameAncestors, filesRoot string, checkCaller bool) bool {
	id, err := uuid.Parse(artifactID)
	if err != nil {
		return false
	}
	// Percent-decode the path like the other file routes (filePathParam), so
	// assets with spaces/encoded chars in their names resolve instead of 404ing.
	if dec, derr := url.PathUnescape(path); derr == nil {
		path = dec
	}
	row, err := s.store.GetByID(r.Context(), id)
	if err != nil {
		return false
	}
	if checkCaller {
		if aerr := s.checkAccess(r.Context(), row, caller); aerr != nil {
			return false
		}
	} else if row.DeletedAt.Valid {
		return false // archived artifacts stay hidden even to a live files token
	}
	body, ct, err := s.ReadPackageFile(r.Context(), row, path)
	if err != nil {
		return false
	}
	setContentSecurityEmbed(w, ct, frameAncestors)
	// In production the full-page viewer's in-content links resolve to this
	// token-scoped route (see filesBaseFor), not /files/*, so the file-nav
	// reporter must be injected here too or the URL wouldn't track navigation
	// past the entry document. No-op on non-HTML; inert in embed surfaces (no
	// listener there).
	body = injectFileNavReporter(body, ct, path)
	body = injectExternalLinkHandler(body, ct, filesRoot)
	_, _ = w.Write(body)
	return true
}

// MountFileToken registers the public, token-scoped sibling-file route that
// backs filesBaseFor's <base href> (see its doc comment in server.go for why
// a rendered page's own asset loads can't rely on the cookie-gated
// /api/artifacts/{id}/files/ route in production). Must be mounted OUTSIDE
// the cookie/bearer auth middleware — like the apps proxy and embed
// surfaces, it authenticates the request itself via appTokenVerify.
func (s *Service) MountFileToken(r chi.Router) {
	r.Get("/api/artifacts/{id}/files-token/{token}/*", s.httpFileByToken)
}

func (s *Service) httpFileByToken(w http.ResponseWriter, r *http.Request) {
	if s.appTokenVerify == nil {
		http.NotFound(w, r)
		return
	}
	email, aid, err := s.appTokenVerify(chi.URLParam(r, "token"))
	if err != nil || aid == "" || aid != chi.URLParam(r, "id") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// frame-ancestors: the artifact allowlist, NOT a hardcoded 'self'. This route
	// backs the <base href> injected into every served PACKAGE page, so an
	// in-content link click navigates the viewer's content frame HERE. When the
	// viewer itself is embedded by a trusted internal origin (couch's side panel),
	// that frame's ancestor chain includes the embedder — 'self' would block the
	// second page exactly as X-Frame-Options blocked the first. The embed surfaces
	// keep their own per-surface lists; they ride /embed/<surface>/_files/, not
	// this route.
	if !s.ServeEmbedFile(w, r, aid, chi.URLParam(r, "*"), email, s.artifactFrameAncestors(), "/api/artifacts/"+aid+"/files-token/"+chi.URLParam(r, "token")+"/") {
		http.NotFound(w, r)
	}
}

// embedFilesBase is the <base href> for a packaged artifact's sibling files:
// a public, token-scoped path so assets load without the cookie-authed
// /api/.../files route. Falls back to the authed path when no app-token minter
// is wired (local dev with auth disabled, where the cookie path is open anyway).
func (s *Service) embedFilesBase(surface, caller, artifactID string) string {
	if s.appToken == nil {
		return "/api/artifacts/" + artifactID + "/files/"
	}
	tok, err := s.appToken(caller, artifactID)
	if err != nil || tok == "" {
		return "/api/artifacts/" + artifactID + "/files/"
	}
	return "/embed/" + surface + "/_files/" + tok + "/"
}

// embedFilesBaseUser is embedFilesBase for user-mode surfaces: there is no
// serve-time caller to mint an email-carrying token for, so sibling files ride
// an email-less token scoped to this artifact alone (SignEmbedFilesToken) —
// minted only here, after the surface's secret + slug_allow gates passed. Same
// local-dev fallback as embedFilesBase.
func (s *Service) embedFilesBaseUser(surface, artifactID string) string {
	if s.embedFilesToken == nil {
		return "/api/artifacts/" + artifactID + "/files/"
	}
	tok, err := s.embedFilesToken(artifactID)
	if err != nil || tok == "" {
		return "/api/artifacts/" + artifactID + "/files/"
	}
	return "/embed/" + surface + "/_files/" + tok + "/"
}

// setContentSecurityEmbed sets the Content-Type and the embed CSP: the app-grade
// sandbox for HTML (so links/popups work like an APP) plus frame-ancestors, and
// for non-HTML just frame-ancestors. Either way it drops the global
// X-Frame-Options: SAMEORIGIN so the surface origin can frame the response.
func setContentSecurityEmbed(w http.ResponseWriter, ct, fa string) {
	w.Header().Set("Content-Type", ct)
	if fa == "" {
		fa = "'self'"
	}
	switch {
	case strings.HasPrefix(strings.ToLower(ct), "text/html"):
		w.Header().Set("Content-Security-Policy",
			"sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-downloads; frame-ancestors "+fa)
	case !isScriptSafeMedia(ct):
		// Scriptable non-HTML (svg/xml/…) also gets the script-less sandbox so a
		// sibling file served into an embedding surface can't run JS on the arti
		// origin. Framing is still governed by frame-ancestors.
		w.Header().Set("Content-Security-Policy", "sandbox; frame-ancestors "+fa)
	default:
		w.Header().Set("Content-Security-Policy", "frame-ancestors "+fa)
	}
	w.Header().Del("X-Frame-Options")
}

func isMarkdown(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/markdown") || strings.HasPrefix(ct, "text/x-markdown")
}

// renderMarkdownDoc converts markdown to a complete, styled HTML document for
// full-page embedding. goldmark runs WITHOUT WithUnsafe, so any raw HTML in the
// markdown is escaped — the rendered output is trusted-safe (and the sandbox is
// defense in depth). <base target=_blank> makes links open a new browser tab
// rather than trapping the panel.
func renderMarkdownDoc(src []byte, title string) []byte {
	var content bytes.Buffer
	// GFM minus its stock strikethrough: extension.Strikethrough matches a
	// single "~", which mangles prose like "~1000×" / "(~$5)". We substitute a
	// double-tilde-only strikethrough (doubleTildeStrikethrough) and keep the
	// rest of GFM (tables, autolinks, task lists).
	md := goldmark.New(goldmark.WithExtensions(
		extension.Table,
		extension.Linkify,
		extension.TaskList,
		doubleTildeStrikethrough,
	))
	if err := md.Convert(src, &content); err != nil {
		content.Reset()
		content.WriteString("<pre>")
		content.WriteString(html.EscapeString(string(src)))
		content.WriteString("</pre>")
	}
	body := spaceTightEmphasis(content.Bytes())
	var doc bytes.Buffer
	doc.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	doc.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	doc.WriteString(`<base target="_blank" rel="noopener">`)
	doc.WriteString("<title>")
	doc.WriteString(html.EscapeString(title))
	doc.WriteString("</title><style>")
	doc.WriteString(embedMarkdownCSS)
	doc.WriteString("</style></head><body>")
	doc.Write(body)
	doc.WriteString("</body></html>")
	return doc.Bytes()
}

// Go twin of spaceTightEmphasis() in web/lib/markdown.tsx — keep the two in
// lockstep. Emphasis delimiters often carry the only separation between two
// words (an email subject quoted verbatim: "Re: ***PAST DUE***Document
// Request"); the parser consumes them and the render collides. Where emphasis
// abuts a letter or digit with no separator at all, inject a zero-content
// marker that carries a small inline padding (.arti-emph-gap below). RE2 has
// no lookaround, so the neighbouring rune is captured and written back.
// Punctuation and real spaces are left exactly as they are.
var (
	tightEmphasisClose = regexp.MustCompile(`</(em|strong|del)>([\p{L}\p{N}])`)
	tightEmphasisOpen  = regexp.MustCompile(`([\p{L}\p{N}])<(em|strong|del)>`)
)

const emphGapHTML = `<span class="arti-emph-gap"></span>`

// Scripts that run words together with no inter-word space. "Tight" is
// meaningless there — `**粗体**文字` is ordinary continuous text, and a gap
// would insert a word break the author never wrote. The abutting rune decides.
var noSpaceScripts = []*unicode.RangeTable{
	unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul,
	unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar, unicode.Tibetan,
}

func spaceTightEmphasis(src []byte) []byte {
	src = injectEmphGap(src, tightEmphasisClose, true)
	return injectEmphGap(src, tightEmphasisOpen, false)
}

// injectEmphGap inserts the gap marker at the tag↔word boundary of every match
// whose abutting rune belongs to a space-separating script. gapAfterTag says
// which side the tag is on: true for the closing form (`</em>W`, where the rune
// is capture 2), false for the opening form (`W<em>`, capture 1). RE2 has no
// lookaround, so the script test lives here rather than in the pattern.
func injectEmphGap(src []byte, re *regexp.Regexp, gapAfterTag bool) []byte {
	matches := re.FindAllSubmatchIndex(src, -1)
	if matches == nil {
		return src
	}
	var out bytes.Buffer
	last := 0
	for _, m := range matches {
		start, end := m[2], m[3] // opening form: the rune is capture 1
		if gapAfterTag {
			start, end = m[4], m[5] // closing form: capture 2
		}
		r, _ := utf8.DecodeRune(src[start:end])
		if unicode.IsOneOf(noSpaceScripts, r) {
			continue
		}
		at := end // opening form: gap goes after the word rune, before the tag
		if gapAfterTag {
			at = start
		}
		out.Write(src[last:at])
		out.WriteString(emphGapHTML)
		last = at
	}
	out.Write(src[last:])
	return out.Bytes()
}

// embedMarkdownCSS is a small, self-contained readable stylesheet for rendered
// markdown docs in a side panel. Intentionally minimal — no external fonts.
const embedMarkdownCSS = `
:root{color-scheme:light dark}
body{max-width:46rem;margin:0 auto;padding:24px 20px 64px;
  font:15px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;
  color:#1a1a1a;background:#fff;word-wrap:break-word}
@media(prefers-color-scheme:dark){body{color:#e6e6e6;background:#1a1a1a}}
h1,h2,h3,h4{line-height:1.25;margin:1.6em 0 .5em;font-weight:650}
h1{font-size:1.6em;margin-top:0}h2{font-size:1.3em}h3{font-size:1.1em}
p,ul,ol,blockquote,table,pre{margin:0 0 1em}
a{color:#2563eb;text-decoration:none}a:hover{text-decoration:underline}
@media(prefers-color-scheme:dark){a{color:#6ea8fe}}
code{font:13px ui-monospace,SFMono-Regular,Menlo,monospace;
  background:rgba(127,127,127,.15);padding:.15em .35em;border-radius:4px}
pre{background:rgba(127,127,127,.12);padding:14px 16px;border-radius:8px;overflow:auto}
pre code{background:none;padding:0}
blockquote{border-left:3px solid rgba(127,127,127,.4);padding-left:1em;color:#666;margin-left:0}
table{border-collapse:collapse;width:100%}
th,td{border:1px solid rgba(127,127,127,.3);padding:6px 10px;text-align:left}
em,i{padding-inline-end:.045em}
.arti-emph-gap{padding-inline-end:.19em}
img{max-width:100%}
hr{border:0;border-top:1px solid rgba(127,127,127,.3);margin:2em 0}
`
