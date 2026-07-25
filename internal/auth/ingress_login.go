package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// Headers forwarded by oauth2-proxy after a successful upstream auth.
// Set by `set_xauthrequest = true` in the AL oauth2-proxy config; nginx
// re-exposes them to the upstream via the auth-response-headers
// annotation on ProtectedIngress.
const (
	IngressEmailHeader  = "X-Auth-Request-Email"
	IngressGroupsHeader = "X-Auth-Request-Groups"
)

// IngressLoginHandler mints an `arti_session` cookie (browser flow) or
// records a cli_code→email pair (CLI flow) using the email + groups
// oauth2-proxy injects into the request after completing the
// Google → Dex chain at the ingress layer.
//
// We trust the X-Auth-Request-* headers because they only reach the pod
// through nginx's auth-subrequest response, which only fires when
// oauth2-proxy has already verified the upstream session. Re-verifying
// the forwarded access token here would be redundant *and* breaks under
// AL's current Dex configuration where the access token's audience and
// groups claim differ from the ID token oauth2-proxy actually used to
// authenticate the user.
//
// Query params:
//
//	cli_code   if present, store (code → email) in PairStore and render a
//	           "you can close this tab" page (CLI completes via
//	           POST /auth/cli/exchange).
//	return_to  if cli_code is absent, redirect here after setting the
//	           cookie (defaults to "/", must be a relative path).
func IngressLoginHandler(requiredGroups []string, signer *JWTSigner, pairs PairStore, deviceStore DeviceStore, accessTTL time.Duration, cookieSecure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(r.Header.Get(IngressEmailHeader))
		if email == "" {
			http.Error(w, "no "+IngressEmailHeader+" header — is oauth2-proxy in front?", http.StatusUnauthorized)
			return
		}
		if !IsAllowed(email) {
			http.Error(w, "email domain not in allowlist", http.StatusForbidden)
			return
		}
		if len(requiredGroups) > 0 {
			groups := splitGroups(r.Header.Get(IngressGroupsHeader))
			if !anyMatch(groups, requiredGroups) {
				http.Error(w, "user not in a required group (have: ["+strings.Join(groups, ", ")+"]; need one of: ["+strings.Join(requiredGroups, ", ")+"])", http.StatusForbidden)
				return
			}
		}

		// Try to extract profile info (name, picture) from the forwarded
		// access token so the session cookie carries them.
		name, picture := decodeTokenProfile(r.Header.Get(ProxyTokenHeader))
		fin := loginFinisher{signer: signer, pairs: pairs, deviceStore: deviceStore,
			accessTTL: accessTTL, cookieSecure: cookieSecure}
		fin.finish(w, r, email, name, picture,
			r.URL.Query().Get("cli_code"), r.URL.Query().Get("user_code"), r.URL.Query().Get("return_to"))
	}
}

// loginFinisher completes a login once an identity has been verified by
// some front door — the trusted-proxy headers (IngressLoginHandler) or the
// built-in OIDC callback (OIDCLogin). It handles the three outcomes:
// device user_code approval, CLI cli_code pairing, or a browser session.
type loginFinisher struct {
	signer       *JWTSigner
	pairs        PairStore
	deviceStore  DeviceStore
	accessTTL    time.Duration
	cookieSecure bool
}

func (f loginFinisher) finish(w http.ResponseWriter, r *http.Request, email, name, picture, cliCode, userCode, returnTo string) {
	// Device-flow approval — the verified email approves a user_code.
	if userCode != "" {
		if f.deviceStore == nil {
			http.Error(w, "device flow not available", http.StatusNotFound)
			return
		}
		if _, err := f.deviceStore.GetDeviceByUserCode(r.Context(), userCode); err != nil {
			http.Error(w, "unknown or expired code", http.StatusNotFound)
			return
		}
		if err := f.deviceStore.ApproveDeviceCode(r.Context(), sqlc.ApproveDeviceCodeParams{
			UserCode: userCode, Email: strptr(email),
		}); err != nil {
			http.Error(w, "approve failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>arti</title>
<style>body{font-family:system-ui;text-align:center;margin-top:20vh;color:#333}</style>
<h1>Approved &#10003;</h1><p>Return to your agent — it will receive its token shortly.</p>`))
		return
	}

	// CLI flow — pair the code with the verified email.
	if cliCode != "" {
		f.pairs.Put(cliCode, email)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>arti</title>
<style>body{font-family:system-ui;text-align:center;margin-top:20vh;color:#333}h1{font-weight:600}p{color:#666}</style>
<h1>Signed in</h1><p>You can close this tab and return to the terminal.</p>`))
		return
	}

	// Browser flow — mint arti_session cookie and redirect.
	jwt, err := f.signer.Sign(Claims{Email: email, Name: name, Picture: picture, Scopes: []string{"user"}, TTL: f.accessTTL})
	if err != nil {
		http.Error(w, "sign session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    jwt,
		Path:     "/",
		Secure:   f.cookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(f.accessTTL.Seconds()),
	})
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		returnTo = "/"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// splitGroups parses oauth2-proxy's comma-separated X-Auth-Request-Groups
// header. Dex emits groups as `<name>@<domain>` (Google Workspace group
// addresses); we strip the domain suffix so AUTH_REQUIRED_GROUPS can
// stay short ("engineers" matches "engineers@example.com").
func splitGroups(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if at := strings.IndexByte(p, '@'); at >= 0 {
			p = p[:at]
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// anyMatch is case-insensitive, matching the bearer path's hasAnyGroup
// semantics so the same IdP groups pass every auth front door identically.
func anyMatch(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if strings.EqualFold(h, w) {
				return true
			}
		}
	}
	return false
}

// decodeTokenProfile extracts name and picture from a JWT without
// verification. Safe to call on untrusted or non-JWT tokens (returns
// empty strings on any parse failure). Used on the ingress path where
// the token is already trusted via oauth2-proxy.
func decodeTokenProfile(tok string) (name, picture string) {
	parts := strings.SplitN(tok, ".", 4)
	if len(parts) != 3 || parts[1] == "" {
		return "", ""
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	_ = json.Unmarshal(body, &claims)
	return claims.Name, claims.Picture
}
