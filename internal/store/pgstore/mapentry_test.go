//go:build integration

package pgstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func newMapStore(t *testing.T) (*pgstore.Store, context.Context) {
	t.Helper()
	return pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{}), context.Background()
}

// seedMap mints v1 of a MAP so the slug exists, mirroring what the create
// path does before any entry is written, and returns the map id its entries
// hang off.
func seedMap(t *testing.T, st *pgstore.Store, ctx context.Context, slug string) pgtype.UUID {
	t.Helper()
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeMap, NamedSlug: &slug, Title: "map " + slug,
		ContentType: pgstore.MapContentType, Content: []byte{}, Creator: "alice@example.com",
		MapID: pgstore.NewMapID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row.MapID
}

func put(t *testing.T, st *pgstore.Store, ctx context.Context, slug string, mapID pgtype.UUID, w ...pgstore.MapEntryWrite) []pgstore.MapWriteResult {
	t.Helper()
	res, err := st.MapPut(ctx, slug, mapID, w, "alice@example.com", nil)
	if err != nil {
		t.Fatalf("MapPut: %v", err)
	}
	return res
}

func val(s string) json.RawMessage { return json.RawMessage(s) }

// Appendix C 4, 5, 8 — a fresh key is rev 1, an unguarded rewrite bumps rev,
// and the stored value is what came back.
func TestMap_PutAndGet(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)

	res := put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "cfg:a", Value: val(`{"a":1}`)})
	if res[0].Entry == nil || res[0].Entry.Rev != 1 {
		t.Fatalf("first write should be rev 1, got %+v", res[0])
	}
	got, err := st.MapGet(ctx, mid, "cfg:a")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Value) != `{"a": 1}` && string(got.Value) != `{"a":1}` {
		t.Fatalf("value round-trip: %s", got.Value)
	}
	res = put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "cfg:a", Value: val(`{"a":2}`)})
	if res[0].Entry.Rev != 2 {
		t.Fatalf("unguarded rewrite should bump rev to 2, got %d", res[0].Entry.Rev)
	}
}

// Appendix C 6 — if_absent does not overwrite, reports the incumbent, and
// leaves rev alone. This is the fleet's claim-lease primitive.
func TestMap_IfAbsentDoesNotOverwrite(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)

	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "seen:x", Value: val(`"first"`), IfAbsent: true})
	res := put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "seen:x", Value: val(`"second"`), IfAbsent: true})
	if !res[0].Conflict {
		t.Fatal("second if_absent write should have conflicted")
	}
	if res[0].Entry != nil {
		t.Fatal("a lost claim must not report a written entry")
	}
	if res[0].Incumbent == nil || string(res[0].Incumbent.Value) != `"first"` {
		t.Fatalf("incumbent should carry the winning value, got %+v", res[0].Incumbent)
	}
	if res[0].Incumbent.Rev != 1 {
		t.Fatalf("a lost claim must not bump rev; got %d", res[0].Incumbent.Rev)
	}
}

// Appendix C 7, 8 — a stale if_rev writes nothing; the current one writes and
// bumps by exactly one.
func TestMap_IfRevCompareAndSet(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "k", Value: val(`1`)})

	stale := int64(99)
	res := put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "k", Value: val(`2`), IfRev: &stale})
	if !res[0].Conflict {
		t.Fatal("stale if_rev should conflict")
	}
	got, _ := st.MapGet(ctx, mid, "k")
	if string(got.Value) != "1" || got.Rev != 1 {
		t.Fatalf("stale if_rev must write nothing; got value=%s rev=%d", got.Value, got.Rev)
	}

	cur := int64(1)
	res = put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "k", Value: val(`2`), IfRev: &cur})
	if res[0].Conflict || res[0].Entry.Rev != 2 {
		t.Fatalf("matching if_rev should write and bump to rev 2, got %+v", res[0])
	}
}

// Appendix C 9 — the guard is what makes concurrency safe. Many writers race
// on one key with if_absent; exactly one may win and rev must stay at 1.
func TestMap_ConcurrentClaimsExactlyOneWinner(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)

	const n = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := st.MapPut(ctx, slug, mid, []pgstore.MapEntryWrite{{
				Key: "claim", Value: val(fmt.Sprintf(`"writer-%d"`, i)), IfAbsent: true,
			}}, "alice@example.com", nil)
			if err != nil {
				return
			}
			if res[0].Entry != nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one claim may win, got %d", wins)
	}
	got, _ := st.MapGet(ctx, mid, "claim")
	if got.Rev != 1 {
		t.Fatalf("a contested claim must leave rev at 1, got %d", got.Rev)
	}
}

// Appendix C 10 — the per-value limit is enforced and the error names it.
func TestMap_ValueOverLimitRejected(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)

	big := val(`"` + strings.Repeat("x", pgstore.MapMaxValueBytes) + `"`)
	_, err := st.MapPut(ctx, slug, mid, []pgstore.MapEntryWrite{{Key: "big", Value: big}}, "alice@example.com", nil)
	if err == nil {
		t.Fatal("an over-size value must be rejected")
	}
	if !strings.Contains(err.Error(), "bytes per value") {
		t.Fatalf("the error must name the limit it hit; got %v", err)
	}
	if _, err := st.MapGet(ctx, mid, "big"); err == nil {
		t.Fatal("a rejected write must leave no entry behind")
	}
}

// Appendix C 13, 14 — prefix listing is scoped to its prefix AND its map,
// and paging never repeats or drops a key.
func TestMap_ListPrefixAndPaging(t *testing.T) {
	st, ctx := newMapStore(t)
	slugA, slugB := unique("mapa"), unique("mapb")
	midA := seedMap(t, st, ctx, slugA)
	midB := seedMap(t, st, ctx, slugB)

	for _, k := range []string{"seen:1", "seen:2", "seen:3", "cfg:x", "snap:2026"} {
		put(t, st, ctx, slugA, midA, pgstore.MapEntryWrite{Key: k, Value: val(`1`)})
	}
	put(t, st, ctx, slugB, midB, pgstore.MapEntryWrite{Key: "seen:other", Value: val(`1`)})

	rows, _, err := st.MapList(ctx, midA, "seen:", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("prefix seen: should match 3 keys, got %d", len(rows))
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Key, "seen:") {
			t.Fatalf("prefix filter leaked %q", r.Key)
		}
		if r.MapID != midA {
			t.Fatalf("another map's key leaked in: %q", r.Key)
		}
	}

	seen := map[string]int{}
	cursor := ""
	for page := 0; page < 10; page++ {
		rows, next, err := st.MapList(ctx, midA, "", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			seen[r.Key]++
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("paging should visit all 5 keys, saw %d", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("paging returned %q %d times; a cursor must be exclusive", k, n)
		}
	}
}

// Appendix C 15 — a key outside the charset is refused before anything is
// written. `/` matters most: it is what keeps a key inside one path segment.
func TestMap_IllegalKeysRejected(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)

	for _, bad := range []string{"a/b", "", strings.Repeat("k", pgstore.MapMaxKeyLen+1), "sp ace", "wild*"} {
		if _, err := st.MapPut(ctx, slug, mid, []pgstore.MapEntryWrite{{Key: bad, Value: val(`1`)}}, "alice@example.com", nil); err == nil {
			t.Fatalf("key %q should have been rejected", bad)
		}
	}
	if pgstore.MapKeyValid("a/b") {
		t.Fatal("MapKeyValid must reject '/' so a key stays inside one URL path segment")
	}
	if !pgstore.MapKeyValid("seen:cnv_123") {
		t.Fatal("MapKeyValid must accept the ':' prefix convention")
	}
}

// Appendix C 16 — deleting an absent key is a success with count 0, so a
// caller cleaning up does not have to check first.
func TestMap_DeleteAbsentIsNotAnError(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "gone", Value: val(`1`)})

	if n, err := st.MapDelete(ctx, slug, mid, []string{"gone"}, nil); err != nil || n != 1 {
		t.Fatalf("delete of a present key: n=%d err=%v", n, err)
	}
	if n, err := st.MapDelete(ctx, slug, mid, []string{"gone"}, nil); err != nil || n != 0 {
		t.Fatalf("delete of an absent key must succeed with 0; n=%d err=%v", n, err)
	}
}

// Appendix C 18, 26 — the snapshot body is NDJSON in key order, one line per
// entry, and every line parses on its own.
func TestMap_SnapshotBodyIsOrderedNDJSON(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)
	for _, k := range []string{"c", "a", "b"} {
		put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: k, Value: val(`{"k":"` + k + `"}`)})
	}

	body, count, err := st.MapSnapshotBody(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("snapshot should report 3 entries, got %d", count)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("snapshot should be 3 NDJSON lines, got %d", len(lines))
	}
	var keys []string
	for _, ln := range lines {
		var row struct {
			Key string `json:"key"`
			Rev int64  `json:"rev"`
		}
		if err := json.Unmarshal([]byte(ln), &row); err != nil {
			t.Fatalf("line is not valid JSON: %q: %v", ln, err)
		}
		keys = append(keys, row.Key)
	}
	if strings.Join(keys, ",") != "a,b,c" {
		t.Fatalf("snapshot must be in key order, got %v", keys)
	}
}

// Appendix C 20 — a MAP slug rejects append, because its versions are
// snapshots of a keyed head and concatenation would produce a body that no
// longer describes the head.
func TestMap_AppendRejected(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	seedMap(t, st, ctx, slug)

	_, err := st.Append(ctx, pgstore.AppendInput{
		NamedSlug: slug, Content: []byte("more"), Creator: "alice@example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot append to MAP") {
		t.Fatalf("append to a MAP must be refused; got %v", err)
	}
}

// Appendix C 23 — a MAP is reached through its slug, so a slugless MAP would
// have a version nothing could ever write to.
func TestMap_RequiresSlug(t *testing.T) {
	st, ctx := newMapStore(t)
	_, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeMap, Title: "no slug",
		ContentType: pgstore.MapContentType, Content: []byte{}, Creator: "alice@example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "MAP requires named_slug") {
		t.Fatalf("a slugless MAP must be refused; got %v", err)
	}
}

// Appendix C 24 — a MAP is not a zip, so every package-shaped branch (file
// serving, manifest listing, append rejection reason) must skip it.
func TestMap_IsNotPackageLike(t *testing.T) {
	if pgstore.IsPackageLike(pgstore.TypeMap) {
		t.Fatal("IsPackageLike(MAP) must be false")
	}
}

// A batch is refused whole when any entry is bad, so a caller never has to
// reason about a half-applied batch.
func TestMap_BadEntryRejectsWholeBatch(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("map")
	mid := seedMap(t, st, ctx, slug)

	rev := int64(1)
	_, err := st.MapPut(ctx, slug, mid, []pgstore.MapEntryWrite{
		{Key: "good", Value: val(`1`)},
		{Key: "alsobad", Value: val(`1`), IfAbsent: true, IfRev: &rev},
	}, "alice@example.com", nil)
	if err == nil {
		t.Fatal("two guards on one entry must be refused")
	}
	if _, err := st.MapGet(ctx, mid, "good"); err == nil {
		t.Fatal("a refused batch must write nothing, including its valid entries")
	}
}

// Appendix C 12 — the prefix filter must be a half-open range so the
// (named_slug, key) primary-key btree can serve it. The risk this guards is
// the one in the design doc: a prefix list degrading to a sequential scan as
// the table grows. So the table has to be big enough for the planner to have
// an opinion — on a handful of rows it picks a Seq Scan because that is
// genuinely cheaper, and asserting against that would be testing nothing.
func TestMap_ListPrefixUsesIndexScanAtScale(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	slug := unique("mapidx")
	mid := seedMap(t, st, ctx, slug)

	// 5,000 rows under one slug, inserted directly: MapPut caps a batch at
	// 100, and this is a planner fixture, not a test of the write path.
	if _, err := pool.Exec(ctx, `
		INSERT INTO artifact_map_entry (map_id, key, value, size_bytes, updated_by)
		SELECT $1, 'seen:' || lpad(g::text, 6, '0'), '1'::jsonb, 1, 'alice@example.com'
		FROM generate_series(1, 5000) g`, mid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artifact_map_entry WHERE map_id = $1`, mid)
	})
	if _, err := pool.Exec(ctx, `ANALYZE artifact_map_entry`); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `EXPLAIN SELECT * FROM artifact_map_entry
		WHERE map_id = $1 AND key >= $2 AND key > $3 AND key < $4 ORDER BY key LIMIT 10`,
		mid, "seen:000", "", "seen:001")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if strings.Contains(plan.String(), "Seq Scan") {
		t.Fatalf("a prefix list over 5000 rows must not plan as a sequential scan:\n%s", plan.String())
	}
	if !strings.Contains(plan.String(), "Index") {
		t.Fatalf("a prefix list should plan as an index scan:\n%s", plan.String())
	}

	// And the range really is served in key order without a sort step: that
	// is what makes the cursor cheap at any depth.
	if strings.Contains(plan.String(), "Sort") {
		t.Fatalf("ORDER BY key should be satisfied by the index, not a sort:\n%s", plan.String())
	}
}

// prefixEnd is what turns a prefix into that half-open range, so its
// behaviour at the edges is part of the isolation story: a wrong successor
// either drops keys or leaks keys from the adjacent prefix. "a" and "b" are
// adjacent and both reachable, which ":" and ";" are not — ';' is outside the
// key charset, so the common "seen:" case is bounded by construction.
func TestMap_PrefixEndBounds(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("mapend")
	mid := seedMap(t, st, ctx, slug)
	for _, k := range []string{"a1", "az", "b1", "cfg", "cfgx"} {
		put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: k, Value: val(`1`)})
	}

	keys := func(prefix string) []string {
		rows, _, err := st.MapList(ctx, mid, prefix, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}

	if got := strings.Join(keys("a"), ","); got != "a1,az" {
		t.Fatalf(`prefix "a" must not leak the adjacent "b" range; got %s`, got)
	}
	// A prefix is a prefix, not a namespace: "cfgx" starts with "cfg" and so
	// belongs in its listing. Callers who want a namespace end it with ':'.
	if got := strings.Join(keys("cfg"), ","); got != "cfg,cfgx" {
		t.Fatalf(`prefix "cfg" must match every key starting with it; got %s`, got)
	}
	if got := len(keys("")); got != 5 {
		t.Fatalf("an empty prefix must be unbounded; got %d of 5", got)
	}
}

// Review round 1, finding 2: an if_rev CAS against a key that is NOT there
// must fail. The obvious upsert shape (ON CONFLICT ... DO UPDATE ... WHERE
// rev = $n) fires its INSERT arm when no row exists, so a compare-and-set
// against a concurrently deleted key would silently succeed and resurrect it
// at rev 1 — which is the opposite of what the caller asked for.
func TestMap_IfRevAgainstAbsentKeyMustNotResurrect(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("mapcas")
	mid := seedMap(t, st, ctx, slug)

	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "k", Value: val(`1`)})
	if _, err := st.MapDelete(ctx, slug, mid, []string{"k"}, nil); err != nil {
		t.Fatal(err)
	}

	rev := int64(1)
	res := put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "k", Value: val(`2`), IfRev: &rev})
	if !res[0].Conflict {
		t.Fatalf("if_rev against an absent key must conflict, got %+v", res[0])
	}
	if _, err := st.MapGet(ctx, mid, "k"); err == nil {
		t.Fatal("if_rev against an absent key must not recreate it")
	}
}

// Review round 1, Q6: a page is capped by bytes as well as by rows. Without
// it the worst case is 1000 x 64 KiB = 64 MiB on a 1024 MiB pod. The cursor
// must still be correct when the byte budget is what ended the page, or a
// caller paging through a map of large values would silently lose entries.
func TestMap_ListPageIsCappedByBytes(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("mapbytes")
	mid := seedMap(t, st, ctx, slug)

	// 40 values of ~48 KiB each: ~1.9 MiB total, comfortably over the 1 MiB
	// page budget but well inside the per-slug cap.
	big := `"` + strings.Repeat("x", 48*1024) + `"`
	for i := 0; i < 40; i++ {
		put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{
			Key: fmt.Sprintf("big:%03d", i), Value: val(big),
		})
	}

	rows, cursor, err := st.MapList(ctx, mid, "big:", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) >= 40 {
		t.Fatalf("the byte budget should have trimmed the page, got all %d rows", len(rows))
	}
	if cursor == "" {
		t.Fatal("a page trimmed by the byte budget must still hand back a cursor")
	}

	// Paging on that cursor must reach every key exactly once.
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Key]++
	}
	for i := 0; i < 20 && cursor != ""; i++ {
		next, c, err := st.MapList(ctx, mid, "big:", cursor, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range next {
			seen[r.Key]++
		}
		cursor = c
	}
	if len(seen) != 40 {
		t.Fatalf("paging must reach all 40 keys, saw %d", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("key %q seen %d times across pages", k, n)
		}
	}
}

// A snapshot must contain EVERY entry. MapSnapshotBody pages through head, and
// once MapList grew a byte budget a page could come back short of the row
// limit without being the last page — so a loop that decided "short page means
// done" would silently drop the tail of a map with large values. Silent
// truncation in a snapshot is the worst failure this type can have: the
// version looks complete and is not.
func TestMap_SnapshotCoversEveryEntryDespiteTheByteBudget(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("mapsnapbig")
	mid := seedMap(t, st, ctx, slug)

	// 60 values of ~48 KiB: ~2.8 MiB, several times the 1 MiB page budget,
	// while every page stays far below the 1000-row limit.
	big := `"` + strings.Repeat("y", 48*1024) + `"`
	for i := 0; i < 60; i++ {
		put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{
			Key: fmt.Sprintf("k:%03d", i), Value: val(big),
		})
	}

	body, count, err := st.MapSnapshotBody(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if count != 60 {
		t.Fatalf("snapshot reported %d entries, want 60 — the page loop stopped early", count)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 60 {
		t.Fatalf("snapshot body has %d lines, want 60", len(lines))
	}
	seen := map[string]bool{}
	for _, ln := range lines {
		var row struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(ln), &row); err != nil {
			t.Fatalf("bad NDJSON line: %v", err)
		}
		if seen[row.Key] {
			t.Fatalf("key %q appears twice in the snapshot", row.Key)
		}
		seen[row.Key] = true
	}
	for i := 0; i < 60; i++ {
		if !seen[fmt.Sprintf("k:%03d", i)] {
			t.Fatalf("snapshot is missing key k:%03d", i)
		}
	}
}

// Appendix C 11, which the first round never wrote: the PER-SLUG caps. The
// per-value cap had a test and these did not, so the enforcement code read
// correctly and was never executed. Both ceilings are checked after the writes
// and inside the same transaction, so a breach must roll the whole batch back
// rather than leaving the map over its limit.
func TestMap_PerSlugCapsRollTheBatchBack(t *testing.T) {
	st, ctx := newMapStore(t)
	pool := newPool(t)

	// --- byte ceiling. Seed just under it, then try to cross it.
	slug := unique("mapbytecap")
	mid := seedMap(t, st, ctx, slug)
	// 8 MiB cap; seed ~8 MiB - 100 KiB directly so the test stays quick.
	if _, err := pool.Exec(ctx, `
		INSERT INTO artifact_map_entry (map_id, key, value, size_bytes, updated_by)
		SELECT $1, 'fill:' || lpad(g::text, 4, '0'), '1'::jsonb, 64000, 'alice@example.com'
		FROM generate_series(1, 130) g`, mid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artifact_map_entry WHERE map_id = $1`, mid)
	})
	before, err := st.MapStats(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if before.Bytes >= pgstore.MapMaxBytesPerSlug {
		t.Fatalf("fixture already over the cap: %d", before.Bytes)
	}

	big := val(`"` + strings.Repeat("z", 60*1024) + `"`)
	_, err = st.MapPut(ctx, slug, mid, []pgstore.MapEntryWrite{
		{Key: "over:1", Value: big}, {Key: "over:2", Value: big},
		{Key: "over:3", Value: big}, {Key: "over:4", Value: big},
	}, "alice@example.com", nil)
	if err == nil {
		t.Fatal("a batch crossing the per-slug byte cap must be refused")
	}
	if !strings.Contains(err.Error(), "bytes per map") {
		t.Fatalf("the error must name the per-slug byte limit; got %v", err)
	}
	after, err := st.MapStats(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if after.Keys != before.Keys || after.Bytes != before.Bytes {
		t.Fatalf("a refused batch must roll back whole: %+v became %+v", before, after)
	}
	if _, err := st.MapGet(ctx, mid, "over:1"); err == nil {
		t.Fatal("an entry from the refused batch was left behind")
	}

	// --- key ceiling, same shape.
	slug2 := unique("mapkeycap")
	mid2 := seedMap(t, st, ctx, slug2)
	if _, err := pool.Exec(ctx, `
		INSERT INTO artifact_map_entry (map_id, key, value, size_bytes, updated_by)
		SELECT $1, 'k:' || lpad(g::text, 6, '0'), '1'::jsonb, 1, 'alice@example.com'
		FROM generate_series(1, $2::int) g`, mid2, pgstore.MapMaxKeysPerSlug-2); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artifact_map_entry WHERE map_id = $1`, mid2)
	})
	_, err = st.MapPut(ctx, slug2, mid2, []pgstore.MapEntryWrite{
		{Key: "extra:1", Value: val(`1`)}, {Key: "extra:2", Value: val(`1`)},
		{Key: "extra:3", Value: val(`1`)},
	}, "alice@example.com", nil)
	if err == nil {
		t.Fatal("a batch crossing the per-slug key cap must be refused")
	}
	if !strings.Contains(err.Error(), "keys per map") {
		t.Fatalf("the error must name the per-slug key limit; got %v", err)
	}
	st2, err := st.MapStats(ctx, mid2)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Keys > pgstore.MapMaxKeysPerSlug {
		t.Fatalf("the map is over its key cap after a refused batch: %d", st2.Keys)
	}

	// A batch that lands exactly ON the ceiling is allowed — the cap is a
	// maximum, not a threshold to stop short of.
	if _, err := st.MapPut(ctx, slug2, mid2, []pgstore.MapEntryWrite{
		{Key: "extra:1", Value: val(`1`)}, {Key: "extra:2", Value: val(`1`)},
	}, "alice@example.com", nil); err != nil {
		t.Fatalf("a batch landing exactly on the key cap must be allowed: %v", err)
	}
}

// MapBrowse backs the viewer's table: search, sort, page, and a total that
// reflects the search rather than the whole map.
func TestMap_BrowseSearchSortPage(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("mapbrowse")
	mid := seedMap(t, st, ctx, slug)

	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "alpha", Value: val(`{"note":"needle here"}`)})
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "beta", Value: val(`{"note":"plain"}`)})
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "needle-key", Value: val(`{"note":"plain"}`)})
	// Give gamma a higher rev so rev-sort has something to order by.
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "gamma", Value: val(`1`)})
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "gamma", Value: val(`2`)})
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "gamma", Value: val(`3`)})

	keysOf := func(rows []pgstore.MapBrowseRow) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}

	// Default: key ascending, everything.
	rows, total, err := st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	if got := strings.Join(keysOf(rows), ","); got != "alpha,beta,gamma,needle-key" {
		t.Fatalf("default order = %s", got)
	}

	// Search matches the KEY and the VALUE text, and total reflects the search.
	rows, total, err = st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid, Q: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("search total = %d, want 2 (one key match, one value match)", total)
	}
	if got := strings.Join(keysOf(rows), ","); got != "alpha,needle-key" {
		t.Fatalf("search hits = %s", got)
	}

	// Sort by rev, descending: gamma is at rev 3, everything else at 1.
	rows, _, err = st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid, Sort: "rev", Dir: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Key != "gamma" || rows[0].Rev != 3 {
		t.Fatalf("rev desc should lead with gamma@3, got %s@%d", rows[0].Key, rows[0].Rev)
	}

	// Paging by offset visits every key exactly once.
	seen := map[string]int{}
	for off := 0; off < 10; off += 2 {
		page, _, err := st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid, Limit: 2, Offset: off})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			seen[r.Key]++
		}
	}
	if len(seen) != 4 {
		t.Fatalf("offset paging saw %d keys, want 4", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("key %q seen %d times across pages", k, n)
		}
	}

	// An unknown sort column falls back to key rather than reaching the SQL.
	rows, _, err = st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid, Sort: "key; DROP TABLE artifact_map_entry--"})
	if err != nil {
		t.Fatalf("an unknown sort must fall back, not error: %v", err)
	}
	if got := strings.Join(keysOf(rows), ","); got != "alpha,beta,gamma,needle-key" {
		t.Fatalf("unknown sort should order by key, got %s", got)
	}
}

// A table must not ship whole values: 200 rows of 64 KiB would be 12 MiB.
// The preview is capped and the row says the full value is elsewhere.
func TestMap_BrowseTruncatesValues(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("mapbrowsebig")
	mid := seedMap(t, st, ctx, slug)
	big := `"` + strings.Repeat("q", 4096) + `"`
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "big", Value: val(big)})
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "small", Value: val(`"tiny"`)})

	rows, _, err := st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]pgstore.MapBrowseRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if b := byKey["big"]; !b.Truncated || len(b.Value) > pgstore.MapPreviewBytes {
		t.Fatalf("a large value must come back truncated: truncated=%v len=%d", b.Truncated, len(b.Value))
	}
	if b := byKey["big"]; b.SizeBytes < 4096 {
		t.Fatalf("the row must still report the REAL size, got %d", b.SizeBytes)
	}
	if s := byKey["small"]; s.Truncated {
		t.Fatal("a small value must not be marked truncated")
	}
}

// Cursor Bugbot, round 4: total comes from count(*) OVER (), a window over the
// returned rows — so an EMPTY page returns no rows at all and total stays 0.
// A reader whose offset went stale (entries deleted under them, or a bookmarked
// page) then sees "0 of 0" and is told the map is empty when it is not.
func TestMap_BrowseTotalSurvivesAnEmptyPage(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("mapemptypage")
	mid := seedMap(t, st, ctx, slug)
	for i := 0; i < 5; i++ {
		put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: fmt.Sprintf("k:%02d", i), Value: val(`1`)})
	}

	rows, total, err := st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid, Limit: 10, Offset: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("offset 100 of 5 entries should return no rows, got %d", len(rows))
	}
	if total != 5 {
		t.Fatalf("total = %d past the last page, want 5 — the pager cannot recover from 0", total)
	}

	// A search that genuinely matches nothing is still 0, not a false count.
	_, total, err = st.MapBrowse(ctx, pgstore.MapBrowseInput{MapID: mid, Q: "nothingmatchesthis"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("a real no-match must report 0, got %d", total)
	}
}

// NOTE: there is deliberately NO concurrency test for snapshot consistency.
//
// MapSnapshotBody pages head through several queries and runs them in one
// read-only REPEATABLE READ transaction, so the whole scan sees one instant.
// Three attempts at a test for that failed to be worth keeping:
//
//   - asserting "no key written after the scan began" is flaky, because
//     Postgres takes the REPEATABLE READ snapshot at the first statement and
//     not at BeginTx, so a write in that window is legitimately in view;
//   - keeping one racing key alive at a time removes the flake and also
//     removes the discrimination — it passes under READ COMMITTED too;
//   - deleting seeded keys during the scan is the right shape, and still does
//     not discriminate at 10,000 rows, because the batched scan finishes far
//     faster than a delete loop can race it.
//
// A test that cannot fail against the unfixed code is worse than none: it
// tells the next reader this case is covered. The multi-page completeness half
// IS covered deterministically, without concurrency, by
// TestMap_SnapshotCoversEveryEntryDespiteTheByteBudget above. The isolation
// level itself rests on documented Postgres semantics, not on a test here.

// depthfirst, round 5: the service authorizes, then the store locks and
// writes. A writer revoked (or the document archived) while it waited for the
// lock still wrote. Put and Append re-check inside their transaction for
// exactly this reason (DD-0055 D8) and MapPut did not.
//
// The race window itself is not what this pins — it pins that the hook is
// invoked inside the write and that a refusal writes nothing. That is the part
// which would regress if someone dropped the callback.
func TestMap_WritesReverifyAuthorizationInsideTheTransaction(t *testing.T) {
	st, ctx := newMapStore(t)
	slug := unique("maprecheck")
	mid := seedMap(t, st, ctx, slug)
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "existing", Value: val(`"before"`)})

	refuse := errors.New("access revoked while the write queued")
	called := 0
	check := func(ctx context.Context, fresh sqlc.Artifact) error {
		called++
		if fresh.NamedSlug == nil || *fresh.NamedSlug != slug {
			t.Errorf("the hook must see the slug's own fresh row, got %v", fresh.NamedSlug)
		}
		return refuse
	}

	if _, err := st.MapPut(ctx, slug, mid, []pgstore.MapEntryWrite{
		{Key: "sneaked", Value: val(`1`)},
	}, "mallory@example.com", check); !errors.Is(err, refuse) {
		t.Fatalf("a refused put must surface the hook's error, got %v", err)
	}
	if called != 1 {
		t.Fatalf("the hook ran %d times, want 1", called)
	}
	if _, err := st.MapGet(ctx, mid, "sneaked"); err == nil {
		t.Fatal("a refused put wrote an entry anyway")
	}

	called = 0
	if _, err := st.MapDelete(ctx, slug, mid, []string{"existing"}, check); !errors.Is(err, refuse) {
		t.Fatalf("a refused delete must surface the hook's error, got %v", err)
	}
	if called != 1 {
		t.Fatalf("the delete hook ran %d times, want 1", called)
	}
	if _, err := st.MapGet(ctx, mid, "existing"); err != nil {
		t.Fatalf("a refused delete removed the entry anyway: %v", err)
	}
}

// Cursor Bugbot, round 6: MapPut took the slug lock before re-checking and
// MapDelete did not — introduced by the previous round's own fix. Without it a
// revoke committed by UpdateAccessBySlug or TransferSlugOwner, which both take
// that lock, can land between the re-check and the delete.
//
// Holding the lock from another transaction and watching the delete fail to
// make progress is what proves it is taken; a refusal test alone cannot.
func TestMap_DeleteWaitsOnTheSlugLock(t *testing.T) {
	st, ctx := newMapStore(t)
	pool := newPool(t)
	slug := unique("mapdellock")
	mid := seedMap(t, st, ctx, slug)
	put(t, st, ctx, slug, mid, pgstore.MapEntryWrite{Key: "doomed", Value: val(`1`)})

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// DEFERRED, not called after the assertion below: a t.Fatal there would
	// otherwise leak this transaction, and it holds the advisory lock, so the
	// package's remaining tests would block instead of failing.
	released := false
	defer func() {
		if !released {
			_ = holder.Rollback(context.Background())
		}
	}()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", slug); err != nil {
		t.Fatal(err)
	}

	blocked, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	if _, err := st.MapDelete(blocked, slug, mid, []string{"doomed"}, nil); err == nil {
		t.Fatal("the delete completed while another transaction held the slug lock")
	}
	_ = holder.Rollback(ctx)
	released = true

	// With the lock free it goes through, so the failure above was the lock
	// and not something broken about the delete.
	if n, err := st.MapDelete(ctx, slug, mid, []string{"doomed"}, nil); err != nil || n != 1 {
		t.Fatalf("delete after the lock is released: n=%d err=%v", n, err)
	}
}
