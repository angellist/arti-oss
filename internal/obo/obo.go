// Package obo makes arti an OAuth 2.1 *client* / on-behalf-of (OBO) token broker
// for MCP authorization servers, realizing the user→arti→upstream leg. For a
// given APP viewer it brokers a per-user access token: discover the resource's
// authorization server (RFC 9728 protected-resource metadata → RFC 8414 AS
// metadata), dynamically register a client (RFC 7591), run authorization-code +
// PKCE (the page opens the consent popup), exchange the code at the callback,
// and store/refresh the token. It implements apps.TokenProvider.
//
// Multi-pod correct: the PKCE state is a self-contained AEAD token (any pod
// opens it with the shared key — no server-side state), and the DCR client +
// per-user tokens live in a shared Store (Postgres) with secret fields
// encrypted at rest. The AS-metadata cache is the only in-process state and is a
// pure, re-fetchable per-pod cache.
//
// Provider-agnostic: nothing here is Runlayer-specific — it's standard OAuth
// 2.1 + PKCE + DCR + resource indicators (RFC 8707). Runlayer is just the first
// configured provider (apps.ServerConfig with Auth="oauth").
package obo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/angellist/arti-oss/internal/apps"
)

// Broker brokers per-user OBO tokens. Safe for concurrent use.
type Broker struct {
	httpc       *http.Client
	callbackURL string // the redirect_uri registered + sent on every leg
	// identify returns the arti-authenticated email of the browser hitting the
	// callback, from the verified (signed) arti_session cookie — never a
	// client-settable header (the callback is on the public ingress). "" if it
	// can't be determined (e.g. local dev). Binds the consent to its initiator.
	identify func(*http.Request) string
	store    Store
	cipher   *Cipher
	stateTTL time.Duration

	mu   sync.Mutex
	meta map[string]asMeta // resource URL → AS metadata (per-pod, re-fetchable cache)
}

// New builds a broker whose OAuth callback is callbackBase + /oauth/obo/callback.
// identify resolves the arti-authenticated caller email at the callback (may be
// nil). store + cipher must be non-nil.
func New(callbackBase string, identify func(*http.Request) string, store Store, cipher *Cipher) *Broker {
	return &Broker{
		httpc:       &http.Client{Timeout: 30 * time.Second},
		callbackURL: strings.TrimRight(callbackBase, "/") + "/oauth/obo/callback",
		identify:    identify,
		store:       store,
		cipher:      cipher,
		stateTTL:    10 * time.Minute,
		meta:        map[string]asMeta{},
	}
}

type asMeta struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Registration  string `json:"registration_endpoint"`
}

// authState is sealed into the opaque `state` parameter (no server-side store).
// Short field names keep the encrypted token compact in the URL.
type authState struct {
	Email    string `json:"e"`
	Verifier string `json:"v"`
	Resource string `json:"r"`
	Scope    string `json:"s,omitempty"`
	IAT      int64  `json:"t"`
}

// ─── apps.TokenProvider ──────────────────────────────────────────────

// BearerFor returns a valid per-user access token for (email, resource), "" if
// the user hasn't authorized yet (or the token expired and can't be refreshed).
func (b *Broker) BearerFor(ctx context.Context, email string, sc apps.ServerConfig) (string, error) {
	t, ok, err := b.store.GetToken(ctx, email, sc.ResourceURL)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	if time.Now().Before(t.Expiry.Add(-30 * time.Second)) {
		return t.Access, nil
	}
	if t.Refresh == "" {
		return "", nil // expired, no refresh → re-auth
	}
	nt, err := b.refresh(ctx, email, sc, t.Refresh)
	if err != nil {
		return "", nil // refresh failed → re-auth
	}
	return nt.Access, nil
}

// AuthorizeURL builds the consent URL for (email, resource): discover the AS,
// ensure a shared DCR client, mint a PKCE challenge, and seal the pending
// authorization into the opaque `state` token.
func (b *Broker) AuthorizeURL(ctx context.Context, email string, sc apps.ServerConfig) (string, error) {
	meta, err := b.discover(ctx, sc.ResourceURL)
	if err != nil {
		return "", err
	}
	cl, err := b.ensureClient(ctx, meta)
	if err != nil {
		return "", err
	}
	verifier := randURL(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state, err := b.cipher.SealState(authState{
		Email: email, Verifier: verifier, Resource: sc.ResourceURL, Scope: sc.Scope, IAT: time.Now().Unix(),
	})
	if err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cl.ClientID)
	q.Set("redirect_uri", b.callbackURL)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if sc.Scope != "" {
		q.Set("scope", sc.Scope)
	}
	q.Set("resource", sc.ResourceURL) // RFC 8707 resource indicator
	return meta.Authorization + "?" + q.Encode(), nil
}

// ─── callback (public route) ─────────────────────────────────────────

// Callback handles GET /oauth/obo/callback: open the sealed state, exchange the
// code for a per-user token, persist it, and return a tiny page that signals the
// opener (the APP page) and closes. Works on any pod — the state is
// self-contained and the client/token live in the shared Store.
func (b *Broker) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		b.fail(w, "authorization server denied authorization: "+e+" "+q.Get("error_description"))
		return
	}
	state, code := q.Get("state"), q.Get("code")
	st, err := b.cipher.OpenState(state)
	// A bad/forged/wrong-key token fails OpenState; an old one fails the TTL.
	if err != nil || code == "" || time.Since(time.Unix(st.IAT, 0)) > b.stateTTL {
		b.fail(w, "invalid or expired authorization state")
		return
	}
	// Bind the consent to the initiating arti user: the browser's identity (from
	// the signed arti_session cookie on this top-level navigation — never a
	// client-settable header on this public route) must match the email sealed
	// in the state. Stops a shared authorize_url being completed as someone else.
	if b.identify != nil {
		if caller := b.identify(r); caller != "" && !strings.EqualFold(caller, st.Email) {
			b.fail(w, "authorization was completed by a different user than the one who started it")
			return
		}
	}
	meta, err := b.discover(r.Context(), st.Resource)
	if err != nil {
		b.fail(w, "authorization-server discovery failed: "+err.Error())
		return
	}
	cl, ok, err := b.store.GetClient(r.Context(), meta.Issuer)
	if err != nil || !ok {
		b.fail(w, "no registered client for this authorization server")
		return
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", b.callbackURL)
	form.Set("client_id", cl.ClientID)
	if cl.ClientSecret != "" {
		form.Set("client_secret", cl.ClientSecret)
	}
	form.Set("code_verifier", st.Verifier)
	form.Set("resource", st.Resource)

	t, err := b.tokenRequest(r.Context(), meta.Token, form)
	if err != nil {
		b.fail(w, "token exchange failed: "+err.Error())
		return
	}
	if err := b.store.PutToken(r.Context(), st.Email, st.Resource, t); err != nil {
		b.fail(w, "could not persist token: "+err.Error())
		return
	}
	b.success(w)
}

// ─── token endpoint calls ────────────────────────────────────────────

func (b *Broker) refresh(ctx context.Context, email string, sc apps.ServerConfig, refreshTok string) (TokenRec, error) {
	meta, err := b.discover(ctx, sc.ResourceURL)
	if err != nil {
		return TokenRec{}, err
	}
	cl, ok, err := b.store.GetClient(ctx, meta.Issuer)
	if err != nil {
		return TokenRec{}, err
	}
	if !ok || meta.Token == "" {
		return TokenRec{}, fmt.Errorf("obo: no client/metadata for refresh")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshTok)
	form.Set("client_id", cl.ClientID)
	if cl.ClientSecret != "" {
		form.Set("client_secret", cl.ClientSecret)
	}
	form.Set("resource", sc.ResourceURL)
	t, err := b.tokenRequest(ctx, meta.Token, form)
	if err != nil {
		return TokenRec{}, err
	}
	if t.Refresh == "" {
		t.Refresh = refreshTok // server didn't rotate; keep the old one
	}
	if err := b.store.PutToken(ctx, email, sc.ResourceURL, t); err != nil {
		return TokenRec{}, err
	}
	return t, nil
}

func (b *Broker) tokenRequest(ctx context.Context, endpoint string, form url.Values) (TokenRec, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenRec{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := b.httpc.Do(req)
	if err != nil {
		return TokenRec{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		return TokenRec{}, fmt.Errorf("token endpoint %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var tr struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return TokenRec{}, fmt.Errorf("token endpoint: bad JSON: %w", err)
	}
	if tr.Access == "" {
		return TokenRec{}, fmt.Errorf("token endpoint: no access_token in response")
	}
	exp := time.Now().Add(55 * time.Minute)
	if tr.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return TokenRec{Access: tr.Access, Refresh: tr.Refresh, Expiry: exp}, nil
}

// ─── discovery (RFC 9728 → RFC 8414) + DCR ───────────────────────────

// discover resolves the resource's authorization server and returns its
// metadata. It first tries RFC 9728 protected-resource metadata (so a resource
// can delegate to an AS on another host), falling back to assuming the AS lives
// at the resource's own origin (true for Runlayer). Result cached per-pod.
func (b *Broker) discover(ctx context.Context, resourceURL string) (asMeta, error) {
	b.mu.Lock()
	m, ok := b.meta[resourceURL]
	b.mu.Unlock()
	if ok {
		return m, nil
	}
	origin, err := resourceOrigin(resourceURL)
	if err != nil {
		return asMeta{}, err
	}
	asURL := origin // fallback when there's no protected-resource metadata
	if servers := b.fetchProtectedResource(ctx, origin); len(servers) > 0 {
		asURL = strings.TrimRight(servers[0], "/")
	}
	meta, err := b.fetchASMeta(ctx, asURL)
	if err != nil {
		return asMeta{}, err
	}
	b.mu.Lock()
	b.meta[resourceURL] = meta
	b.mu.Unlock()
	return meta, nil
}

// fetchProtectedResource returns the authorization_servers from RFC 9728
// protected-resource metadata, or nil if it's absent/unreachable (→ caller
// falls back to the resource origin).
func (b *Broker) fetchProtectedResource(ctx context.Context, origin string) []string {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/.well-known/oauth-protected-resource", nil)
	req.Header.Set("Accept", "application/json")
	res, err := b.httpc.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var prm struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if json.Unmarshal(body, &prm) != nil {
		return nil
	}
	return prm.AuthorizationServers
}

func (b *Broker) fetchASMeta(ctx context.Context, asURL string) (asMeta, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, asURL+"/.well-known/oauth-authorization-server", nil)
	req.Header.Set("Accept", "application/json")
	res, err := b.httpc.Do(req)
	if err != nil {
		return asMeta{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		return asMeta{}, fmt.Errorf("AS metadata %d at %s", res.StatusCode, asURL)
	}
	var m asMeta
	if err := json.Unmarshal(body, &m); err != nil {
		return asMeta{}, err
	}
	if m.Authorization == "" || m.Token == "" {
		return asMeta{}, fmt.Errorf("incomplete AS metadata at %s", asURL)
	}
	// issuer is REQUIRED by RFC 8414 and is the DCR-client / token key. We must
	// NOT fall back to the discovery URL: that URL can differ per pod (RFC 9728
	// path vs origin fallback), which would split the key — one pod registers a
	// client under one issuer while another pod's callback looks up a different
	// key. Keying on the AS-reported issuer is path-independent and stable.
	if m.Issuer == "" {
		return asMeta{}, fmt.Errorf("AS metadata at %s omits the required issuer", asURL)
	}
	return m, nil
}

// ensureClient returns the shared DCR client for the AS, registering one (RFC
// 7591) if none is stored yet. Concurrent registrations across pods converge:
// PutClient is INSERT … ON CONFLICT DO NOTHING, then we re-read the winner.
func (b *Broker) ensureClient(ctx context.Context, meta asMeta) (ClientReg, error) {
	if cl, ok, err := b.store.GetClient(ctx, meta.Issuer); err != nil {
		return ClientReg{}, err
	} else if ok {
		return cl, nil
	}
	if meta.Registration == "" {
		return ClientReg{}, fmt.Errorf("obo: AS %s has no registration_endpoint (DCR unsupported)", meta.Issuer)
	}
	reg := map[string]any{
		"client_name":                "arti app-serving",
		"redirect_uris":              []string{b.callbackURL},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "client_secret_post",
	}
	body, _ := json.Marshal(reg)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, meta.Registration, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := b.httpc.Do(req)
	if err != nil {
		return ClientReg{}, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		return ClientReg{}, fmt.Errorf("DCR %d: %s", res.StatusCode, strings.TrimSpace(string(rb)))
	}
	var dr struct {
		ID     string `json:"client_id"`
		Secret string `json:"client_secret"`
	}
	if err := json.Unmarshal(rb, &dr); err != nil {
		return ClientReg{}, err
	}
	if dr.ID == "" {
		return ClientReg{}, fmt.Errorf("DCR: no client_id in response")
	}
	mine := ClientReg{ClientID: dr.ID, ClientSecret: dr.Secret}
	if err := b.store.PutClient(ctx, meta.Issuer, mine); err != nil {
		return ClientReg{}, err
	}
	// The stored row is authoritative. PutClient is INSERT … ON CONFLICT DO
	// NOTHING, so a pod that registered concurrently may have won and ITS
	// client_id is the one the callback will load from the DB. Always return the
	// stored row — never fall back to our local result (which could differ and
	// would make AuthorizeURL's client_id mismatch the callback's), and surface a
	// read failure rather than hand back a possibly-unregistered client.
	cl, ok, err := b.store.GetClient(ctx, meta.Issuer)
	if err != nil {
		return ClientReg{}, err
	}
	if !ok {
		return ClientReg{}, fmt.Errorf("obo: client registration for %s missing right after insert", meta.Issuer)
	}
	return cl, nil
}

// ─── helpers ─────────────────────────────────────────────────────────

func resourceOrigin(resourceURL string) (string, error) {
	u, err := url.Parse(resourceURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("obo: bad resource URL %q", resourceURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

func randURL(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (b *Broker) success(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset=utf-8><title>Connected</title>
<body style="font:15px system-ui;padding:48px;color:#1c1a17;background:#f7f4ee">
<p>✅ Connected. You can close this window.</p>
<script>try{if(window.opener){window.opener.postMessage({arti_oauth:"done"},"*");}}catch(e){}setTimeout(function(){window.close();},300);</script>`))
	// postMessage targets "*" because the opener (the APP page) is opaque-origin
	// (sandbox) — its origin can't be named. The bridge authenticates the message
	// by matching the popup window handle (e.source === pop), not the origin.
}

func (b *Broker) fail(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`<!doctype html><meta charset=utf-8><title>Authorization failed</title>
<body style="font:15px system-ui;padding:48px;color:#1c1a17;background:#f7f4ee">
<p>Authorization failed: ` + html.EscapeString(msg) + `</p>
<script>setTimeout(function(){window.close();},2500);</script>`))
}
