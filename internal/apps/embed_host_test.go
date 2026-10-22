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

// A host-minted token is pinned to the version it reports and drives the proxy
// as the viewer the host named — and only for apps that viewer can read.
func TestMintEmbedHostToken_PinsVersionAndRunsAsViewer(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	secretSlug := uniqueSlug("bobs")
	seedText(t, st, secretSlug, "bob's eyes only", "bob@example.com", []string{})
	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "read_artifact"}})
	_, slug, err := appsSvc.EmbedUserApp(context.Background(), appID, "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}

	tok, gotID, ver, err := appsSvc.MintEmbedHostToken(context.Background(), slug, "bob@example.com")
	if err != nil || gotID != appID || ver < 1 {
		t.Fatalf("MintEmbedHostToken = (%q, v%d, %v), want app %q", gotID, ver, err, appID)
	}
	if rr := proxyPost(t, r, tok, appID, "arti", "read_artifact", map[string]any{"ident": secretSlug}); rr.Code != http.StatusOK ||
		!strings.Contains(rr.Body.String(), "bob's eyes only") {
		t.Fatalf("host token must act as bob: %d %s", rr.Code, rr.Body.String())
	}

	// Another writer publishes v2: the host must not run it unseen as every viewer.
	if _, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType: pgstore.TypeApp, NamedSlug: &slug, Title: "demo app", ContentType: "application/zip",
		Content: buildAppZip(t, `{"name":"demo","entry":"index.html","tools":[{"server":"notion","tool":"search"}]}`),
		Creator: "mallory@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := appsSvc.MintEmbedHostToken(context.Background(), slug, "bob@example.com"); err == nil {
		t.Fatal("a version the owner did not publish must not host-mint")
	}

	if _, _, _, err := appsSvc.MintEmbedHostToken(context.Background(), secretSlug, "bob@example.com"); err == nil {
		t.Fatal("a non-APP slug must not mint")
	}
	if _, _, _, err := appsSvc.MintEmbedHostToken(context.Background(), uniqueSlug("absent"), "bob@example.com"); err == nil {
		t.Fatal("an absent slug must not mint")
	}
}
