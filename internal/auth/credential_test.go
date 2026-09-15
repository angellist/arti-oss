package auth

import "testing"

// The bypass this marker exists to close: a CLI access token is claim-for-claim
// identical to a session cookie, so an agent holding one could send it as
// `Cookie: arti_session=<token>` and be read as a person at a browser — which
// would have handed it back the key-minting route the gate removes.
func TestCLITokenInACookieIsNotABrowserSession(t *testing.T) {
	cliToken := Claims{Email: "alice@example.com", Scopes: []string{"user"}} // no Typ, as issueTokens mints it

	got := credentialFor(cliToken, CredSourceCookie)

	if got.FromBrowser() {
		t.Fatalf("a CLI token presented as a cookie passed FromBrowser (kind %q)", got.Kind)
	}
	if !got.PredatesSessionMarker() {
		t.Errorf("kind/source = %q/%q; an unmarked cookie must be recognisable so a real person gets told to sign in again", got.Kind, got.Source)
	}
}

// The cookie the login flow mints does pass, or nobody can create a key.
func TestMarkedSessionCookieIsABrowserSession(t *testing.T) {
	session := Claims{Email: "alice@example.com", Scopes: []string{"user"}, Typ: TokenTypeSession}

	got := credentialFor(session, CredSourceCookie)

	if !got.FromBrowser() || got.Kind != CredKindSession {
		t.Fatalf("kind = %q, FromBrowser = %v; the minted session cookie must qualify", got.Kind, got.FromBrowser())
	}
}

// Adding the marker must not stop a session standing in for a browser on the
// routes that already required one (MCP authorize, share admin).
func TestMarkedSessionStillPassesIsSessionCredential(t *testing.T) {
	for _, c := range []Claims{
		{Email: "a@x.com", Scopes: []string{"user"}, Typ: TokenTypeSession},
		{Email: "a@x.com", Scopes: []string{"user"}}, // minted before the marker
	} {
		if !c.IsSessionCredential() {
			t.Errorf("claims %+v stopped counting as a session credential", c)
		}
	}
}
