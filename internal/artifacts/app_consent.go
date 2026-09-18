package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// An APP page runs as whoever opens it: the injected bridge token lets its
// code read and write arti and drive every connector the viewer has ever
// granted. Until the viewer has said yes to THIS version (a new version is a
// new artifact_id, so a republish asks again), the document routes serve a
// consent page instead of the app, and mint nothing.

const appConsentCookiePrefix = "arti_app_ok_"

func isHTMLContentType(ct string) bool { return strings.HasPrefix(strings.ToLower(ct), "text/html") }

// SetAppConsentFns wires the consent-record signer + verifier (from the apps
// service) and the cookie Secure flag. With a minter wired but no verifier,
// every APP document load is refused — the gate fails closed.
func (s *Service) SetAppConsentFns(sign func(email, artifactID string) (string, error), verify func(tok string) (email, artifactID string, err error), cookieSecure bool) {
	s.appConsentSign, s.appConsentVerify, s.appConsentSecure = sign, verify, cookieSecure
}

// appConsented reports whether caller has recorded consent for row: either
// for this exact version, or for the slug while the same publisher ships the
// same declared tools. True when no bridge minter is wired: with no token to
// protect there is nothing to gate (local dev without the apps service).
func (s *Service) appConsented(r *http.Request, caller string, row sqlc.Artifact) bool {
	if s.appToken == nil {
		return true
	}
	if s.appConsentVerify == nil || caller == "" {
		return false
	}
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()
	if s.consentCookieMatches(r, caller, appConsentCookiePrefix+aid, aid) {
		return true
	}
	if row.NamedSlug == nil || *row.NamedSlug == "" {
		return false
	}
	return s.consentCookieMatches(r, caller, slugConsentCookie(*row.NamedSlug), s.slugConsentSubject(r.Context(), row))
}

func (s *Service) consentCookieMatches(r *http.Request, caller, name, subject string) bool {
	c, err := r.Cookie(name)
	if err != nil {
		return false
	}
	email, got, err := s.appConsentVerify(c.Value)
	return err == nil && got == subject && strings.EqualFold(email, caller)
}

// slugConsentSubject is what a "trust all versions" record binds to: the slug,
// the version's publisher, and a digest of the declared tools. A version by a
// different publisher, or with a different tool set, needs a fresh yes.
func (s *Service) slugConsentSubject(ctx context.Context, row sqlc.Artifact) string {
	man := s.readAppManifest(ctx, row)
	tools := make([]string, 0, len(man.Tools))
	for _, t := range man.Tools {
		tools = append(tools, t.Server+"/"+t.Tool)
	}
	sort.Strings(tools)
	sum := sha256.Sum256([]byte(strings.Join(tools, "\n")))
	return "slug=" + *row.NamedSlug + "~by=" + strings.ToLower(row.Creator) + "~tools=" + hex.EncodeToString(sum[:8])
}

// slugConsentCookie names the per-slug record; a slug can carry characters a
// cookie name cannot, and the digest also keeps it apart from the UUID names.
func slugConsentCookie(slug string) string {
	sum := sha256.Sum256([]byte(slug))
	return appConsentCookiePrefix + "s" + hex.EncodeToString(sum[:8])
}

// grantAppConsent records the viewer's yes for row as a cookie, and with
// allVersions also for the slug. Path=/ so the record reaches both /app/… and
// /api/artifacts/…/files/… document loads.
func (s *Service) grantAppConsent(w http.ResponseWriter, r *http.Request, row sqlc.Artifact, caller string, allVersions bool) error {
	// A consented viewer sees the app, never the consent page, so a POST that
	// arrives with consent already in place came from the app's own code (or a
	// double submit). It must not widen anything.
	if s.appConsented(r, caller, row) {
		return nil
	}
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()
	if err := s.setConsentCookie(w, caller, appConsentCookiePrefix+aid, aid); err != nil {
		return err
	}
	if allVersions && row.NamedSlug != nil && *row.NamedSlug != "" {
		return s.setConsentCookie(w, caller, slugConsentCookie(*row.NamedSlug), s.slugConsentSubject(r.Context(), row))
	}
	return nil
}

func (s *Service) setConsentCookie(w http.ResponseWriter, caller, name, subject string) error {
	tok, err := s.appConsentSign(caller, subject)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: tok, Path: "/", MaxAge: int((30 * 24 * time.Hour).Seconds()),
		Secure: s.appConsentSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// httpAppConsent is POST /app/{ident}/consent. A form post carries `next`
// (the document URL to return to); a fetch from the viewer chrome carries
// none and gets 204.
func (s *Service) httpAppConsent(w http.ResponseWriter, r *http.Request) {
	if s.appConsentSign == nil {
		http.NotFound(w, r)
		return
	}
	caller := auth.EmailFromContext(r.Context())
	row, err := s.resolveIdent(r.Context(), chi.URLParam(r, "ident"), parseVersion(r), caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	if row.ArtifactType != pgstore.TypeApp {
		writeError(w, http.StatusNotFound, "not-found", "not an APP artifact")
		return
	}
	if err := s.grantAppConsent(w, r, row, caller, r.FormValue("scope") == "slug"); err != nil {
		writeBadOrInternal(w, err)
		return
	}
	next := r.FormValue("next")
	if next == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !safeAppNext(next) {
		next = "/app/" + pgstore.UUIDFromPG(row.ArtifactID).String()
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// safeAppNext admits only a same-origin path to one of the APP document
// routes, so the redirect cannot be pointed off-site.
func safeAppNext(next string) bool {
	if strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return false
	}
	return strings.HasPrefix(next, "/app/") || strings.HasPrefix(next, "/api/artifacts/")
}

// appManifest is the part of arti-app.json the server reads.
type appManifest struct {
	Entry  string         `json:"entry"`
	Params []appParamSpec `json:"params"`
	Tools  []struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	} `json:"tools"`
}

func (s *Service) readAppManifest(ctx context.Context, row sqlc.Artifact) appManifest {
	var man appManifest
	if mb, _, err := s.ReadPackageFile(ctx, row, "arti-app.json"); err == nil {
		_ = json.Unmarshal(mb, &man)
	}
	return man
}

type appConsentTool struct {
	Name   string
	Access string // read | write | llm
}

type appConsentServer struct {
	Server string
	Tools  []appConsentTool
	Writes int
}

// artiToolAccess is exact for arti's own tools; every other server is
// classified by verb below.
var artiToolAccess = map[string]string{
	"get_artifact": "read", "read_artifact": "read", "read_package_file": "read", "list_package_files": "read",
	"list_artifacts": "read", "search_artifacts": "read", "list_artifact_versions": "read", "list_comments": "read",
	"map_get":     "read",
	"add_comment": "write", "reply_to_comment": "write", "resolve_comment": "write", "add_artifact": "write",
	"append_artifact": "write", "update_artifact": "write", "archive_artifact": "write", "map_put": "write", "map_delete": "write",
	"map_snapshot": "write",
}

var (
	writeVerbs = map[string]bool{"add": true, "create": true, "update": true, "delete": true, "remove": true, "send": true, "post": true,
		"put": true, "patch": true, "archive": true, "upload": true, "write": true, "set": true, "edit": true, "reply": true, "resolve": true,
		"move": true, "merge": true, "assign": true, "mark": true, "cancel": true, "insert": true, "append": true, "publish": true,
		"schedule": true, "trigger": true, "dispatch": true, "import": true, "sync": true, "grant": true, "revoke": true, "invite": true}
	readVerbs = map[string]bool{"get": true, "list": true, "search": true, "read": true, "fetch": true, "find": true, "query": true,
		"describe": true, "lookup": true, "download": true, "count": true, "snapshot": true, "check": true, "whoami": true, "export": true,
		"view": true, "show": true, "retrieve": true, "browse": true, "preview": true}
)

// toolAccess labels a declared tool read or write. A write verb anywhere in the
// name wins; a name with no recognised verb is labelled write, since the label
// exists to warn.
func toolAccess(server, tool string) string {
	if server == "llm" {
		return "llm"
	}
	if isArtiServer(server) {
		if a, ok := artiToolAccess[tool]; ok {
			return a
		}
	}
	read := false
	for _, tok := range strings.FieldsFunc(strings.ToLower(tool), func(r rune) bool { return r == '_' || r == '-' || r == '.' || r == '/' }) {
		if writeVerbs[tok] {
			return "write"
		}
		if readVerbs[tok] {
			read = true
		}
	}
	if read {
		return "read"
	}
	return "write"
}

func isArtiServer(server string) bool { return server == "arti" || server == "arti-self" }

type appConsentPage struct {
	Title, Slug, Creator, CreatedAt, WrittenVia, AppID, Next, OpenHref string
	Version                                                            int32
	Automated                                                          bool
	Servers                                                            []appConsentServer
}

// writeAppConsentPage renders the consent page for row in place of the app.
// No sandbox CSP: the page is arti's own, and its form must be able to post.
func (s *Service) writeAppConsentPage(w http.ResponseWriter, r *http.Request, row sqlc.Artifact) {
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()
	man := s.readAppManifest(r.Context(), row)
	byServer := map[string][]appConsentTool{}
	for _, t := range man.Tools {
		byServer[t.Server] = append(byServer[t.Server], appConsentTool{Name: t.Tool, Access: toolAccess(t.Server, t.Tool)})
	}
	var servers []appConsentServer
	for name, tools := range byServer {
		sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
		sv := appConsentServer{Server: name, Tools: tools}
		for _, t := range tools {
			if t.Access != "read" {
				sv.Writes++
			}
		}
		servers = append(servers, sv)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Server < servers[j].Server })

	p := appConsentPage{
		Title: row.Title, Creator: row.Creator, AppID: aid, Servers: servers,
		Next:     r.URL.RequestURI(),
		OpenHref: "/app/" + aid,
	}
	if row.NamedSlug != nil {
		p.Slug = *row.NamedSlug
	}
	if row.Version != nil {
		p.Version = *row.Version
	}
	if row.CreatedAt.Valid {
		p.CreatedAt = row.CreatedAt.Time.UTC().Format("2006-01-02 15:04 UTC")
	}
	if row.WrittenVia != nil {
		p.WrittenVia = *row.WrittenVia
		if row.WrittenViaName != nil && *row.WrittenViaName != "" {
			p.WrittenVia = *row.WrittenViaName
		}
		kind, _, _ := strings.Cut(*row.WrittenVia, ":")
		p.Automated = kind == auth.CredKindService || kind == auth.CredKindAPIKey || kind == auth.CredKindApp
	}
	for _, sc := range row.Scopes {
		if sc == pgstore.ScopeAppCouch {
			p.Automated = true
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Same framing rule as the app it stands in for, and no sandbox: the page
	// must be able to post its form and to render inside an embedded viewer.
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+s.artifactFrameAncestors())
	w.Header().Del("X-Frame-Options")
	_ = appConsentTmpl.Execute(w, p)
}

var appConsentTmpl = template.Must(template.New("appconsent").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Run {{.Title}} · arti</title>
<style>
:root{--teal:#0abfb6;--teal-700:#068e88;--ink:#171717;--n700:#404040;--n500:#737373;--n400:#a3a3a3;--n200:#e5e5e5;--n100:#f5f5f5;--amber:#92400e;--amber-bg:#fef3c7}
*{box-sizing:border-box}
body{margin:0;background:#fafafa;color:var(--ink);font-family:system-ui,-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;
 display:flex;align-items:flex-start;justify-content:center;padding:24px;-webkit-font-smoothing:antialiased}
.card{width:100%;max-width:520px;background:#fff;border:1px solid var(--n200);border-radius:16px;padding:32px 36px 28px;
 box-shadow:0 1px 2px rgba(0,0,0,.04),0 8px 28px -12px rgba(0,0,0,.12)}
.kicker{font-size:11px;letter-spacing:.09em;text-transform:uppercase;color:var(--teal-700);font-weight:600;margin:0 0 8px}
h1{font-size:20px;font-weight:600;letter-spacing:-.02em;margin:0 0 4px;word-break:break-word}
.meta{font-size:13px;color:var(--n500);margin:0 0 18px;line-height:1.6}
.meta code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;color:var(--n700)}
.auto{background:var(--amber-bg);color:var(--amber);border-radius:8px;padding:8px 12px;font-size:13px;margin:0 0 18px}
h2{font-size:12px;letter-spacing:.06em;text-transform:uppercase;color:var(--n500);font-weight:600;margin:18px 0 8px}
.tools{max-height:280px;overflow:auto;border:1px solid var(--n200);border-radius:10px;padding:10px 14px}
ul{margin:0;padding:0 0 0 18px;font-size:13.5px;line-height:1.7;color:var(--n700)}
.tools>ul{padding-left:0;list-style:none}
.tools>ul>li{margin:0 0 8px}
.tools>ul>li:last-child{margin-bottom:0}
.tools ul ul{padding-left:16px;margin-top:2px}
.tools ul ul li{list-style:disc}
li code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px}
.chip{display:inline-block;vertical-align:1px;margin-left:6px;padding:1px 7px;border-radius:999px;font-size:10.5px;font-weight:600;letter-spacing:.04em;text-transform:uppercase;line-height:1.5}
.chip-read{background:#e0f2fe;color:#075985}
.chip-write{background:#fee2e2;color:#991b1b}
.chip-llm{background:var(--amber-bg);color:var(--amber)}
.srv{font-weight:600;color:var(--ink)}
.chip-count{background:var(--n100);color:var(--n500)}
.none{font-size:13.5px;color:var(--n500)}
.warn{font-size:13.5px;color:var(--n700);line-height:1.55;margin:18px 0 20px;padding-top:16px;border-top:1px solid var(--n100)}
.btn{display:block;width:100%;border:0;border-radius:10px;background:var(--teal);color:#fff;font:inherit;font-size:14.5px;font-weight:600;padding:12px;cursor:pointer}
.btn:hover{background:var(--teal-700)}
.opt{display:flex;gap:8px;align-items:flex-start;font-size:13px;color:var(--n700);line-height:1.5;margin:0 0 14px;cursor:pointer}
.opt input{margin:3px 0 0;flex:none}
.opt code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px}
.alt{display:block;text-align:center;margin-top:12px;font-size:12.5px;color:var(--n500)}
.alt a{color:var(--teal-700)}
</style>
<main class="card">
<p class="kicker">Run an app</p>
<h1>{{.Title}}</h1>
<p class="meta">Published by <strong>{{.Creator}}</strong>{{if .WrittenVia}} via <code>{{.WrittenVia}}</code>{{end}}{{if .CreatedAt}} on {{.CreatedAt}}{{end}}.<br>
{{if .Slug}}Slug <code>{{.Slug}}</code> · v{{.Version}}<br>{{end}}
Version <code>{{.AppID}}</code></p>
{{if .Automated}}<p class="auto">This version was published by an automated process, not by a person typing in arti. Treat its code as unreviewed.</p>{{end}}
<h2>Connectors it declares</h2>
{{if .Servers}}<div class="tools"><ul>{{range .Servers}}<li><span class="srv">{{.Server}}</span>{{if .Writes}} <span class="chip chip-count">{{.Writes}} write</span>{{end}}
<ul>{{range .Tools}}<li><code>{{.Name}}</code> <span class="chip chip-{{.Access}}">{{.Access}}</span></li>{{end}}</ul></li>{{end}}</ul></div>
{{else}}<p class="none">No connector tools declared. The app can still read and write arti as you.</p>{{end}}
<p class="warn">The app's code runs in your browser <strong>as you</strong>. It can read every arti document you can read, create and edit documents and comments in your name, and call the connectors above with your access. A <span class="chip chip-write">write</span> tool changes data in your name; the label comes from the tool's name and errs toward write. A new version of this app will ask again.</p>
<form method="post" action="/app/{{.AppID}}/consent" id="arti-consent">
<!-- Inside a sandboxed frame the browser drops the submission before any submit event fires, so the relay hangs off the button's click. -->
<input type="hidden" name="next" value="{{.Next}}">
{{if .Slug}}<label class="opt"><input type="checkbox" name="scope" value="slug"><span>Trust all versions of <code>{{.Slug}}</code>. arti asks again if a different publisher releases a version or the declared tools change.</span></label>{{end}}
<button class="btn" type="submit">I trust this creator, run this app as me</button>
</form>
<p class="alt">Inside a preview? <a href="{{.OpenHref}}" target="_blank" rel="noopener">Open the app in its own tab</a>.</p>
</main>
<script>
(function(){var f=document.querySelector('#arti-consent'),b=f&&f.querySelector('button');if(!b||window.parent===window)return;
b.addEventListener('click',function(e){e.preventDefault();var c=f.querySelector('input[name=scope]');
window.parent.postMessage({source:'arti-app-consent',appId:{{.AppID}},scope:(c&&c.checked)?'slug':''},'*');});})();
</script>
`))
