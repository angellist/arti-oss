//go:build integration

package apps_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// The full user-mode arc: a token minted by the popup handshake is a real,
// per-user app token — it drives the proxy AS THE VIEWER (here reading a
// creator-only artifact only that viewer can see).
func TestMintEmbedUserToken_RunsAsViewer(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	secretSlug := uniqueSlug("bobs")
	seedText(t, st, secretSlug, "bob's eyes only", "bob@example.com", []string{}) // creator-only
	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "read_artifact"}})

	title, slug, err := appsSvc.EmbedUserApp(context.Background(), appID, "bob@example.com")
	if err != nil || title == "" || !strings.HasPrefix(slug, "data-app-") {
		t.Fatalf("EmbedUserApp = (%q,%q,%v)", title, slug, err)
	}
	tok, err := appsSvc.MintEmbedUserToken(context.Background(), appID, "bob@example.com")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	rr := proxyPost(t, r, tok, appID, "arti", "read_artifact", map[string]any{"ident": secretSlug})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "bob's eyes only") {
		t.Fatalf("minted token must act as bob: %d %s", rr.Code, rr.Body.String())
	}

	// Same call as alice: her handshake token must NOT see bob's artifact —
	// identity comes from the token, and access is enforced per call.
	tokA, err := appsSvc.MintEmbedUserToken(context.Background(), appID, "alice@example.com")
	if err != nil {
		t.Fatalf("mint alice: %v", err)
	}
	if rr := proxyPost(t, r, tokA, appID, "arti", "read_artifact", map[string]any{"ident": secretSlug}); rr.Code != http.StatusNotFound {
		t.Fatalf("alice's token reading bob's artifact: %d, want 404", rr.Code)
	}
}

// The mint authorizes against the viewer's read access and the artifact type,
// and accepts only version-pinned UUIDs (a slug could drift to a different row
// than the served page and 403 every call).
func TestMintEmbedUserToken_Gates(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)

	appID := newAppArtifact(t, st, nil)
	ctx := context.Background()

	if _, err := appsSvc.MintEmbedUserToken(ctx, "not-a-uuid", "alice@example.com"); err == nil {
		t.Fatal("slug/garbage ident must be rejected — mint is by UUID only")
	}

	// A restricted APP (creator-only) is mintable by its creator, not others.
	restrictedSlug := uniqueSlug("private-app")
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeApp, NamedSlug: &restrictedSlug, Title: "private",
		ContentType: "application/zip", Content: buildAppZip(t, `{"name":"p","entry":"index.html","tools":[]}`),
		Creator: "alice@example.com", AllowedAccess: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	restrictedID := pgstore.UUIDFromPG(row.ArtifactID).String()
	if _, err := appsSvc.MintEmbedUserToken(ctx, restrictedID, "alice@example.com"); err != nil {
		t.Fatalf("creator must be able to mint: %v", err)
	}
	if _, err := appsSvc.MintEmbedUserToken(ctx, restrictedID, "mallory@example.com"); err == nil {
		t.Fatal("non-reader must not mint a token for a restricted app")
	}

	// A non-APP artifact never mints (the token would scope tool calls to it).
	textSlug := uniqueSlug("plain")
	seedText(t, st, textSlug, "text", "alice@example.com", nil)
	textRow, err := st.GetBySlug(ctx, textSlug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appsSvc.MintEmbedUserToken(ctx, pgstore.UUIDFromPG(textRow.ArtifactID).String(), "alice@example.com"); err == nil {
		t.Fatal("non-APP artifact must not mint")
	}
	_ = appID
}

// The user-mode files token is a deliberately narrow credential: it round-trips
// through its own verifier only — it is not an app token, not a session
// credential, and the service-mode files verifier rejects it (and vice versa).
func TestEmbedFilesToken_Isolation(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "read_artifact"}})

	ftok, err := appsSvc.SignEmbedFilesToken(appID)
	if err != nil {
		t.Fatal(err)
	}
	if aid, err := appsSvc.VerifyEmbedFilesToken(ftok); err != nil || aid != appID {
		t.Fatalf("files-token round-trip: (%q,%v)", aid, err)
	}
	if _, _, err := appsSvc.VerifyEmbedToken(ftok); err == nil {
		t.Fatal("service-mode files verifier must reject an email-less files token")
	}
	if rr := proxyPost(t, r, ftok, appID, "arti", "read_artifact", map[string]any{"ident": "x"}); rr.Code != http.StatusUnauthorized {
		t.Fatalf("files token driving the proxy: %d, want 401", rr.Code)
	}

	// And the other direction: a real app token is not a files token.
	atok := mustToken(t, appsSvc, "alice@example.com", appID)
	if _, err := appsSvc.VerifyEmbedFilesToken(atok); err == nil {
		t.Fatal("files verifier must reject an app token")
	}
}
