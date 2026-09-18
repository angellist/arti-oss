//go:build integration

package artifacts_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func mapSvc(t *testing.T) (*artifacts.Service, context.Context) {
	t.Helper()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	svc.SetMapEnabled(true)
	return svc, context.Background()
}

func jsonVal(s string) json.RawMessage { return json.RawMessage(s) }

// Appendix C 21 — creation goes through the map routes, which validate keys,
// enforce the limits and require a slug. The generic create path would do
// none of that, so it refuses outright.
func TestMap_CreateRejectedOnGenericEndpoint(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapcreate")
	_, err := svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: "MAP", Title: "sneaky", ContentType: "application/x-ndjson",
		Content: "{}", NamedSlug: &slug,
	}, "alice@example.com")
	if err == nil {
		t.Fatal("POST /api/artifacts must refuse artifact_type=MAP")
	}
	if !strings.Contains(err.Error(), "/map") {
		t.Fatalf("the refusal should point at the route that does work; got %v", err)
	}
}

// The map put path IS the create path: a first write to an unknown slug mints
// v1, the same way append auto-creates. Without a title it refuses, because
// there is nothing to name the artifact.
func TestMap_PutCreatesTheMap(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapnew")

	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err == nil {
		t.Fatal("creating a map without a title should be refused")
	}

	out, conflict, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title:   "my map",
		Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com")
	if err != nil || conflict {
		t.Fatalf("create+write: err=%v conflict=%v", err, conflict)
	}
	if len(out.Results) != 1 || !out.Results[0].Written {
		t.Fatalf("entry should have been written: %+v", out.Results)
	}
	if out.Stats.Keys != 1 {
		t.Fatalf("stats should report 1 key, got %d", out.Stats.Keys)
	}
}

// Appendix C 22 — a MAP republished as TEXT would leave a head no route can
// reach and a version chain whose types disagree, so the retype is refused in
// BOTH directions and allow_type_change does not override it. That last part
// is the point: every other type change is opt-in-able.
func TestMap_RetypeRefusedEvenWithAllowTypeChange(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapretype")
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title: "m", Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: "TEXT", Title: "now text", ContentType: "text/markdown",
		Content: "hello", NamedSlug: &slug, AllowTypeChange: true,
	}, "alice@example.com")
	if err == nil {
		t.Fatal("a MAP slug must not be republishable as TEXT, even with allow_type_change")
	}
	if !strings.Contains(err.Error(), "strand") {
		t.Fatalf("the refusal should say why; got %v", err)
	}

	// The entries are still there, which is the thing the refusal protects.
	if _, err := svc.MapGet(ctx, slug, "k", "alice@example.com"); err != nil {
		t.Fatalf("entries must survive a refused retype: %v", err)
	}
}

// A slug that is not a MAP must not be drivable through the map routes —
// otherwise a TEXT doc would silently grow a head nothing renders.
func TestMap_RoutesRefuseNonMapSlug(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("plaintext")
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: "TEXT", Title: "doc", ContentType: "text/markdown",
		Content: "hello", NamedSlug: &slug,
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MapGet(ctx, slug, "k", "alice@example.com"); err == nil ||
		!strings.Contains(err.Error(), "not a MAP") {
		t.Fatalf("a TEXT slug must be refused by the map routes; got %v", err)
	}
}

// The flag is the feature's backout: with it off the routes refuse, and the
// stored entries are left untouched.
func TestMap_DisabledByFlag(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	// deliberately NOT SetMapEnabled
	slug := unique("mapoff")
	if _, _, err := svc.MapPut(context.Background(), slug, artifacts.MapPutRequest{
		Title: "m", Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err == nil {
		t.Fatal("with the flag off the map routes must refuse")
	}
}

// Snapshot freezes head into the version chain, and a second snapshot of an
// unchanged head is a no-op — otherwise a scheduled snapshot would mint a
// version per run forever.
func TestMap_SnapshotAndNoOpRepeat(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapsnap")
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title: "m", Entries: []artifacts.MapPutEntry{{Key: "a", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	first, err := svc.MapSnapshot(ctx, slug, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.Unchanged || first.Entries != 1 {
		t.Fatalf("first snapshot should write v2 with 1 entry: %+v", first)
	}

	again, err := svc.MapSnapshot(ctx, slug, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged {
		t.Fatalf("snapshotting an unchanged head must be a no-op: %+v", again)
	}
	if *again.Version != *first.Version {
		t.Fatalf("a no-op snapshot must return the existing version, got v%d after v%d",
			*again.Version, *first.Version)
	}

	// After a real change it mints the next version.
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Entries: []artifacts.MapPutEntry{{Key: "b", Value: jsonVal(`2`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	third, err := svc.MapSnapshot(ctx, slug, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if third.Unchanged || *third.Version <= *first.Version || third.Entries != 2 {
		t.Fatalf("a changed head should mint the next version with 2 entries: %+v", third)
	}
}

// Criterion 7 — access is inherited from the document, not redefined by the
// map routes. Two different denials, and the difference matters: a caller who
// cannot read at all learns nothing (404), while a reader who lacks write
// already knows the map exists, so hiding it would only confuse them (403).
func TestMap_AccessIsInheritedNotRedefined(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapacl")

	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title:   "alice's map",
		Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	// Readers: alice and bob. Writers: alice only.
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	if _, err := st.UpdateAccessBySlug(ctx, slug,
		[]string{"alice@example.com", "bob@example.com"},
		[]string{"alice@example.com"}); err != nil {
		t.Fatal(err)
	}

	// Bob can read.
	if _, err := svc.MapGet(ctx, slug, "k", "bob@example.com"); err != nil {
		t.Fatalf("a reader on the document must be able to read its entries: %v", err)
	}
	// Bob cannot write, and is told so rather than being shown a 404.
	_, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`2`)}},
	}, "bob@example.com")
	if err == nil {
		t.Fatal("a reader without write access must not be able to put")
	}
	if status, _, _ := artifacts.WriteStatus(err); status != 403 {
		t.Fatalf("a reader without write should get 403, got %d (%v)", status, err)
	}
	// Carol has no access at all: the map must not even admit to existing.
	_, err = svc.MapGet(ctx, slug, "k", "carol@example.com")
	if err == nil {
		t.Fatal("a caller with no access must not be able to read")
	}
	if status, _, _ := artifacts.WriteStatus(err); status != 404 {
		t.Fatalf("a caller with no access should get 404, not 403; got %d (%v)", status, err)
	}
	// And alice's write still lands, so the ACL narrowed nothing for her.
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`3`)}},
	}, "alice@example.com"); err != nil {
		t.Fatalf("the owner must still be able to write: %v", err)
	}
}

// Staging round 1: every map validation error surfaced as HTTP 500, because
// WriteStatus never handled pgstore.ErrInvalidInput. The service-level tests
// missed it by asserting only that an error came back. These assert the
// STATUS, which is the half a caller actually sees.
func TestMap_ValidationErrorsAre400NotInternal(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapstatus")
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title: "m", Entries: []artifacts.MapPutEntry{{Key: "ok", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		entry artifacts.MapPutEntry
		names string // substring the message must carry
	}{
		{"illegal key", artifacts.MapPutEntry{Key: "bad/key", Value: jsonVal(`1`)}, "legal map key"},
		{"over-size value", artifacts.MapPutEntry{Key: "big", Value: jsonVal(`"` + strings.Repeat("x", 70000) + `"`)}, "bytes per value"},
		{"non-JSON value", artifacts.MapPutEntry{Key: "bad", Value: jsonVal(`not json`)}, "valid JSON"},
	}
	for _, c := range cases {
		_, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
			Entries: []artifacts.MapPutEntry{c.entry},
		}, "alice@example.com")
		if err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
		status, code, msg := artifacts.WriteStatus(err)
		if status != 400 {
			t.Errorf("%s: status = %d (%s), want 400 — a caller mistake must not read as an internal error", c.name, status, code)
		}
		if !strings.Contains(msg, c.names) {
			t.Errorf("%s: message %q must name what to fix (%q)", c.name, msg, c.names)
		}
	}
}

// PR #314 review: mapSlugForWrite returns ErrNotFound both for a slug that
// does not exist and for one the caller may not read — arti hides existence
// rather than returning 403. The create-on-absent branch treated both as
// "create", so a caller with no access could mint a MAP version over someone
// else's private slug. That bypasses versionGuard (createMap calls store.Put
// directly) and, because createMap passed no access list, would have
// republished a private document as world-readable.
func TestMap_CannotCreateOverAnInvisibleSlug(t *testing.T) {
	svc, ctx := mapSvc(t)
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	// Alice has a private TEXT document. It has no map entries, so the
	// stale-entry guard does not apply — this is the path that was open.
	slug := unique("mapinvisible")
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: "TEXT", Title: "alice's private doc", ContentType: "text/markdown",
		Content: "secret", NamedSlug: &slug,
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateAccessBySlug(ctx, slug, []string{"alice@example.com"}, nil); err != nil {
		t.Fatal(err)
	}

	// Mallory cannot see it, so cannot create over it either.
	_, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title:   "mallory's map",
		Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "mallory@example.com")
	if err == nil {
		t.Fatal("a caller who cannot read a slug must not be able to create a MAP over it")
	}
	// 404, not 403: telling Mallory the slug exists is the leak arti's
	// unreadable-slug convention exists to prevent.
	if status, _, _ := artifacts.WriteStatus(err); status != 404 {
		t.Fatalf("expected 404 so existence stays hidden, got %d (%v)", status, err)
	}

	// Alice's document is untouched: same type, same version, still private.
	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.ArtifactType != "TEXT" {
		t.Fatalf("alice's doc was retyped to %s", row.ArtifactType)
	}
	if *row.Version != 1 {
		t.Fatalf("a new version was minted over alice's doc: v%d", *row.Version)
	}
	if _, err := svc.Get(ctx, uuid.UUID(row.ArtifactID.Bytes), "mallory@example.com"); err == nil {
		t.Fatal("alice's doc became readable by mallory — the ACL was widened")
	}
}

// A caller can set the ACL when the map is created, so there is no window in
// which entries are written to a world-readable map before it is locked down.
func TestMap_CreateTakesAnAccessList(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapacl2")
	only := []string{"alice@example.com"}
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title:         "locked from birth",
		AllowedAccess: &only,
		Entries:       []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MapGet(ctx, slug, "k", "bob@example.com"); err == nil {
		t.Fatal("an access list given at creation must apply to the first entries too")
	}
}

// Found in a browser: chi hands back the RAW path segment, so a client that
// percent-encodes its key (encodeURIComponent does, for ':') made the server
// validate "cfg%3Aa" and reject it. ':' is the prefix separator, so this broke
// the per-key routes for very nearly every real key. The Go CLI and curl both
// missed it by not encoding ':' in the first place.
func TestMap_PerKeyRoutesAcceptAPercentEncodedKey(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapenc")
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title:   "m",
		Entries: []artifacts.MapPutEntry{{Key: "seen:cnv_1", Value: jsonVal(`{"a":1}`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{"seen:cnv_1", "seen%3Acnv_1"} {
		e, err := svc.MapGet(ctx, slug, form, "alice@example.com")
		if err != nil {
			t.Fatalf("key %q must resolve: %v", form, err)
		}
		if e.Key != "seen:cnv_1" {
			t.Fatalf("key %q resolved to %q", form, e.Key)
		}
	}
	// An encoded '/' still has to be refused: decoding must not smuggle a
	// character the charset excludes precisely to keep keys in one segment.
	if _, err := svc.MapGet(ctx, slug, "bad%2Fkey", "alice@example.com"); err == nil {
		t.Fatal("an encoded '/' must still be rejected")
	}
}

// Cursor Bugbot, round 5: the snapshot copied title, labels and scopes onto
// the new version but not the description, and description is per-version. So
// the first snapshot silently cleared a description set at create — and the
// description is what the catalog and search show.
func TestMap_SnapshotKeepsTitleDescriptionAndACL(t *testing.T) {
	svc, ctx := mapSvc(t)
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("mapsnapmeta")
	desc := "a map that keeps its description"
	private := []string{"alice@example.com"}

	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title:         "titled map",
		Description:   &desc,
		Labels:        []string{"demo"},
		AllowedAccess: &private,
		Entries:       []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MapSnapshot(ctx, slug, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.Description == nil || *row.Description != desc {
		t.Fatalf("the snapshot dropped the description: %v", row.Description)
	}
	if row.Title != "titled map" {
		t.Fatalf("the snapshot changed the title to %q", row.Title)
	}
	// The ACL must survive too — a snapshot is a new version of the same
	// document, not a fresh publication.
	for _, a := range row.AllowedAccess {
		if a == "*" {
			t.Fatalf("the snapshot widened the ACL to everyone: %v", row.AllowedAccess)
		}
	}
	if _, err := svc.MapGet(ctx, slug, "k", "bob@example.com"); err == nil {
		t.Fatal("the snapshot made a private map readable")
	}
}

// Cursor Bugbot on #323 reported this as a non-owner ACL escalation. It is
// not reachable: the map routes never modify an existing map's access list,
// so allowed_access on a put to an existing slug changed nothing. What it did
// do was change nothing SILENTLY, which lets a caller believe they locked a
// map that is still open. It is refused now.
func TestMap_AccessListOnAnExistingMapIsRefusedNotIgnored(t *testing.T) {
	svc, ctx := mapSvc(t)
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("mapaclexisting")

	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title: "alice's map", Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}

	world := []string{"*"}
	_, _, err = svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		AllowedAccess: &world,
		Entries:       []artifacts.MapPutEntry{{Key: "k2", Value: jsonVal(`2`)}},
	}, "alice@example.com")
	if err == nil {
		t.Fatal("an access list on an existing map must be refused, not ignored")
	}
	if status, _, msg := artifacts.WriteStatus(err); status != 400 {
		t.Fatalf("expected 400, got %d (%s)", status, msg)
	}
	after, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.AllowedAccess) != len(before.AllowedAccess) {
		t.Fatalf("the ACL moved: %v -> %v", before.AllowedAccess, after.AllowedAccess)
	}
	// A write with no access fields is unaffected.
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Entries: []artifacts.MapPutEntry{{Key: "k3", Value: jsonVal(`3`)}},
	}, "alice@example.com"); err != nil {
		t.Fatalf("an ordinary entry write must still work: %v", err)
	}
}

// A map's head is keyed by a map id minted at creation, so the head belongs to
// the document rather than to the name it was published under. Archiving hands
// the name back, and whoever takes it next gets an empty map — the four
// separate rules that used to hold this line (an owner gate, an ACL carried
// forward from the archived version, a fallback for an intervening document,
// and the same gate again for an empty map) are all one property now.
func TestMap_ReclaimingASlugStartsANewEmptyMap(t *testing.T) {
	svc, ctx := mapSvc(t)
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("mapreclaim")

	private := []string{"alice@example.com"}
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title: "alice's private map", AllowedAccess: &private,
		Entries: []artifacts.MapPutEntry{{Key: "secret:a", Value: jsonVal(`"private"`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatal(err)
	}

	// Bob is not the owner and takes the freed name, which arti allows for
	// every other type too.
	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title: "bob's map", Entries: []artifacts.MapPutEntry{{Key: "b:1", Value: jsonVal(`1`)}},
	}, "bob@example.com"); err != nil {
		t.Fatalf("a freed slug must be reusable: %v", err)
	}

	got, err := svc.MapList(ctx, slug, "", "", 100, "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Key != "b:1" {
		t.Fatalf("bob's map must hold only his own key, got %+v", got.Entries)
	}
	if _, err := svc.MapGet(ctx, slug, "secret:a", "bob@example.com"); err == nil {
		t.Fatal("alice's entries came back under bob's map")
	}
	// Nor through alice: her map is archived, and the name is bob's now.
	if _, err := svc.MapGet(ctx, slug, "secret:a", "alice@example.com"); err == nil {
		t.Fatal("the archived map's entries are still reachable at the slug")
	}
	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.AllowedAccess) == 1 && row.AllowedAccess[0] == "alice@example.com" {
		t.Fatal("bob's new map inherited the archived map's access list")
	}
}

// The other half of the same property: archiving hides a map's head, and
// unarchiving the version brings it back, because the entries were never
// keyed to the name in the first place.
func TestMap_UnarchiveRestoresTheHead(t *testing.T) {
	svc, ctx := mapSvc(t)
	slug := unique("mapunarchive")

	if _, _, err := svc.MapPut(ctx, slug, artifacts.MapPutRequest{
		Title: "alice's map", Entries: []artifacts.MapPutEntry{{Key: "k", Value: jsonVal(`1`)}},
	}, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MapGet(ctx, slug, "k", "alice@example.com"); err == nil {
		t.Fatal("an archived map still answers reads")
	}
	if _, err := svc.UnarchiveByID(ctx, uuid.UUID(row.ArtifactID.Bytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MapGet(ctx, slug, "k", "alice@example.com"); err != nil {
		t.Fatalf("unarchiving must bring the head back: %v", err)
	}
}
