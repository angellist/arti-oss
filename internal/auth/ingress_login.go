package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// Headers forwarded by oauth2-proxy after a successful upstream auth.
// Set by `set_xauthrequest = true` in the AL oauth2-proxy config; nginx
// re-exposes them to the upstream via the auth-response-headers
// annotation on ProtectedIngress.
const (
	IngressEmailHeader         = "X-Auth-Request-Email"
	IngressGroupsHeader        = "X-Auth-Request-Groups"
	loginConfirmCookie         = "arti_login_confirm_identity"
	loginConfirmStateDomain    = "arti-login-confirm-state-v1."
	loginConfirmIdentityDomain = "arti-login-confirm-identity-v1."
)

// IngressLoginHandler mints an `arti_session` cookie (browser flow) or renders
// a confirmation page for a CLI/device code using the email + groups
// oauth2-proxy injects into the request after completing the Google → Dex chain
// at the ingress layer. Code binding happens only after the confirmation POST.
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
func IngressLoginHandler(requiredGroups []string, signer *JWTSigner, pairs PairStore, deviceStore DeviceStore, accessTTL time.Duration, cookieSecure bool, capturer GroupCapturer) http.HandlerFunc {
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
		// Computed once, above the gate, so it's available for both the
		// required-groups check and the login-time IdP-group capture in finish().
		groups := splitGroups(r.Header.Get(IngressGroupsHeader))
		if len(requiredGroups) > 0 {
			if !anyMatch(groups, requiredGroups) {
				http.Error(w, "user not in a required group (have: ["+strings.Join(groups, ", ")+"]; need one of: ["+strings.Join(requiredGroups, ", ")+"])", http.StatusForbidden)
				return
			}
		}

		// Try to extract profile info (name, picture) from the forwarded
		// access token so the session cookie carries them.
		name, picture := decodeTokenProfile(r.Header.Get(ProxyTokenHeader))
		fin := loginFinisher{signer: signer, pairs: pairs, deviceStore: deviceStore,
			accessTTL: accessTTL, cookieSecure: cookieSecure, capturer: capturer}
		fin.finish(w, r, email, name, picture,
			r.URL.Query().Get("cli_code"), r.URL.Query().Get("user_code"), r.URL.Query().Get("return_to"), groups)
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
	capturer     GroupCapturer

	// stateVerified marks a front door that already proved this login
	// transaction was the one this browser started — the OIDC callback,
	// which round-trips cli_code/user_code inside an HMAC-signed state
	// cookie and compares the returned `state` before calling finish().
	// The proxy front door has no such proof (identity arrives in a
	// header, the codes in plain query params), so it leaves this false
	// and relies on the Sec-Fetch-Site check in finish() instead.
	stateVerified bool
}

// GroupCapturer records a user's IdP (SSO) group memberships at login so they
// can later be granted access via `idp:<name>` tokens. Satisfied by
// *pgstore.Store. Both interactive login paths capture through this at the
// shared finish() point, so it works in oidc and proxy modes alike.
type GroupCapturer interface {
	UpsertIdPGroups(ctx context.Context, email string, groups []string) error
}

func (f loginFinisher) finish(w http.ResponseWriter, r *http.Request, email, name, picture, cliCode, userCode, returnTo string, groups []string) {
	// Guard against a cross-site page walking a logged-in user through a
	// CLI-pair or device approval they didn't initiate. Skipped when the
	// front door already verified a signed state blob: in `oidc` mode the
	// callback is a redirect from the issuer and is therefore ALWAYS
	// Sec-Fetch-Site: cross-site, so applying it there would make CLI login
	// and device approval impossible rather than merely safe.
	if !f.stateVerified && (cliCode != "" || userCode != "") && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-site login confirmation rejected", http.StatusForbidden)
		return
	}
	// Snapshot the caller's IdP group memberships (best-effort — a capture
	// failure must never block a login). Runs after the domain + required-group
	// gates upstream, so only admitted users are recorded.
	if f.capturer != nil && email != "" {
		if err := f.capturer.UpsertIdPGroups(r.Context(), email, groups); err != nil {
			log.Printf("idp group capture failed for %s: %v", email, err)
		}
	}
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
		f.renderConfirmation(w, r, email, userCode, "device")
		return
	}

	// CLI flow — pair the code with the verified email.
	if cliCode != "" {
		f.renderConfirmation(w, r, email, cliCode, "cli")
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
	http.Redirect(w, r, sanitizeReturnTo(returnTo), http.StatusFound)
}

func (f loginFinisher) renderConfirmation(w http.ResponseWriter, r *http.Request, email, code, flow string) {
	state := signLoginConfirmState(f.signer, loginConfirmState{
		Email: email, Code: code, Flow: flow, Exp: time.Now().Add(5 * time.Minute).Unix(),
	})
	identity := signLoginConfirmIdentity(f.signer, loginConfirmIdentity{
		Email: email, Exp: time.Now().Add(5 * time.Minute).Unix(),
	})
	http.SetCookie(w, &http.Cookie{
		Name: loginConfirmCookie, Value: identity, Path: "/auth", Secure: f.cookieSecure,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 300,
	})
	heading, intro, footer := "Authorize CLI sign-in", "A terminal on this machine is requesting access to arti as", "Only confirm if you just ran arti login in your own terminal."
	if flow == "device" {
		heading = "Authorize device sign-in"
		intro = "A device is requesting access to arti as"
		footer = "Only confirm if you started this sign-in yourself."
	}
	brandPage{
		Title:   "arti — authorize sign-in",
		Heading: heading,
		Intro:   intro,
		Email:   email,
		Code:    code,
		Note:    "Check that this matches the code shown where you started sign-in. Expires in 5 minutes.",
		Action: &brandAction{
			Method: "POST",
			URL:    "/auth/login/confirm",
			Hidden: []brandField{{Name: "state", Value: state}},
			Submit: "Confirm sign-in",
		},
		Footer: footer,
	}.render(w)
}

// ConfirmLoginHandler performs the explicit same-origin confirmation that
// binds a verified browser identity to a CLI or device code.
func ConfirmLoginHandler(signer *JWTSigner, pairs PairStore, deviceStore DeviceStore, cookieSecure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		state, ok := verifyLoginConfirmState(signer, r.FormValue("state"))
		if !ok || (state.Flow != "cli" && state.Flow != "device") ||
			state.Code == "" || state.Email == "" {
			http.Error(w, "invalid or expired confirmation", http.StatusForbidden)
			return
		}
		identityCookie, err := r.Cookie(loginConfirmCookie)
		if err != nil {
			http.Error(w, "missing login identity", http.StatusUnauthorized)
			return
		}
		identity, ok := verifyLoginConfirmIdentity(signer, identityCookie.Value)
		if !ok ||
			!strings.EqualFold(identity.Email, state.Email) {
			http.Error(w, "login identity mismatch", http.StatusForbidden)
			return
		}
		if ingressEmail := strings.TrimSpace(r.Header.Get(IngressEmailHeader)); ingressEmail != "" &&
			!strings.EqualFold(ingressEmail, state.Email) {
			http.Error(w, "login identity mismatch", http.StatusForbidden)
			return
		}
		if state.Flow == "device" {
			if deviceStore == nil {
				http.Error(w, "device flow not available", http.StatusNotFound)
				return
			}
			if _, err := deviceStore.GetDeviceByUserCode(r.Context(), state.Code); err != nil {
				http.Error(w, "unknown or expired code", http.StatusNotFound)
				return
			}
			if err := deviceStore.ApproveDeviceCode(r.Context(), sqlc.ApproveDeviceCodeParams{
				UserCode: state.Code, Email: strptr(state.Email),
			}); err != nil {
				http.Error(w, "approve failed", http.StatusInternalServerError)
				return
			}
			clearLoginIdentityCookie(w, cookieSecure)
			brandPage{
				Title:   "arti — approved",
				Heading: "Approved",
				Intro:   "Return to your agent — it will receive its token shortly.",
				Done:    true,
			}.render(w)
			return
		}
		if pairs == nil {
			http.Error(w, "CLI flow not available", http.StatusNotFound)
			return
		}
		pairs.Put(state.Code, state.Email)
		clearLoginIdentityCookie(w, cookieSecure)
		brandPage{
			Title:   "arti — signed in",
			Heading: "Signed in",
			Intro:   "You can close this tab and return to the terminal.",
			Done:    true,
		}.render(w)
	}
}

type loginConfirmState struct {
	Email string `json:"e"`
	Code  string `json:"c"`
	Flow  string `json:"f"`
	Exp   int64  `json:"x"`
}

type loginConfirmIdentity struct {
	Email string `json:"e"`
	Exp   int64  `json:"x"`
}

func signLoginConfirmState(signer *JWTSigner, state loginConfirmState) string {
	return signLoginBlob(signer, loginConfirmStateDomain, state)
}

func verifyLoginConfirmState(signer *JWTSigner, value string) (loginConfirmState, bool) {
	var state loginConfirmState
	if !verifyLoginBlob(signer, loginConfirmStateDomain, value, &state) {
		return loginConfirmState{}, false
	}
	return state, true
}

func signLoginConfirmIdentity(signer *JWTSigner, identity loginConfirmIdentity) string {
	return signLoginBlob(signer, loginConfirmIdentityDomain, identity)
}

func verifyLoginConfirmIdentity(signer *JWTSigner, value string) (loginConfirmIdentity, bool) {
	var identity loginConfirmIdentity
	if !verifyLoginBlob(signer, loginConfirmIdentityDomain, value, &identity) {
		return loginConfirmIdentity{}, false
	}
	return identity, true
}

func signLoginBlob(signer *JWTSigner, domain string, payload any) string {
	body, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, signer.key)
	mac.Write([]byte(domain + encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyLoginBlob(signer *JWTSigner, domain, value string, payload any) bool {
	dot := strings.LastIndexByte(value, '.')
	if dot < 0 {
		return false
	}
	encoded, signature := value[:dot], value[dot+1:]
	mac := hmac.New(sha256.New, signer.key)
	mac.Write([]byte(domain + encoded))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(signature), []byte(want)) {
		return false
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(body, payload) != nil {
		return false
	}
	switch p := payload.(type) {
	case *loginConfirmState:
		return time.Now().Unix() <= p.Exp
	case *loginConfirmIdentity:
		return time.Now().Unix() <= p.Exp
	case *mcpConsentState:
		return time.Now().Unix() <= p.Exp
	default:
		return false
	}
}

// sanitizeReturnTo reduces a return_to parameter to a same-site path, and
// falls back to "/" for anything else. Used by both interactive front
// doors, since each finishes through loginFinisher.finish.
//
// Testing the raw parameter is not enough. http.Redirect path.Cleans a
// relative Location before writing it, so the string checked and the
// string sent can differ: `/../\host` passes a leading-`/\` test and is
// then written as `/\host`. Browsers normalise a backslash to a slash,
// which makes that protocol-relative and sends the user off-site at the
// moment they have most reason to trust where they land. So clean first,
// judge the cleaned path, and emit exactly what was judged.
func sanitizeReturnTo(returnTo string) string {
	const home = "/"
	if returnTo == "" {
		return home
	}
	u, err := url.Parse(returnTo)
	// A scheme, an authority, or an opaque body all leave the site. Note
	// that "//host/x" parses as an authority and is caught here.
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return home
	}
	// Judge the decoded path, so a backslash is seen as the slash the
	// browser will treat it as.
	if p := path.Clean("/" + strings.TrimPrefix(u.Path, "/")); strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) {
		return home
	}
	out := path.Clean("/" + strings.TrimPrefix(u.EscapedPath(), "/"))
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func clearLoginIdentityCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: loginConfirmCookie, Value: "", Path: "/auth", MaxAge: -1,
		Secure: secure, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
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
