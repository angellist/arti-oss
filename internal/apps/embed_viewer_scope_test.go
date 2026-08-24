package apps

import (
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
)

// The viewer-mode document token and the user-mode APP token carry the same app
// scope shape, so the marker scope is the only thing distinguishing them. The
// separation is deliberately ASYMMETRIC, and these tests pin both halves:
//
//   - An APP token must NOT authorize a viewer document render. That direction is
//     enforced, because a document render has no second gate behind it.
//   - A viewer token DOES still authorize tool calls on the same app through the
//     apps proxy. That direction is intentionally open so an APP embedded in
//     viewer mode keeps working; the token grants exactly what the same viewer
//     would get from a user-mode surface.
//
// Bugbot flagged the open direction on PR 242 as a gap. It is not — it is the
// requirement. The test below exists so a future reader does not "fix" it.
func TestEmbedViewerToken_ScopeSeparation(t *testing.T) {
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	s := &Service{signer: signer}
	const aid = "5cc6a0b2-64c8-4c5a-9d4e-000000000001"

	viewerTok, err := signer.Sign(auth.Claims{
		Email: "reader@x.com", Scopes: []string{appScopePrefix + aid, embedViewerScope}, TTL: embedUserTokenTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	userTok, err := signer.Sign(auth.Claims{
		Email: "reader@x.com", Scopes: []string{appScopePrefix + aid, embedUserScope}, TTL: embedUserTokenTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	plainAppTok, err := signer.Sign(auth.Claims{
		Email: "reader@x.com", Scopes: []string{appScopePrefix + aid}, TTL: appTokenTTL,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("viewer token verifies as a viewer token", func(t *testing.T) {
		email, got, err := s.VerifyEmbedViewerToken(viewerTok)
		if err != nil {
			t.Fatalf("want the viewer token to verify, got %v", err)
		}
		if email != "reader@x.com" || got != aid {
			t.Fatalf("want reader@x.com/%s, got %s/%s", aid, email, got)
		}
	})

	t.Run("embed-user APP token is rejected as a viewer token", func(t *testing.T) {
		if _, _, err := s.VerifyEmbedViewerToken(userTok); err == nil {
			t.Fatal("an embed-user APP token must not authorize a viewer document render")
		} else if !strings.Contains(err.Error(), "embed-viewer scope") {
			t.Fatalf("want a scope error, got %v", err)
		}
	})

	t.Run("plain app token is rejected as a viewer token", func(t *testing.T) {
		if _, _, err := s.VerifyEmbedViewerToken(plainAppTok); err == nil {
			t.Fatal("a plain /app token must not authorize a viewer document render")
		}
	})

	// Review A F1 (blocker): the JWT signer is shared with session auth, which
	// accepts any authentic token for an allowed email UNLESS its scopes are
	// embed- or app-scoped (internal/auth/middleware.go). A viewer token carries
	// app:<uuid>, so IsAppScoped() rejects it there. This asserts the property
	// directly, because the alternative token shape the review considered — a bare
	// embed-viewer:<uuid> prefix with no app: scope — would have sailed through
	// middleware as a FULL API session credential riding in a URL query param.
	t.Run("viewer token is not a session credential", func(t *testing.T) {
		claims, err := signer.Verify(viewerTok)
		if err != nil {
			t.Fatal(err)
		}
		if !claims.IsAppScoped() {
			t.Fatal("viewer token is not app-scoped, so session middleware would accept it as a full API credential")
		}
	})

	t.Run("garbage is rejected", func(t *testing.T) {
		if _, _, err := s.VerifyEmbedViewerToken("not.a.token"); err == nil {
			t.Fatal("want an error on a malformed token")
		}
	})

	// The open direction, asserted so it cannot regress silently. A viewer token
	// satisfies the looser email+app-scope check that the sibling-files route and
	// the apps proxy both use, which is what keeps an APP embedded in viewer mode
	// able to call its tools. If this ever starts failing, APP embeds broke.
	t.Run("viewer token still satisfies the looser app-scope verifier", func(t *testing.T) {
		email, got, err := s.VerifyEmbedToken(viewerTok)
		if err != nil || email != "reader@x.com" || got != aid {
			t.Fatalf("APP embeds in viewer mode just broke: %s/%s err=%v", email, got, err)
		}
	})
}
