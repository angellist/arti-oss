package auth

import (
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"
	"unicode"
)

// The browser-facing auth pages (CLI confirmation, device approval, and the
// two "you're done" pages) are the only arti surfaces served by the Go binary
// rather than the Next app, so they can't reach the web app's Tailwind theme
// or its next/font bundles. To still look like arti, this file carries the
// two brand assets inline:
//
//   - assets/logo.png — the 96px teal mark, same artwork as web/public/logo.png
//   - assets/petrona-arti.woff2 — Petrona 700 subset to the four glyphs of the
//     "arti" wordmark (3 KB; SIL OFL, see assets/PETRONA-OFL.txt)
//
// Both are emitted as data: URIs in the page's own <style>/<img>, so a page
// render is a single request with no subresources — which matters because
// these pages are served under /auth/*, a path family the ingress routes to
// this pod while everything else goes to the web app. Body text uses the
// system UI stack (Geist isn't embeddable at a sane size and system-ui is
// what the confirmation page always used).

//go:embed assets/logo.png
var brandLogoPNG []byte

//go:embed assets/petrona-arti.woff2
var brandWordmarkWOFF2 []byte

var (
	brandLogoURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(brandLogoPNG)
	brandFontURI = "data:font/woff2;base64," + base64.StdEncoding.EncodeToString(brandWordmarkWOFF2)
)

// brandPage is the content of one auth page. Everything but Title and Heading
// is optional; empty fields drop their section.
type brandPage struct {
	Title   string // <title>
	Heading string // <h1>
	Intro   string // sentence under the heading
	Email   string // renders the identity chip
	Label   string // caption above Code (default "verification code")
	Code    string // renders the code panel
	Note    string // small line under the code (expiry, matching hint)
	Action  *brandAction
	Cancel  string // href for the quiet "Cancel" link under the button
	Footer  string // fine print under the divider
	Done    bool   // success page: teal check badge instead of a form
}

type brandAction struct {
	Method string
	URL    string
	Hidden []brandField
	Submit string
}

type brandField struct{ Name, Value string }

// Initials is the 1–2 letter avatar for the identity chip: "ada.lovelace@…" → AL.
func (p brandPage) Initials() string {
	local, _, _ := strings.Cut(p.Email, "@")
	var out []rune
	for _, part := range strings.FieldsFunc(local, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == '+'
	}) {
		for _, r := range part {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
			}
			break
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// CodeLabel is Label with its default.
func (p brandPage) CodeLabel() string {
	if p.Label == "" {
		return "verification code"
	}
	return p.Label
}

func (p brandPage) render(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The template is parsed at init and every field is escaped by
	// html/template, so an execute error here is not reachable in practice.
	_ = brandTmpl.Execute(w, brandPageData{brandPage: p, Logo: template.URL(brandLogoURI), Font: template.URL(brandFontURI)})
}

type brandPageData struct {
	brandPage
	Logo template.URL
	Font template.URL
}

var brandTmpl = template.Must(template.New("authpage").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<style>
@font-face{font-family:'arti Wordmark';src:url({{.Font}}) format('woff2');font-weight:700;font-display:swap}
:root{--teal:#0abfb6;--teal-700:#068e88;--ink:#171717;--n700:#404040;--n500:#737373;--n400:#a3a3a3;--n200:#e5e5e5;--n100:#f5f5f5}
*{box-sizing:border-box}
html,body{height:100%}
body{margin:0;background:#fafafa;color:var(--ink);-webkit-font-smoothing:antialiased;
 font-family:system-ui,-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;
 display:flex;align-items:center;justify-content:center;padding:24px}
.card{width:100%;max-width:420px;background:#fff;border:1px solid var(--n200);border-radius:16px;
 box-shadow:0 1px 2px rgba(0,0,0,.04),0 8px 28px -12px rgba(0,0,0,.12);padding:36px 36px 28px;text-align:center}
.brand{display:flex;align-items:center;justify-content:center;gap:9px;margin-bottom:26px}
.brand img{width:30px;height:30px;display:block}
.brand span{font-family:'arti Wordmark',ui-serif,Georgia,serif;font-weight:700;letter-spacing:-.03em;font-size:26px;line-height:1}
.badge{width:44px;height:44px;margin:0 auto 16px;border-radius:999px;background:#eefbfa;color:var(--teal-700);
 display:flex;align-items:center;justify-content:center;font-size:22px;line-height:1}
h1{font-size:20px;font-weight:600;letter-spacing:-.02em;margin:0 0 6px}
.sub{margin:0 0 22px;font-size:13.5px;color:var(--n500);line-height:1.5}
.who{display:inline-flex;align-items:center;gap:8px;background:var(--n100);border:1px solid var(--n200);
 border-radius:999px;padding:5px 12px 5px 6px;font-size:13px;color:var(--n700);margin-bottom:20px;max-width:100%;
 word-break:break-all}
.who .av{flex:0 0 auto;width:22px;height:22px;border-radius:999px;background:var(--teal);color:#fff;font-size:11px;
 font-weight:600;display:flex;align-items:center;justify-content:center}
.codewrap{background:#eefbfa;border:1px solid #cdefec;border-radius:10px;padding:12px 10px;margin-bottom:8px}
.codelabel{font-size:10.5px;letter-spacing:.09em;text-transform:uppercase;color:var(--teal-700);font-weight:600;margin-bottom:5px}
.code{font-family:ui-monospace,SFMono-Regular,'SF Mono',Menlo,Consolas,monospace;font-size:15px;font-weight:500;
 letter-spacing:.02em;color:#0f3f3d;word-break:break-all;font-variant-ligatures:none}
.hint{font-size:11.5px;color:var(--n400);margin:0 0 22px}
.btn{display:block;width:100%;border:0;border-radius:10px;background:var(--teal);color:#fff;font-family:inherit;
 font-size:14.5px;font-weight:600;padding:12px;cursor:pointer;box-shadow:0 1px 2px rgba(6,142,136,.35)}
.btn:hover{background:var(--teal-700)}
.btn:focus-visible{outline:2px solid var(--teal-700);outline-offset:2px}
.cancel{display:inline-block;margin-top:12px;font-size:12.5px;color:var(--n500);text-decoration:none}
.cancel:hover{color:var(--n700);text-decoration:underline}
.foot{margin-top:24px;padding-top:16px;border-top:1px solid var(--n100);font-size:11.5px;color:var(--n400);line-height:1.5}
.foot code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
</style>
<main class="card">
<div class="brand"><img src="{{.Logo}}" alt=""><span>arti</span></div>
{{if .Done}}<div class="badge" aria-hidden="true">&#10003;</div>{{end}}
<h1>{{.Heading}}</h1>
{{if .Intro}}<p class="sub">{{.Intro}}</p>{{end}}
{{if .Email}}<div class="who"><span class="av">{{.Initials}}</span>{{.Email}}</div>{{end}}
{{if .Code}}<div class="codewrap"><div class="codelabel">{{.CodeLabel}}</div><div class="code">{{.Code}}</div></div>{{end}}
{{if .Note}}<p class="hint">{{.Note}}</p>{{end}}
{{with .Action}}<form method="{{.Method}}" action="{{.URL}}">
{{range .Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
{{end}}<button class="btn" type="submit">{{.Submit}}</button></form>{{end}}
{{if .Cancel}}<a class="cancel" href="{{.Cancel}}">Cancel</a>{{end}}
{{if .Footer}}<p class="foot">{{.Footer}}</p>{{end}}
</main>
`))
