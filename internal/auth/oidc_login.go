package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// stateCookieName carries the signed login-transaction blob between
// /auth/login and /auth/callback. Scoped to /auth and short-lived.
const stateCookieName = "arti_oidc_state"

// OIDCLoginConfig configures the built-in OpenID Connect login
// (auth.mode "oidc"): arti runs the authorization-code flow itself against
// any compliant issuer, with PKCE and a nonce, and mints the same
// arti_session cookie the ingress login does.
type OIDCLoginConfig struct {
	IssuerURL      string
	ClientID       string
	ClientSecret   string
	Scopes         []string // default: openid email profile
	GroupsClaim    string   // ID-token claim carrying groups; default "groups"
	BaseURL        string   // redirect URI = BaseURL + /auth/callback
	RequiredGroups []string

	Signer       *JWTSigner
	Pairs        PairStore
	DeviceStore  DeviceStore   // may be nil; disables user_code approval
	Capturer     GroupCapturer // may be nil; disables login-time IdP-group capture
	AccessTTL    time.Duration
	CookieSecure bool
	// StateKey signs the login-transaction cookie (HMAC-SHA256). Typically
	// the JWT signing key.
	StateKey []byte
}

// OIDCLogin implements /auth/login and /auth/callback for auth.mode "oidc".
type OIDCLogin struct {
	oauth       oauth2.Config
	verifier    *oidc.IDTokenVerifier
	groupsClaim string
	required    []string
	stateKey    []byte
	secure      bool
	fin         loginFinisher
}

// NewOIDCLogin discovers the issuer (one network round-trip) and returns
// handlers ready to mount.
func NewOIDCLogin(ctx context.Context, cfg OIDCLoginConfig) (*OIDCLogin, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return nil, errors.New("oidc login: issuer URL and client ID are required")
	}
	if len(cfg.StateKey) < 16 {
		return nil, errors.New("oidc login: state key too short")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prov, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc login: provider discovery: %w", err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	groupsClaim := cfg.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	return &OIDCLogin{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     prov.Endpoint(),
			RedirectURL:  strings.TrimRight(cfg.BaseURL, "/") + "/auth/callback",
			Scopes:       scopes,
		},
		verifier:    prov.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		groupsClaim: groupsClaim,
		required:    cfg.RequiredGroups,
		stateKey:    cfg.StateKey,
		secure:      cfg.CookieSecure,
		fin: loginFinisher{
			signer: cfg.Signer, pairs: cfg.Pairs, deviceStore: cfg.DeviceStore,
			accessTTL: cfg.AccessTTL, cookieSecure: cfg.CookieSecure, capturer: cfg.Capturer,
		},
	}, nil
}

// loginState is the signed transaction blob round-tripped through the
// browser while the user is at the issuer. The PKCE verifier lives only
// here (cookie), never in a URL.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	CLICode  string `json:"c,omitempty"`
	UserCode string `json:"u,omitempty"`
	ReturnTo string `json:"r,omitempty"`
	Exp      int64  `json:"e"`
}

// LoginHandler starts the authorization-code flow. It accepts the same
// query params as the ingress login (cli_code, user_code, return_to) and
// carries them through the flow in the signed state cookie.
func (l *OIDCLogin) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := loginState{
			State:    randToken(),
			Nonce:    randToken(),
			Verifier: oauth2.GenerateVerifier(),
			CLICode:  r.URL.Query().Get("cli_code"),
			UserCode: r.URL.Query().Get("user_code"),
			ReturnTo: r.URL.Query().Get("return_to"),
			Exp:      time.Now().Add(10 * time.Minute).Unix(),
		}
		http.SetCookie(w, &http.Cookie{
			Name: stateCookieName, Value: l.signState(st), Path: "/auth",
			MaxAge: 600, Secure: l.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		authURL := l.oauth.AuthCodeURL(st.State,
			oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// CallbackHandler finishes the flow: validates state, exchanges the code
// (PKCE), verifies the ID token (signature, audience, nonce), applies the
// domain and group policy, then completes exactly like the ingress login
// (device approval / CLI pairing / browser session).
func (l *OIDCLogin) CallbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(stateCookieName)
		if err != nil {
			http.Error(w, "missing login state — restart at /auth/login", http.StatusBadRequest)
			return
		}
		// One-shot: clear the transaction cookie whatever happens next.
		http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: "", Path: "/auth", MaxAge: -1,
			Secure: l.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		st, ok := l.verifyState(ck.Value)
		if !ok || time.Now().Unix() > st.Exp {
			http.Error(w, "invalid or expired login state — restart at /auth/login", http.StatusBadRequest)
			return
		}
		if sub := subtleCompare(r.URL.Query().Get("state"), st.State); !sub {
			http.Error(w, "state mismatch — restart at /auth/login", http.StatusBadRequest)
			return
		}
		if e := r.URL.Query().Get("error"); e != "" {
			http.Error(w, "issuer returned error: "+e, http.StatusBadGateway)
			return
		}

		tok, err := l.oauth.Exchange(r.Context(), r.URL.Query().Get("code"),
			oauth2.VerifierOption(st.Verifier))
		if err != nil {
			http.Error(w, "code exchange failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		rawID, _ := tok.Extra("id_token").(string)
		if rawID == "" {
			http.Error(w, "issuer returned no id_token", http.StatusBadGateway)
			return
		}
		idt, err := l.verifier.Verify(r.Context(), rawID)
		if err != nil {
			http.Error(w, "id_token verification failed: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if !subtleCompare(idt.Nonce, st.Nonce) {
			http.Error(w, "nonce mismatch — restart at /auth/login", http.StatusBadRequest)
			return
		}

		var claims map[string]any
		if err := idt.Claims(&claims); err != nil {
			http.Error(w, "id_token claims: "+err.Error(), http.StatusBadGateway)
			return
		}
		email, _ := claims["email"].(string)
		email = strings.TrimSpace(email)
		if email == "" {
			http.Error(w, "id_token carries no email claim (request the email scope at the issuer)", http.StatusForbidden)
			return
		}
		// Honor email_verified when the issuer emits it: an explicitly
		// unverified address must not become an identity. Absent claim →
		// trust the issuer's email (many IdPs only assert verified mail).
		// Some IdPs (e.g. Cognito) emit the claim as a STRING — cover both.
		if unverified(claims["email_verified"]) {
			http.Error(w, "email is not verified at the issuer", http.StatusForbidden)
			return
		}
		if !IsAllowed(email) {
			http.Error(w, "email domain not in allowlist", http.StatusForbidden)
			return
		}
		groups := claimGroups(claims[l.groupsClaim])
		if len(l.required) > 0 && !anyMatch(groups, l.required) {
			http.Error(w, "user not in a required group (have: ["+strings.Join(groups, ", ")+
				"]; need one of: ["+strings.Join(l.required, ", ")+"])", http.StatusForbidden)
			return
		}
		name, _ := claims["name"].(string)
		picture, _ := claims["picture"].(string)

		l.fin.finish(w, r, email, name, picture, st.CLICode, st.UserCode, st.ReturnTo, groups)
	}
}

// unverified reports whether an email_verified claim value explicitly says
// false — as a JSON bool or as the string form some IdPs emit.
func unverified(v any) bool {
	switch t := v.(type) {
	case bool:
		return !t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "false")
	}
	return false
}

// claimGroups extracts group names from an ID-token claim value, stripping
// any @domain suffix so required_groups can stay short — the same
// normalization splitGroups applies to the proxy header.
func claimGroups(v any) []string {
	items, ok := v.([]any)
	if !ok {
		if s, isStr := v.(string); isStr && s != "" {
			return splitGroups(s)
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, isStr := it.(string)
		if !isStr {
			continue
		}
		if at := strings.IndexByte(s, '@'); at >= 0 {
			s = s[:at]
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (l *OIDCLogin) signState(st loginState) string {
	body, _ := json.Marshal(st)
	b := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, l.stateKey)
	mac.Write([]byte(b))
	return b + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (l *OIDCLogin) verifyState(v string) (loginState, bool) {
	dot := strings.LastIndexByte(v, '.')
	if dot < 0 {
		return loginState{}, false
	}
	b, sig := v[:dot], v[dot+1:]
	mac := hmac.New(sha256.New, l.stateKey)
	mac.Write([]byte(b))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return loginState{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return loginState{}, false
	}
	var st loginState
	if err := json.Unmarshal(body, &st); err != nil {
		return loginState{}, false
	}
	return st, true
}

func subtleCompare(a, b string) bool {
	return a != "" && hmac.Equal([]byte(a), []byte(b))
}

func randToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
