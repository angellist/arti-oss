package artifacts

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// DenialInfo is the one response in this service that confirms an artifact
// exists to a caller who cannot read it. Every other read collapses "no such
// artifact" and "not for you" into the same 404 (see checkAccess), because a
// 403 on the ordinary read path lets anyone probe the catalog for slugs.
//
// The denial route exists because that uniformity has a cost paid by people
// who were handed a link in good faith: the page they open is indistinguishable
// from a typo, so they cannot tell whether to fix the URL or ask someone for
// access, and the search ends in a Slack thread aimed at whoever shared it.
// What it discloses is the smallest set that ends that search — the document's
// name, its slug, and the address that can grant access. Never its content,
// its description, its labels or its ACL.
//
// Owner is the slug's owner, NOT the row's creator: versioning reassigns
// creator to whoever pushed the latest version, so a delegated writer would be
// named here as the person to ask while having no authority to change the ACL.
type DenialInfo struct {
	NamedSlug *string `json:"named_slug"`
	Version   *int32  `json:"version"`
	Title     string  `json:"title"`
	Owner     string  `json:"owner"`
}

func (s *Service) httpDenialBySlug(w http.ResponseWriter, r *http.Request) {
	caller, ok := denialCaller(r)
	if !ok {
		writeNotDenied(w)
		return
	}
	row, err := s.store.GetBySlug(r.Context(), chi.URLParam(r, "slug"), parseVersion(r))
	s.answerDenial(r.Context(), w, row, caller, err)
}

func (s *Service) httpDenialByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller, ok := denialCaller(r)
	if !ok {
		writeNotDenied(w)
		return
	}
	row, err := s.store.GetByID(r.Context(), id)
	s.answerDenial(r.Context(), w, row, caller, err)
}

// denialCaller returns the caller's address when their credential is an
// interactive session. A service credential (an arti_ API key), an embed or
// app page token, and a device upload token all get nothing: this route
// exists to unstick a person at a browser, and existence is not something a
// long-lived automated credential should be able to sweep the catalog for.
// IsSessionCredential states what a session IS, so a token type added later
// is refused here without anyone remembering to come back.
func denialCaller(r *http.Request) (string, bool) {
	c, ok := auth.ClaimsFromContext(r.Context())
	if !ok || !c.IsSessionCredential() {
		return "", false
	}
	email := auth.EmailFromContext(r.Context())
	return email, email != ""
}

// answerDenial discloses the artifact only on the one branch that means "this
// exists and you are the one being refused". Everything else answers with the
// same 404 the ordinary read path gives, so the route adds no signal beyond
// that single case.
func (s *Service) answerDenial(ctx context.Context, w http.ResponseWriter, row sqlc.Artifact, caller string, lookupErr error) {
	info, ok, err := s.denialFor(ctx, row, caller, lookupErr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeNotDenied(w)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// denialFor returns ok only when row exists and caller is shut out of it.
func (s *Service) denialFor(ctx context.Context, row sqlc.Artifact, caller string, lookupErr error) (DenialInfo, bool, error) {
	if lookupErr != nil {
		if errors.Is(lookupErr, pgstore.ErrNotFound) {
			return DenialInfo{}, false, nil
		}
		return DenialInfo{}, false, lookupErr
	}
	// An archived document is one its owner took down. Naming it here would
	// undo that decision, so it keeps the 404 every other read gives it.
	if row.DeletedAt.Valid {
		return DenialInfo{}, false, nil
	}
	switch err := s.checkAccess(ctx, row, caller); {
	case err == nil:
		// The caller can read it after all — a grant that landed between the
		// page's failed read and this call, or an admin arriving by hand.
		// There is no denial to explain.
		return DenialInfo{}, false, nil
	case errors.Is(err, pgstore.ErrNotFound):
		shutOut, serr := s.deniedWholeSlug(ctx, row, caller)
		if serr != nil || !shutOut {
			return DenialInfo{}, false, serr
		}
		owner, oerr := s.denialOwner(ctx, row)
		if oerr != nil {
			return DenialInfo{}, false, oerr
		}
		slog.Info("served access-denied details",
			"caller", caller,
			"artifact_id", pgstore.UUIDFromPG(row.ArtifactID).String(),
			"slug", row.NamedSlug)
		return DenialInfo{
			NamedSlug: row.NamedSlug,
			Version:   row.Version,
			Title:     row.Title,
			Owner:     owner,
		}, true, nil
	default:
		// A group-lookup failure must not read as a denial, which would
		// disclose an artifact this caller may well be allowed to read.
		return DenialInfo{}, false, err
	}
}

// deniedWholeSlug reports whether `caller` is shut out of the slug entirely,
// rather than merely off the newest version. answerDenial resolves the latest
// row unfiltered, but the ordinary slug read resolves through
// GetLatestBySlugForCaller and silently serves the newest version the caller
// CAN read — so a caller with an older version would otherwise be handed a
// restricted newer version's title and number by this route. Reusing that same
// access-filtered lookup keeps "can read something here" meaning exactly what
// it means on the read path.
func (s *Service) deniedWholeSlug(ctx context.Context, row sqlc.Artifact, caller string) (bool, error) {
	if row.NamedSlug == nil || *row.NamedSlug == "" {
		return true, nil
	}
	_, err := s.store.GetLatestBySlugForCaller(ctx, *row.NamedSlug, caller)
	if errors.Is(err, pgstore.ErrNotFound) {
		return true, nil
	}
	return false, err
}

// denialOwner resolves the address the page tells the reader to ask. Access
// changes answer to the document's owner, which is what IsDocOwner gates every
// ACL write on; row.Creator names whoever pushed the latest version and can be
// a delegated writer with no such authority.
func (s *Service) denialOwner(ctx context.Context, row sqlc.Artifact) (string, error) {
	owner, err := s.store.DocOwner(ctx, row)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return row.Creator, nil
	}
	return owner, nil
}

func writeNotDenied(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not-found", "not found")
}

// writeAppDenial renders the access-denied card for /app/{ident}, which the
// server serves directly and so never reaches the web viewer's denial page.
// It reports false (writing nothing) when there is no denial to explain.
func (s *Service) writeAppDenial(w http.ResponseWriter, r *http.Request, ident string, ver *int32) bool {
	caller, ok := denialCaller(r)
	if !ok {
		return false
	}
	var row sqlc.Artifact
	var err error
	if id, perr := uuid.Parse(ident); perr == nil {
		row, err = s.store.GetByID(r.Context(), id)
	} else {
		row, err = s.store.GetBySlug(r.Context(), ident, ver)
	}
	info, ok, err := s.denialFor(r.Context(), row, caller, err)
	if err != nil {
		slog.Warn("app denial lookup failed", "ident", ident, "err", err)
		return false
	}
	if !ok {
		return false
	}
	p := appDenialPage{Title: info.Title, Owner: info.Owner, Mailto: denialMailto(info)}
	if info.NamedSlug != nil {
		p.Slug = *info.NamedSlug
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Same framing rule as the app it stands in for: trusted hosts iframe /app.
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+s.artifactFrameAncestors())
	w.Header().Del("X-Frame-Options")
	w.WriteHeader(http.StatusForbidden)
	_ = appDenialTmpl.Execute(w, p)
	return true
}

type appDenialPage struct {
	Title, Slug, Owner, Mailto string
}

// denialMailto matches the web AccessDenied page's request email.
func denialMailto(info DenialInfo) string {
	doc := info.Title
	if info.NamedSlug != nil {
		doc += " (" + *info.NamedSlug + ")"
	}
	body := "Hi — I don't have access to this arti document and would like to read it.\n\nDocument: " + doc
	esc := func(v string) string { return strings.ReplaceAll(url.QueryEscape(v), "+", "%20") }
	return "mailto:" + esc(info.Owner) + "?subject=" + esc("arti access request: "+info.Title) + "&body=" + esc(body)
}

var appDenialTmpl = template.Must(template.New("appdenial").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Access required · arti</title>
<style>
body{margin:0;background:#fafafa;color:#171717;font-family:system-ui,-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;
 display:flex;align-items:flex-start;justify-content:center;padding:64px 16px;-webkit-font-smoothing:antialiased}
.card{width:100%;max-width:576px;box-sizing:border-box;background:#fff;border:1px solid #e5e5e5;border-radius:8px;padding:32px;box-shadow:0 1px 2px rgba(0,0,0,.05)}
.kicker{font-size:11px;letter-spacing:.05em;text-transform:uppercase;color:#b45309;font-weight:600;margin:0}
h1{font-size:24px;font-weight:600;margin:12px 0 0;word-break:break-word}
.slug{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:13px;color:#737373;margin:4px 0 0}
p.msg{font-size:14px;line-height:1.5;color:#404040;margin:24px 0 0}
dl{margin:24px 0 0;border-top:1px solid #e5e5e5;padding-top:16px;font-size:14px;display:flex;gap:12px}
dt{width:80px;flex:none;color:#737373}
dd{margin:0;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:13px;word-break:break-all}
a.btn{display:inline-block;margin-top:24px;border-radius:6px;background:#171717;color:#fff;padding:8px 16px;font-size:14px;font-weight:500;text-decoration:none}
a.btn:hover{background:#404040}
</style>
<main class="card">
<p class="kicker">Access required</p>
<h1>{{.Title}}</h1>
{{if .Slug}}<p class="slug">{{.Slug}}</p>{{end}}
<p class="msg">This app exists, but it is not shared with you. Its owner can grant you access.</p>
<dl><dt>Owner</dt><dd>{{.Owner}}</dd></dl>
<a class="btn" href="{{.Mailto}}">Ask {{.Owner}} for access</a>
</main>
`))
