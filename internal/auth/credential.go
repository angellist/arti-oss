package auth

import "context"

// Credential names WHICH credential authenticated a request, as distinct from
// WHO it authenticated as. The two diverge whenever a bearer is held by
// something other than the person it belongs to: an API key authenticates as
// its owner's email, so a write made by an agent holding a copied key is
// otherwise indistinguishable from that person working in a browser.
type Credential struct {
	Kind   string // CredKind*
	ID     string // stable per credential where one exists; "" otherwise
	Label  string // human-chosen name; API keys only
	Source string // CredSource*: how the credential reached the server
}

// Credential kinds. Only API keys and device tokens have a per-credential
// identity to record; a user bearer and a browser session are both signed with
// the same key and carry identical claims, so the kind is as specific as
// attribution can get for them.
const (
	CredKindAPIKey  = "apikey"
	CredKindDevice  = "device"
	CredKindToken   = "token" // user bearer: CLI or MCP, indistinguishable
	CredKindSession = "session"
	CredKindService = "service"
	// CredKindApp is the scoped token arti injects into an APP artifact's page.
	// Its ID is the app's own artifact UUID: a write an app makes runs as the
	// viewer, but the viewer never presented a bearer of their own, so the app
	// is the honest answer to "which credential wrote this".
	CredKindApp = "app"
)

// Credential transport. A browser can only present a cookie or, behind
// oauth2-proxy, a forwarded access token; a script chooses Authorization.
const (
	CredSourceBearer = "bearer"
	CredSourceCookie = "cookie"
	CredSourceProxy  = "proxy"
)

// Ref is the stored form: "apikey:<uuid>", "device:<family>", or the bare kind
// for credentials with no identity of their own. It is what lands in
// artifacts.written_via and keys credential_usage, so it must stay stable.
func (c Credential) Ref() string {
	if c.Kind == "" {
		return ""
	}
	if c.ID == "" {
		return c.Kind
	}
	return c.Kind + ":" + c.ID
}

// Display is what a person should see: the owner's name for the credential
// when it has one, otherwise the reference.
func (c Credential) Display() string {
	if c.Label != "" {
		return c.Label
	}
	return c.Ref()
}

// FromBrowser reports whether this credential is one only a browser holds: the
// arti_session cookie, carrying the session marker its mint stamps on.
//
// Transport alone is not enough. A CLI access token is claim-for-claim
// identical to a session cookie, so a caller could simply send its own bearer
// as `Cookie: arti_session=<token>` and be read as a person at a browser. The
// marker is what a bearer cannot mint for itself.
//
// A caller replaying a stolen cookie VALUE still passes, which is accepted:
// that caller already holds full rights as the user.
func (c Credential) FromBrowser() bool {
	return c.Kind == CredKindSession
}

// PredatesSessionMarker reports a cookie-borne credential that carries no
// session marker — a session minted before this shipped. Browser-only routes
// use it to say "sign in again" rather than refusing a real person with a
// message about bearer tokens. It stops being reachable once every pre-existing
// cookie has expired.
func (c Credential) PredatesSessionMarker() bool {
	return c.Kind == CredKindToken && c.Source == CredSourceCookie
}

// credentialFor derives the credential from verified claims and the transport
// they arrived on. Order matters: a key or a device family identifies itself,
// and only an otherwise-unmarked token falls back to its transport.
func credentialFor(c Claims, source string) Credential {
	switch {
	case c.Typ == TokenTypeAPIKey:
		return Credential{Kind: CredKindAPIKey, ID: c.KeyID, Label: c.KeyName, Source: source}
	case c.Fam != "":
		return Credential{Kind: CredKindDevice, ID: c.Fam, Source: source}
	case c.Typ == TokenTypeSession && (source == CredSourceCookie || source == CredSourceProxy):
		return Credential{Kind: CredKindSession, Source: source}
	default:
		return Credential{Kind: CredKindToken, Source: source}
	}
}

// CredentialFromContext returns the credential that authenticated the request.
// The zero value means the request was not authenticated by RequireAuth — an
// unauthenticated route, or a test injecting an identity directly.
func CredentialFromContext(ctx context.Context) Credential {
	v, _ := ctx.Value(ctxCredential).(Credential)
	return v
}

// WithAppCredential marks ctx as authenticated by the token arti injected into
// APP artifact appID, labelled with the app's title. The apps proxy sets it
// before running an arti tool in-process, so the write is attributed to the
// app rather than inheriting whatever the viewer's own session would stamp.
func WithAppCredential(ctx context.Context, appID, title string) context.Context {
	return withCredential(ctx, Credential{
		Kind:   CredKindApp,
		ID:     appID,
		Label:  title,
		Source: CredSourceBearer,
	})
}

// WithTestCredential injects a credential for tests that bypass the middleware.
func WithTestCredential(ctx context.Context, c Credential) context.Context {
	return context.WithValue(ctx, ctxCredential, c)
}

func withCredential(ctx context.Context, c Credential) context.Context {
	return context.WithValue(ctx, ctxCredential, c)
}
