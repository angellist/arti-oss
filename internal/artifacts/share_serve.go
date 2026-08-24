package artifacts

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// The public serve side of external share links. These routes are mounted
// OUTSIDE the auth middleware: the token in the path authenticates the
// request, and there is deliberately no identity anywhere in this file — no
// caller string is read, inferred or synthesised, which is what keeps a share
// link from ever inheriting the admin short-circuit in checkAccess.

// shareFrameAncestors is the CSP source list for every share response. A
// shared document is not an embed: nothing may frame it.
const shareFrameAncestors = "'none'"

// MountShare registers the public share routes. Mounted only when
// ARTI_SHARE_ENABLED is on; the FE-proxy guard and the log-redaction entry are
// registered unconditionally, so with the feature off a /share request 404s
// here rather than falling through to the FE and collecting an SSO redirect.
func (s *Service) MountShare(r chi.Router) {
	r.Get("/share/{token}", s.httpShareDoc)
	r.Get("/share/{token}/download", s.httpShareDownload)
	r.Get("/share/{token}/_files/*", s.httpShareFile)
}

// shareHeaders sets the headers every share response carries, refusals
// included. Called before anything is written, on every path, so the six
// refusal branches cannot drift apart by header set — a divergence there
// would undo the uniform 404 as surely as a different body would.
func shareHeaders(w http.ResponseWriter) {
	// The token is in the URL path, so any outbound request the rendered
	// document makes would otherwise carry the live credential in Referer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cache-Control", "no-store, private")
}

// shareNotFound writes the one and only refusal. Every branch goes through
// here so that an unknown token, a revoked link, an expired link, a missing
// artifact, an archived document and an APP are byte-identical on the wire.
func shareNotFound(w http.ResponseWriter) {
	shareHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "not found\n")
}

func (s *Service) httpShareDoc(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	row, linkID, err := s.ResolveShare(r.Context(), token)
	if err != nil {
		shareNotFound(w)
		return
	}

	shareHeaders(w)
	// Track write errors: serveEmbedBody reports success from its own return
	// value and discards the io.Copy error for non-markdown bodies, so a
	// stream truncated mid-flight would otherwise still be counted as an open.
	tw := &writeErrTracker{ResponseWriter: w}
	// serveEmbedPackageEntry takes a caller it never reads. Pass "" — the one
	// place an implementer would be tempted to synthesise an identity.
	served := false
	if pgstore.IsPackageLike(row.ArtifactType) {
		served = s.serveEmbedPackageEntry(tw, r, row, "", shareFrameAncestors, "/share/"+token+"/_files/")
	} else {
		served = s.serveEmbedBody(tw, r, row, shareFrameAncestors)
	}
	if !served {
		shareNotFound(w)
		return
	}
	if tw.err != nil {
		slog.Default().Warn("share document truncated", "err", tw.err)
		return
	}
	s.recordShareOpen(r, linkID)
}

// recordShareOpen appends the audit row, and is called only where a document
// was actually handed over.
//
// Two things are deliberate. It runs on the success path only: recording on a
// refusal would add a write-latency signature separating revoked from unknown,
// which is the channel the uniform 404 exists to close — so once a link is
// revoked, further attempts by whoever holds it stop appearing. And it runs
// AFTER the body is written, so a render that fails and falls back to the 404
// does not leave an open counted against a document nobody received.
//
// A failure here is logged and swallowed: a document that cannot be read
// because its audit row failed to write is a worse outcome than a missing
// audit row.
func (s *Service) recordShareOpen(r *http.Request, linkID pgtype.UUID) {
	if err := s.RecordShareOpen(r.Context(), linkID,
		realIPOf(r), auth.PeerAddrFromContext(r.Context()), r.UserAgent()); err != nil {
		slog.Default().Warn("share open not recorded", "err", err)
	}
}

func (s *Service) httpShareDownload(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	row, linkID, err := s.ResolveShare(r.Context(), token)
	if err != nil {
		shareNotFound(w)
		return
	}

	rc, cerr := s.store.Content(r.Context(), row)
	if cerr != nil {
		shareNotFound(w)
		return
	}
	defer rc.Close()

	shareHeaders(w)
	// The sandbox CSP goes on the download too, not just the rendered view.
	// Without it a text/html body served here executes SAME-ORIGIN on arti's
	// domain with the viewer's cookie in scope: the global security headers
	// set nosniff and X-Frame-Options but no CSP, and nosniff does not help
	// when the declared type genuinely is text/html. frame-ancestors 'none'
	// keeps it unframable; the sandbox omits allow-same-origin, so any script
	// runs in an opaque origin.
	setContentSecurityEmbed(w, row.ContentType, shareFrameAncestors)

	// Content-Disposition is UNCONDITIONAL. It used to be skipped when
	// deriveDownloadFilename returned "" — which it does for a
	// whitespace-only title, and Create accepts one because it rejects only
	// the exact empty string (server.go:173). That combination served
	// attacker-authored HTML for rendering instead of download.
	name := deriveDownloadFilename(row.Title, row.ContentType)
	if name == "" {
		name = "download" + extForContentType(row.ContentType)
	}
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
			sanitizeASCIIFilename(name), urlEscapeRFC5987(name)))
	if _, cerr := io.Copy(w, rc); cerr != nil {
		// The bytes did not all reach the recipient; do not count an open.
		slog.Default().Warn("share download truncated", "err", cerr)
		return
	}
	s.recordShareOpen(r, linkID)
}

// httpShareFile serves a PACKAGE's sibling assets. It re-runs the FULL
// resolution rather than trusting the entry page's earlier one, so revoking a
// link, letting it expire, or archiving the document takes effect on the very
// next asset fetch. That is stronger than the embed path's signed files token,
// which stays valid for its own TTL after the document is pulled.
func (s *Service) httpShareFile(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	row, _, err := s.ResolveShare(r.Context(), token)
	if err != nil {
		shareNotFound(w)
		return
	}
	// Siblings get the same headers as the entry document. Referrer-Policy is
	// the one that matters most: the token is in the path, so a navigated HTML
	// sibling under the weaker global policy would put the live credential in
	// the Referer of every outbound request it makes.
	shareHeaders(w)
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()
	// Scoped to the artifact the token resolved to. A path with dot segments
	// can at worst name a strange entry inside this same zip; pkgzip looks
	// entries up by name in the archive, so there is no filesystem to escape.
	if !s.ServeEmbedFilePublic(w, r, aid, chi.URLParam(r, "*"),
		shareFrameAncestors, "/share/"+token+"/_files/") {
		shareNotFound(w)
	}
}

// writeErrTracker remembers the first write failure so a caller can tell a
// completed response from a truncated one. Only used to decide whether an open
// is recorded — an open should mean a document actually reached someone.
type writeErrTracker struct {
	http.ResponseWriter
	err error
}

func (t *writeErrTracker) Write(b []byte) (int, error) {
	n, err := t.ResponseWriter.Write(b)
	if err != nil && t.err == nil {
		t.err = err
	}
	return n, err
}

// realIPOf returns what the handler sees as the client address. After chi's
// RealIP that is header-derived, which is trustworthy here only because
// auth.StripSpoofableIPHeaders removes the one client-IP header this edge does
// not manage before RealIP reads any of them.
func realIPOf(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
