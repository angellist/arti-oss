//go:build integration

package artifacts_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ─── fixtures ────────────────────────────────────────────────────────
// This package has no fixture DSL; the convention is inline construction
// (see write_access_test.go). These helpers exist only for the share tests.

func newShareService(t *testing.T) *artifacts.Service {
	svc, _ := newShareServiceWithPool(t)
	return svc
}

// newShareServiceWithPool also hands back the pool, for the few tests that
// have to reach past the service to age a row.
func newShareServiceWithPool(t *testing.T) (*artifacts.Service, *pgxpool.Pool) {
	t.Helper()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "https://arti.example.com", nil, nil)
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: true, MaxTTL: 720 * time.Hour})
	return svc, pool
}

// expireShareLink ages a link past its expiry. There is no service call for
// this — waiting out a real TTL is not a test.
func expireShareLink(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE share_links SET expires_at = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
		t.Fatalf("expire %s: %v", id, err)
	}
}

type artOpt func(*artifacts.CreateRequest)

func withWrite(emails ...string) artOpt {
	return func(r *artifacts.CreateRequest) { r.AllowedWrite = &emails }
}
func withAccess(emails ...string) artOpt {
	return func(r *artifacts.CreateRequest) { r.AllowedAccess = &emails }
}

// withZipType makes the artifact a package-like type (PACKAGE or APP), whose
// content must be a real zip — the create path validates the archive, so a
// plain string body is rejected.
func withZipType(t *testing.T, kind string, files map[string]string) artOpt {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return func(r *artifacts.CreateRequest) {
		r.ArtifactType = kind
		r.ContentType = "application/zip"
		r.Content = ""
		r.ContentBase64 = encoded
		entry := "index.html"
		r.EntryPoint = &entry
	}
}

// createArtifact makes one version. Pass slugPrefix "" for a slugless
// artifact. Defaults to a world-readable TEXT document so the tests that care
// about ACLs can narrow it explicitly and the rest need not think about it.
func createArtifact(t *testing.T, svc *artifacts.Service, creator, slugPrefix string, opts ...artOpt) artifacts.ArtifactInfo {
	t.Helper()
	world := []string{"*"}
	req := artifacts.CreateRequest{
		Title:         "share-fixture",
		ContentType:   "text/markdown",
		Content:       "# body\n",
		AllowedAccess: &world,
	}
	if slugPrefix != "" {
		slug := uniqueSlug(slugPrefix)
		req.NamedSlug = &slug
	}
	for _, o := range opts {
		o(&req)
	}
	info, err := svc.Create(context.Background(), req, creator)
	if err != nil {
		t.Fatalf("createArtifact(%s): %v", slugPrefix, err)
	}
	return info
}

// publishVersion pushes another version onto an existing slug as `who`,
// optionally changing the artifact type — which is what the APP-at-serve case
// needs, and which the create path permits because it never compares the
// requested type to the previous version.
func publishVersion(t *testing.T, svc *artifacts.Service, slug, who string, opts ...artOpt) artifacts.ArtifactInfo {
	t.Helper()
	req := artifacts.CreateRequest{
		NamedSlug:   &slug,
		Title:       "share-fixture-next",
		ContentType: "text/markdown",
		Content:     "# next\n",
	}
	for _, o := range opts {
		o(&req)
	}
	info, err := svc.Create(context.Background(), req, who)
	if err != nil {
		t.Fatalf("publishVersion(%s as %s): %v", slug, who, err)
	}
	return info
}

func mint(t *testing.T, svc *artifacts.Service, id, caller, scope string) artifacts.MintShareResult {
	t.Helper()
	res, err := svc.MintShare(context.Background(), id, caller,
		artifacts.MintShareRequest{Scope: scope, TTL: "1h"})
	if err != nil {
		t.Fatalf("mint(%s, %s): %v", id, scope, err)
	}
	return res
}

func tokenOf(res artifacts.MintShareResult) string {
	return res.URL[strings.LastIndex(res.URL, "/")+1:]
}

// ─── tests ───────────────────────────────────────────────────────────

// The plaintext is shown once and never stored. What the row keeps must be the
// digest and a display prefix, nothing from which the token is derivable.
func TestNewShareToken(t *testing.T) {
	tok, hash, prefix, err := artifacts.NewShareToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 40 {
		t.Fatalf("token %q is too short to be 32 random bytes", tok)
	}
	if !strings.HasPrefix(tok, prefix) || len(prefix) != 8 {
		t.Fatalf("prefix %q is not the token's first 8 chars", prefix)
	}
	if want := artifacts.HashShareToken(tok); string(hash) != string(want) {
		t.Fatal("returned hash is not HashShareToken(token)")
	}
	if strings.Contains(string(hash), tok) {
		t.Fatal("hash contains the plaintext")
	}
	tok2, _, _, _ := artifacts.NewShareToken()
	if tok == tok2 {
		t.Fatal("two mints produced the same token")
	}
}

// Minting publishes a document to the internet, which is a larger act than any
// ACL widening — so it takes the same authority an ACL change takes. A
// delegated writer can push versions but must not be able to mint.
func TestMintShare_AuthorityIsDocOwner(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner, writer, reader = "owner@example.com", "writer@example.com", "reader@example.com"
	art := createArtifact(t, svc, owner, "share-auth",
		withAccess(owner, writer, reader), withWrite(owner, writer))

	if _, err := svc.MintShare(ctx, art.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); err != nil {
		t.Fatalf("owner mint: %v", err)
	}

	// A writer and a reader can SEE the document, so they clear checkAccess
	// and are refused by isDocOwner — forbidden, not not-found.
	for _, who := range []string{writer, reader} {
		_, err := svc.MintShare(ctx, art.ArtifactID, who,
			artifacts.MintShareRequest{Scope: "version", TTL: "1h"})
		if !artifacts.IsForbidden(err) {
			t.Errorf("%s mint: want forbidden, got %v", who, err)
		}
	}

	// A stranger cannot see the document at all, so checkAccess refuses FIRST
	// with ErrNotFound. That ordering is deliberate: a 403 here would confirm
	// the document exists to someone with no access to it.
	if _, err := svc.MintShare(ctx, art.ArtifactID, "stranger@example.com",
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("stranger mint: want ErrNotFound, got %v", err)
	}
}

func TestMintShare_Refusals(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner = "owner@example.com"

	app := createArtifact(t, svc, owner, "share-app",
		withZipType(t, pgstore.TypeApp, map[string]string{"index.html": "<html><body>app</body></html>"}))
	if _, err := svc.MintShare(ctx, app.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); err == nil {
		t.Error("APP: want refusal")
	}

	arch := createArtifact(t, svc, owner, "share-archived")
	if _, err := svc.ArchiveByID(ctx, mustUUID(t, arch.ArtifactID)); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := svc.MintShare(ctx, arch.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); err == nil {
		t.Error("archived: want refusal")
	}

	slugless := createArtifact(t, svc, owner, "")
	if _, err := svc.MintShare(ctx, slugless.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "slug", TTL: "1h"}); err == nil {
		t.Error("slug scope on a slugless artifact: want refusal")
	}

	ok := createArtifact(t, svc, owner, "share-ttl")
	if _, err := svc.MintShare(ctx, ok.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "version", TTL: "9999h"}); err == nil {
		t.Error("TTL outside the allowed set: want refusal")
	}
	if _, err := svc.MintShare(ctx, ok.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "sideways", TTL: "1h"}); err == nil {
		t.Error("unknown scope: want refusal")
	}
}

// The TTL cap is configurable and must actually bind.
func TestMintShare_TTLCap(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: true, MaxTTL: 8 * time.Hour})
	art := createArtifact(t, svc, "owner@example.com", "share-cap")

	if _, err := svc.MintShare(ctx, art.ArtifactID, "owner@example.com",
		artifacts.MintShareRequest{Scope: "version", TTL: "7d"}); err == nil {
		t.Fatal("7d against an 8h cap: want refusal")
	}
	if _, err := svc.MintShare(ctx, art.ArtifactID, "owner@example.com",
		artifacts.MintShareRequest{Scope: "version", TTL: "8h"}); err != nil {
		t.Fatalf("8h against an 8h cap: %v", err)
	}
}

// With the feature off nothing mints, and the refusal is a 404 rather than a
// 403 — the endpoint should look absent, not forbidden.
func TestMintShare_DisabledIsNotFound(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	art := createArtifact(t, svc, "owner@example.com", "share-off")
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: false})

	if _, err := svc.MintShare(ctx, art.ArtifactID, "owner@example.com",
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("disabled mint: want ErrNotFound, got %v", err)
	}
}

// The list endpoint must never hand back anything that could reconstruct a
// URL. The prefix identifies a row; it does not open one.
func TestListShares_NeverReturnsAToken(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner = "owner@example.com"
	art := createArtifact(t, svc, owner, "share-list")
	res := mint(t, svc, art.ArtifactID, owner, "version")

	rows, err := svc.ListShares(ctx, art.ArtifactID, owner)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %d rows, %v", len(rows), err)
	}
	blob, _ := json.Marshal(rows)
	if strings.Contains(string(blob), tokenOf(res)) {
		t.Fatalf("serialized list leaks the token: %s", blob)
	}
	if rows[0].TokenPrefix == "" || rows[0].Scope != "version" {
		t.Fatalf("row = %+v", rows[0])
	}
}

// Revoke authority follows the DOCUMENT, not who minted the link.
func TestRevokeShare_AuthorityFollowsTheDocument(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner, other = "owner@example.com", "other@example.com"
	art := createArtifact(t, svc, owner, "share-revoke", withAccess(owner, other))
	res := mint(t, svc, art.ArtifactID, owner, "version")

	if err := svc.RevokeShare(ctx, res.ID, other); !artifacts.IsForbidden(err) {
		t.Fatalf("non-owner revoke: want forbidden, got %v", err)
	}
	if err := svc.RevokeShare(ctx, res.ID, owner); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	// A revoked link is still listable — an owner asking "did this leak" needs
	// to see recent history, not an empty table.
	rows, err := svc.ListShares(ctx, art.ArtifactID, owner)
	if err != nil || len(rows) != 1 || rows[0].RevokedAt == nil {
		t.Fatalf("after revoke: rows=%+v err=%v", rows, err)
	}
}

// ─── resolution ──────────────────────────────────────────────────────

// All six refusal branches must be indistinguishable to the caller. Asserting
// they return the SAME error is what lets the HTTP layer emit one identical
// 404 for every case — a holder of a revoked link must not be able to tell it
// from a random string.
func TestResolveShare_AllRefusalsAreIdentical(t *testing.T) {
	svc, pool := newShareServiceWithPool(t)
	ctx := context.Background()
	const owner, writer = "owner@example.com", "writer@example.com"

	cases := map[string]func() string{
		"unknown": func() string { return "not-a-real-token" },

		"revoked": func() string {
			a := createArtifact(t, svc, owner, "res-revoked")
			r := mint(t, svc, a.ArtifactID, owner, "version")
			if err := svc.RevokeShare(ctx, r.ID, owner); err != nil {
				t.Fatal(err)
			}
			return tokenOf(r)
		},

		"expired": func() string {
			a := createArtifact(t, svc, owner, "res-expired")
			r := mint(t, svc, a.ArtifactID, owner, "version")
			expireShareLink(t, pool, r.ID)
			return tokenOf(r)
		},

		"archived": func() string {
			a := createArtifact(t, svc, owner, "res-archived")
			r := mint(t, svc, a.ArtifactID, owner, "version")
			if _, err := svc.ArchiveByID(ctx, mustUUID(t, a.ArtifactID)); err != nil {
				t.Fatal(err)
			}
			return tokenOf(r)
		},

		// The adversary this branch stops is not the owner. Publishing a
		// version needs write access, not ownership, so a delegated writer
		// can turn a live tracking link into anonymous APP execution.
		"app-at-serve": func() string {
			slug := uniqueSlug("res-app")
			world := []string{"*"}
			a, err := svc.Create(ctx, artifacts.CreateRequest{
				NamedSlug: &slug, Title: "res-app", ContentType: "text/markdown",
				Content: "# v1\n", AllowedAccess: &world,
			}, owner)
			if err != nil {
				t.Fatal(err)
			}
			r := mint(t, svc, a.ArtifactID, owner, "slug")
			publishVersion(t, svc, slug, writer,
				withZipType(t, pgstore.TypeApp, map[string]string{"index.html": "<html>app</html>"}))
			return tokenOf(r)
		},
	}

	for name, mk := range cases {
		_, _, err := svc.ResolveShare(ctx, mk())
		if !errors.Is(err, pgstore.ErrNotFound) {
			t.Errorf("%s: got %v, want ErrNotFound — the refusal branches must be indistinguishable", name, err)
		}
	}
}

// Archiving the LATEST version must kill a tracking link, not silently serve
// the previous one. GetLatestArtifactBySlug filters deleted_at and walks back
// (db/queries/artifacts.sql:20-24), which is exactly the behaviour the share
// path must not inherit: an owner archiving a document to pull it back would
// otherwise get a link that keeps serving older content.
func TestResolveShare_TrackingLinkDiesWhenLatestIsArchived(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner = "owner@example.com"

	slug := uniqueSlug("res-walkback")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown",
		Content: "# v1\n", AllowedAccess: &world,
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	v2 := publishVersion(t, svc, slug, owner)
	r := mint(t, svc, v2.ArtifactID, owner, "slug")

	if _, err := svc.ArchiveByID(ctx, mustUUID(t, v2.ArtifactID)); err != nil {
		t.Fatal(err)
	}

	row, _, err := svc.ResolveShare(ctx, tokenOf(r))
	if !errors.Is(err, pgstore.ErrNotFound) {
		served := int32(-1)
		if row.Version != nil {
			served = *row.Version
		}
		t.Fatalf("archiving the latest version left the tracking link alive, serving v%d (err=%v); it walked back to v1 %s",
			served, err, v1.ArtifactID)
	}
}

// A pinned link is frozen at its version; a tracking link follows the slug.
func TestResolveShare_PinnedVersusTracking(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner = "owner@example.com"

	slug := uniqueSlug("res-scope")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown",
		Content: "# v1\n", AllowedAccess: &world,
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	pinned := mint(t, svc, v1.ArtifactID, owner, "version")
	tracking := mint(t, svc, v1.ArtifactID, owner, "slug")

	publishVersion(t, svc, slug, owner)

	pinRow, _, err := svc.ResolveShare(ctx, tokenOf(pinned))
	if err != nil || pinRow.Version == nil || *pinRow.Version != 1 {
		t.Fatalf("pinned resolved to %+v (%v), want v1", pinRow.Version, err)
	}
	trkRow, _, err := svc.ResolveShare(ctx, tokenOf(tracking))
	if err != nil || trkRow.Version == nil || *trkRow.Version != 2 {
		t.Fatalf("tracking resolved to %+v (%v), want v2", trkRow.Version, err)
	}
}

// can_share is what the viewer gates the Share control on, so it must agree
// with what MintShare will actually allow. The client cannot compute this
// itself: `creator` is reassigned to whoever pushed the latest version, so a
// delegated writer re-deriving from it would be shown a control the server
// then refuses.
func TestCanShareFlag_MatchesMintAuthority(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner, writer = "owner@example.com", "writer@example.com"

	slug := uniqueSlug("can-share")
	world := []string{"*"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown",
		Content: "# v1\n", AllowedAccess: &world,
	}, owner); err != nil {
		t.Fatal(err)
	}
	// The writer pushes v2 and so becomes the LATEST version's creator — the
	// exact state a client-side check would misread as ownership.
	v2 := publishVersion(t, svc, slug, writer)

	for _, tc := range []struct {
		who  string
		want bool
	}{
		{owner, true},
		{writer, false},
	} {
		info, err := svc.Get(ctx, mustUUID(t, v2.ArtifactID), tc.who)
		if err != nil {
			t.Fatalf("%s: %v", tc.who, err)
		}
		if info.CanShare == nil || *info.CanShare != tc.want {
			t.Errorf("%s: can_share = %v, want %v", tc.who, info.CanShare, tc.want)
		}
		_, mintErr := svc.MintShare(ctx, v2.ArtifactID, tc.who,
			artifacts.MintShareRequest{Scope: "version", TTL: "1h"})
		if (mintErr == nil) != tc.want {
			t.Errorf("%s: can_share says %v but MintShare returned %v", tc.who, tc.want, mintErr)
		}
	}
}

// The flag folds in the feature switch and artifact eligibility, so the viewer
// never has to know about either.
func TestCanShareFlag_FalseWhenDisabledOrIneligible(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner = "owner@example.com"

	app := createArtifact(t, svc, owner, "can-share-app",
		withZipType(t, pgstore.TypeApp, map[string]string{"index.html": "<html>a</html>"}))
	info, err := svc.Get(ctx, mustUUID(t, app.ArtifactID), owner)
	if err != nil {
		t.Fatal(err)
	}
	if info.CanShare == nil || *info.CanShare {
		t.Error("APP: can_share should be false")
	}

	doc := createArtifact(t, svc, owner, "can-share-off")
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: false})
	info, err = svc.Get(ctx, mustUUID(t, doc.ArtifactID), owner)
	if err != nil {
		t.Fatal(err)
	}
	if info.CanShare == nil || *info.CanShare {
		t.Error("feature off: can_share should be false")
	}
}

// Archiving does not revoke anything, so an owner must still be able to kill a
// link on an archived document — otherwise unarchiving brings a still-live
// link back with nobody having been able to stop it.
//
// The trap this pins: checkAccess hides an archived row from everyone but that
// row's own Creator, and versioning reassigns Creator to whoever pushed the
// latest version. Running it before the owner check locked the real owner out.
func TestRevokeShare_OwnerCanRevokeOnAnArchivedDocWithAWritersTip(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner, writer = "owner@example.com", "writer@example.com"

	slug := uniqueSlug("revoke-archived")
	world := []string{"*"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown",
		Content: "# v1\n", AllowedAccess: &world,
	}, owner); err != nil {
		t.Fatal(err)
	}
	// The delegated writer publishes v2, becoming the tip's Creator.
	v2 := publishVersion(t, svc, slug, writer)
	res := mint(t, svc, v2.ArtifactID, owner, "slug")

	if _, err := svc.ArchiveByID(ctx, mustUUID(t, v2.ArtifactID)); err != nil {
		t.Fatal(err)
	}

	if err := svc.RevokeShare(ctx, res.ID, owner); err != nil {
		t.Fatalf("owner revoking on an archived doc whose tip a writer published: %v", err)
	}
	if _, err := svc.ShareOpens(ctx, res.ID, owner); err != nil {
		t.Fatalf("owner reading opens on an archived doc: %v", err)
	}
}

// The reordering must not widen anything: a reader still gets 403, and someone
// who cannot see the document at all still gets 404 rather than a 403 that
// would confirm it exists.
func TestShareAuthority_StillHidesExistenceFromNonReaders(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner, reader = "owner@example.com", "reader@example.com"
	art := createArtifact(t, svc, owner, "authority-split", withAccess(owner, reader))
	res := mint(t, svc, art.ArtifactID, owner, "version")

	if err := svc.RevokeShare(ctx, res.ID, reader); !artifacts.IsForbidden(err) {
		t.Errorf("reader revoke: want forbidden, got %v", err)
	}
	if err := svc.RevokeShare(ctx, res.ID, "stranger@example.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("stranger revoke: want ErrNotFound, got %v", err)
	}
}

// An all-archived slug is free for anyone to reuse — pgstore/store.go:355 says
// so deliberately — and SlugCreator ignores deleted_at (store.go:1646). A
// slug-scoped share link stores only the bare slug string. Put those together
// and a link minted by A, whose lineage A then archived, resolves whatever B
// later publishes to that slug, serving B's private document to the internet.
//
// The same root cause lets A keep minting: SlugCreator still names A, so
// isDocOwner authorizes A over B's document. Both are closed here; see the
// third assertion for what is deliberately left failing closed.
func TestResolveShare_TrackingLinkDoesNotSurviveSlugReuse(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const attacker, victim = "attacker@example.com", "victim@example.com"

	slug := uniqueSlug("slug-reuse")
	world := []string{"*"}
	a1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "attacker v1", ContentType: "text/markdown",
		Content: "# attacker content\n", AllowedAccess: &world,
	}, attacker)
	if err != nil {
		t.Fatal(err)
	}
	link := mint(t, svc, a1.ArtifactID, attacker, "slug")

	// The attacker archives their whole lineage, freeing the slug.
	if _, err := svc.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatal(err)
	}

	// The victim publishes a PRIVATE document to the now-free slug.
	onlyVictim := []string{victim}
	vRow, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "victim private", ContentType: "text/markdown",
		Content: "# CONFIDENTIAL victim content\n", AllowedAccess: &onlyVictim,
	}, victim)
	if err != nil {
		t.Fatal(err)
	}

	// (1) The stale tracking link must not serve the victim's document.
	row, _, rerr := svc.ResolveShare(ctx, tokenOf(link))
	if rerr == nil {
		t.Errorf("stale tracking link resolved the victim's document %q (%s) — anonymous ACL bypass",
			row.Title, vRow.ArtifactID)
	} else if !errors.Is(rerr, pgstore.ErrNotFound) {
		t.Errorf("stale tracking link: got %v, want ErrNotFound", rerr)
	}

	// (2) The former owner must not be able to mint fresh links on it either.
	if _, merr := svc.MintShare(ctx, vRow.ArtifactID, attacker,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); merr == nil {
		t.Error("the former slug owner minted an external link on the victim's private document")
	}

	// (3) KNOWN LIMITATION, asserted so it cannot change unnoticed: the victim
	// cannot mint either. isDocOwner still resolves the slug's owner to the
	// attacker, and share minting refuses rather than guessing which of the
	// two is the real owner — publishing to the internet is not a decision to
	// take on an ambiguous answer. Recovery is an admin, or the ownership fix
	// in bl-arti-isdocowner-slug-reuse. This is a usability cost that fails
	// closed, not a security hole.
	if _, merr := svc.MintShare(ctx, vRow.ArtifactID, victim,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); merr == nil {
		t.Error("minting on a reclaimed slug should be refused until ownership is unambiguous")
	}
}

// Archiving your own early versions while a delegated writer's later version
// stays live must not hand that writer the ability to publish the document
// externally, and must not silently transfer authority away from the owner.
// Ownership is ambiguous there, so minting is refused for both and an admin is
// the recovery path.
func TestMintShare_RefusedWhenTheLineageIsAmbiguous(t *testing.T) {
	svc, ctx := newShareService(t), context.Background()
	const owner, writer = "owner@example.com", "writer@example.com"

	slug := uniqueSlug("ambiguous")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown",
		Content: "# v1\n", AllowedAccess: &world,
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	v2 := publishVersion(t, svc, slug, writer)

	// While the whole lineage is intact, the owner can mint.
	if _, err := svc.MintShare(ctx, v2.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); err != nil {
		t.Fatalf("owner mint on an intact lineage: %v", err)
	}

	// The owner archives their own v1. The earliest LIVE version is now the
	// writer's, so who owns the slug is no longer answerable.
	if _, err := svc.ArchiveByID(ctx, mustUUID(t, v1.ArtifactID)); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.MintShare(ctx, v2.ArtifactID, writer,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); err == nil {
		t.Error("a delegated writer gained the ability to publish the document externally")
	}
	if _, err := svc.MintShare(ctx, v2.ArtifactID, owner,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); err == nil {
		t.Error("minting should be refused while ownership is ambiguous, even for the slug owner")
	}
	// can_share must say the same thing the server will do.
	info, err := svc.Get(ctx, mustUUID(t, v2.ArtifactID), owner)
	if err != nil {
		t.Fatal(err)
	}
	if info.CanShare == nil || *info.CanShare {
		t.Error("can_share should be false while minting is refused")
	}
}

// A slug-scoped link with no anchor predates migration 0025. It is exactly the
// row that can follow a reclaimed slug, so resolution refuses it rather than
// skipping the check.
func TestResolveShare_UnanchoredSlugLinkIsRefused(t *testing.T) {
	svc, pool := newShareServiceWithPool(t)
	ctx := context.Background()
	const owner = "owner@example.com"

	slug := uniqueSlug("unanchored")
	world := []string{"*"}
	a, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown",
		Content: "# v1\n", AllowedAccess: &world,
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	r := mint(t, svc, a.ArtifactID, owner, "slug")
	if _, _, err := svc.ResolveShare(ctx, tokenOf(r)); err != nil {
		t.Fatalf("anchored link should resolve: %v", err)
	}

	// Simulate a pre-0025 row.
	if _, err := pool.Exec(ctx, `UPDATE share_links SET anchor_owner = '' WHERE id = $1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResolveShare(ctx, tokenOf(r)); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("unanchored slug link: got %v, want ErrNotFound", err)
	}
}
