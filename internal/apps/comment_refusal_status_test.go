//go:build integration

package apps_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/apps"
	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/comments"
	"github.com/angellist/arti-oss/internal/mcp"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// An in-process comment refusal is a refusal, not an arti outage. The apps
// proxy maps unrecognized errors to 502, which turned "commenting is off for
// this document" into a gateway failure the page could not act on.
func TestProxyCommentRefusalKeepsItsStatus(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	cs := comments.NewService(pool, st, auth.NewJWTSigner([]byte("test-secret")))
	appsSvc := apps.New(st, signer, map[string]apps.ServerConfig{}, nil, nil, nil)
	appsSvc.SetArtiTools(mcp.NewServer(svc, cs))
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	slug := uniqueSlug("app-comment-off")
	seedText(t, st, slug, "body", "alice@example.com", []string{"*"})
	if _, err := st.SetCommentsEnabledBySlug(ctx, slug, false); err != nil {
		t.Fatalf("turn commenting off: %v", err)
	}

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "add_comment"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti", "add_comment", map[string]any{
		"ident": slug, "body": "hello",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
	}
}
