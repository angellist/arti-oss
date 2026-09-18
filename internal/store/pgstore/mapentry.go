package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// Limits for MAP artifacts. Every one is enforced at the write boundary and
// named in the error, so a caller at a ceiling learns which ceiling.
const (
	MapMaxKeyLen       = 256
	MapMaxValueBytes   = 64 * 1024
	MapMaxKeysPerSlug  = 10_000
	MapMaxBytesPerSlug = 8 * 1024 * 1024
	MapMaxBatch        = 100
	MapMaxPageSize     = 1000
	MapDefaultPageSize = 100
	// Caps a page by bytes as well as rows: 1000 x 64 KiB is 64 MiB on a
	// 1024 MiB pod, which the row cap cannot see.
	MapMaxPageBytes = 1 << 20
)

// MapKeyValid reports whether k is a legal MAP key. `/` is excluded so a key
// is always safe in a single URL path segment; `:` is the prefix separator.
func MapKeyValid(k string) bool {
	if len(k) == 0 || len(k) > MapMaxKeyLen {
		return false
	}
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == ':' || r == '~' || r == '@' || r == '+':
		default:
			return false
		}
	}
	return true
}

// prefixEnd is the exclusive upper bound of prefix's key range, nil when
// unbounded. The carry loop cannot run off the end for a 7-bit charset.
func prefixEnd(prefix string) *string {
	if prefix == "" {
		return nil
	}
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xFF {
			b[i]++
			s := string(b[:i+1])
			return &s
		}
	}
	return nil // all 0xFF: every key with this prefix sorts above it, unbounded
}

// MapAccessCheck re-verifies authorization against the slug's FRESH latest
// version, inside the writing transaction. Put and Append take the same shape
// (DD-0055 D8): checking at the service layer and writing later leaves a
// window in which access can be revoked, or the document archived, and the
// write still lands.
type MapAccessCheck func(ctx context.Context, fresh sqlc.Artifact) error

// MapEntryWrite is one entry in a put batch. At most one guard may be set.
type MapEntryWrite struct {
	Key      string
	Value    json.RawMessage
	IfAbsent bool
	IfRev    *int64
}

// MapWriteResult reports what happened to one entry. On a lost guard,
// Conflict is true and Incumbent carries the row that won.
type MapWriteResult struct {
	Key       string
	Entry     *sqlc.ArtifactMapEntry
	Conflict  bool
	Incumbent *sqlc.ArtifactMapEntry
}

// MapStats is the slug's current occupancy, used for the limit check and
// reported to callers so they can see how close to a ceiling they are.
type MapStats struct {
	Keys  int64
	Bytes int64
}

// NewMapID mints the identity of a new map. Entries are keyed by it rather
// than by the slug, so a map that is archived stays archived and a new map at
// the same name starts empty.
func NewMapID() pgtype.UUID { return pgUUID(uuid.New()) }

func (s *Store) MapStats(ctx context.Context, mapID pgtype.UUID) (MapStats, error) {
	row, err := s.q.CountMapEntries(ctx, mapID)
	if err != nil {
		return MapStats{}, fmt.Errorf("pgstore: map stats: %w", err)
	}
	return MapStats{Keys: row.N, Bytes: row.Bytes}, nil
}

func (s *Store) MapGet(ctx context.Context, mapID pgtype.UUID, key string) (sqlc.ArtifactMapEntry, error) {
	row, err := s.q.GetMapEntry(ctx, sqlc.GetMapEntryParams{MapID: mapID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ArtifactMapEntry{}, ErrNotFound
	}
	if err != nil {
		return sqlc.ArtifactMapEntry{}, fmt.Errorf("pgstore: map get: %w", err)
	}
	return row, nil
}

// MapList returns one page of entries under prefix, ordered by key. The
// returned cursor is the last key of the page, or "" when the page is the
// last one. afterKey is exclusive.
func (s *Store) MapList(ctx context.Context, mapID pgtype.UUID, prefix, afterKey string, limit int) ([]sqlc.ArtifactMapEntry, string, error) {
	if limit <= 0 {
		limit = MapDefaultPageSize
	}
	if limit > MapMaxPageSize {
		limit = MapMaxPageSize
	}
	rows, err := s.q.ListMapEntries(ctx, sqlc.ListMapEntriesParams{
		MapID:     mapID,
		Prefix:    prefix,
		AfterKey:  afterKey,
		PrefixEnd: prefixEnd(prefix),
		Lim:       int32(limit),
	})
	if err != nil {
		return nil, "", fmt.Errorf("pgstore: map list: %w", err)
	}
	// Trimmed here rather than in SQL: a running SUM would cost a window
	// function on every list for a bound that rarely binds.
	truncated := false
	total := 0
	for i, r := range rows {
		total += int(r.SizeBytes)
		if total > MapMaxPageBytes && i > 0 {
			rows = rows[:i]
			truncated = true
			break
		}
	}
	cursor := ""
	if truncated || len(rows) == limit {
		cursor = rows[len(rows)-1].Key
	}
	return rows, cursor, nil
}

// MapPut applies a batch of entry writes in one transaction. It is
// all-or-nothing only for failures: a guard that legitimately loses is
// reported per entry and does not roll the batch back, because a batch of
// independent claims is the motivating case. A limit breach or a validation
// error rolls everything back and writes nothing.
// The slug is still needed alongside mapID: the advisory lock that serializes
// this against Put and UpdateAccessBySlug is taken on the name.
func (s *Store) MapPut(ctx context.Context, slug string, mapID pgtype.UUID, writes []MapEntryWrite, writer string, check MapAccessCheck) ([]MapWriteResult, error) {
	if slug == "" {
		return nil, fmt.Errorf("%w: map put requires named_slug", ErrInvalidInput)
	}
	if !mapID.Valid {
		return nil, fmt.Errorf("%w: map put requires map_id", ErrInvalidInput)
	}
	if writer == "" {
		return nil, fmt.Errorf("%w: map put requires writer", ErrInvalidInput)
	}
	if len(writes) == 0 {
		return nil, fmt.Errorf("%w: map put requires at least one entry", ErrInvalidInput)
	}
	if len(writes) > MapMaxBatch {
		return nil, fmt.Errorf("%w: batch of %d entries exceeds the limit of %d entries per batch",
			ErrInvalidInput, len(writes), MapMaxBatch)
	}
	seen := make(map[string]struct{}, len(writes))
	for _, w := range writes {
		if !MapKeyValid(w.Key) {
			return nil, fmt.Errorf("%w: key %q is not a legal map key: 1-%d chars from [A-Za-z0-9._:~@+-]",
				ErrInvalidInput, w.Key, MapMaxKeyLen)
		}
		if w.IfAbsent && w.IfRev != nil {
			return nil, fmt.Errorf("%w: key %q sets both if_absent and if_rev; at most one guard per entry",
				ErrInvalidInput, w.Key)
		}
		if len(w.Value) == 0 || !json.Valid(w.Value) {
			return nil, fmt.Errorf("%w: value for key %q is not valid JSON", ErrInvalidInput, w.Key)
		}
		if len(w.Value) > MapMaxValueBytes {
			return nil, fmt.Errorf("%w: value for key %q is %d bytes, over the limit of %d bytes per value",
				ErrInvalidInput, w.Key, len(w.Value), MapMaxValueBytes)
		}
		if _, dup := seen[w.Key]; dup {
			return nil, fmt.Errorf("%w: key %q appears twice in one batch", ErrInvalidInput, w.Key)
		}
		seen[w.Key] = struct{}{}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Bound the time this transaction can hold a pooled connection and a row
	// lock. A queued caller gets an error rather than waiting out chi's
	// request timeout.
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '3s'"); err != nil {
		return nil, fmt.Errorf("pgstore: set lock_timeout: %w", err)
	}
	// Per-slug, so the cap check below is sound: under READ COMMITTED a SUM
	// cannot see a concurrent writer's uncommitted rows.
	if err := acquireSlugLock(ctx, tx, slug); err != nil {
		return nil, err
	}
	qtx := s.q.WithTx(tx)

	// A map's entries are its live head, so a write here changes what the
	// document serves without ever going through Put. The block therefore has
	// to be refused on this path too, from the table rather than the snapshot,
	// for the same reason Put re-checks inside its own lock. Unconditional:
	// a caller that passes no access check is still not allowed to write to a
	// blocked slug.
	blocked, err := blockedSlugTx(ctx, tx, slug)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, ErrNotFound
	}

	// Re-check INSIDE the lock, against the fresh row. The caller authorized
	// against a read taken before it queued here, so without this a writer
	// whose access was revoked — or whose document was archived — while it
	// waited still writes.
	if check != nil {
		fresh, err := qtx.GetLatestArtifactBySlug(ctx, &slug)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if err := check(ctx, fresh); err != nil {
			return nil, err
		}
	}

	results := make([]MapWriteResult, 0, len(writes))
	for _, w := range writes {
		res := MapWriteResult{Key: w.Key}
		params := func() (pgtype.UUID, string, []byte, int32, string) {
			return mapID, w.Key, []byte(w.Value), int32(len(w.Value)), writer
		}
		mid, k, v, sz, by := params()
		var (
			row    sqlc.ArtifactMapEntry
			putErr error
		)
		switch {
		case w.IfAbsent:
			row, putErr = qtx.PutMapEntryIfAbsent(ctx, sqlc.PutMapEntryIfAbsentParams{
				MapID: mid, Key: k, Value: v, SizeBytes: sz, UpdatedBy: by})
		case w.IfRev != nil:
			row, putErr = qtx.PutMapEntryIfRev(ctx, sqlc.PutMapEntryIfRevParams{
				MapID: mid, Key: k, Value: v, SizeBytes: sz, UpdatedBy: by, IfRev: *w.IfRev})
		default:
			row, putErr = qtx.PutMapEntry(ctx, sqlc.PutMapEntryParams{
				MapID: mid, Key: k, Value: v, SizeBytes: sz, UpdatedBy: by})
		}
		switch {
		case putErr == nil:
			e := row
			res.Entry = &e
		case errors.Is(putErr, pgx.ErrNoRows):
			// Report the winner so a claim-lease needs one round trip. For
			// if_rev the incumbent may be nil: the key may simply be gone.
			res.Conflict = true
			if inc, err := qtx.GetMapEntry(ctx, sqlc.GetMapEntryParams{MapID: mid, Key: k}); err == nil {
				res.Incumbent = &inc
			}
		default:
			return nil, fmt.Errorf("pgstore: map put %q: %w", w.Key, putErr)
		}
		results = append(results, res)
	}

	// Limits are checked AFTER the writes and inside the same transaction, so
	// the count includes this batch and a breach rolls the whole batch back.
	// Checking before would race two concurrent batches past the ceiling.
	stats, err := qtx.CountMapEntries(ctx, mapID)
	if err != nil {
		return nil, fmt.Errorf("pgstore: map stats: %w", err)
	}
	if stats.N > MapMaxKeysPerSlug {
		return nil, fmt.Errorf("%w: slug %q would hold %d keys, over the limit of %d keys per map",
			ErrInvalidInput, slug, stats.N, MapMaxKeysPerSlug)
	}
	if stats.Bytes > MapMaxBytesPerSlug {
		return nil, fmt.Errorf("%w: slug %q would hold %d value bytes, over the limit of %d bytes per map",
			ErrInvalidInput, slug, stats.Bytes, MapMaxBytesPerSlug)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("pgstore: commit: %w", err)
	}
	return results, nil
}

// MapDelete removes keys. Deleting an absent key is a success with a count of
// zero, not a not-found.
func (s *Store) MapDelete(ctx context.Context, slug string, mapID pgtype.UUID, keys []string, check MapAccessCheck) (int64, error) {
	if len(keys) == 0 {
		return 0, fmt.Errorf("%w: map delete requires at least one key", ErrInvalidInput)
	}
	if len(keys) > MapMaxBatch {
		return 0, fmt.Errorf("%w: batch of %d keys exceeds the limit of %d keys per batch",
			ErrInvalidInput, len(keys), MapMaxBatch)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("pgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Same lock as MapPut, and for the same reason: UpdateAccessBySlug and
	// TransferSlugOwner take it too, so without it a revoke can commit between
	// the re-check below and the delete, and the keys still go.
	if err := acquireSlugLock(ctx, tx, slug); err != nil {
		return 0, err
	}
	qtx := s.q.WithTx(tx)
	// Same block refusal as MapPut: a delete is a write.
	blocked, err := blockedSlugTx(ctx, tx, slug)
	if err != nil {
		return 0, err
	}
	if blocked {
		return 0, ErrNotFound
	}
	// Same re-check as MapPut: a delete is a write.
	if check != nil {
		fresh, err := qtx.GetLatestArtifactBySlug(ctx, &slug)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, err
		}
		if err := check(ctx, fresh); err != nil {
			return 0, err
		}
	}
	n, err := qtx.DeleteMapEntries(ctx, sqlc.DeleteMapEntriesParams{MapID: mapID, Keys: keys})
	if err != nil {
		return 0, fmt.Errorf("pgstore: map delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("pgstore: commit: %w", err)
	}
	return n, nil
}

// MapSnapshotBody serialises the whole head as NDJSON in key order. Fields are
// written in a fixed order so two versions of the same map diff line by line.
// Rows are paged rather than read at once, so the peak allocation is a page,
// not the map.
func (s *Store) MapSnapshotBody(ctx context.Context, mapID pgtype.UUID) ([]byte, int, error) {
	// One read-only REPEATABLE READ transaction for the whole scan. Paging
	// through separate statements gives each page its own snapshot, so a
	// concurrent write lands between pages and the result matches no actual
	// head: keys added ahead of the cursor are missed, and revisions from
	// different instants are mixed into one "snapshot". MVCC gives a stable
	// view here without blocking writers, which a slug lock would not.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("pgstore: map snapshot begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	var (
		buf   strings.Builder
		after string
		count int
	)
	for {
		rows, err := qtx.ListMapEntries(ctx, sqlc.ListMapEntriesParams{
			MapID: mapID, Prefix: "", AfterKey: after, PrefixEnd: nil,
			Lim: mapSnapshotPage,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("pgstore: map snapshot page: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			line, err := json.Marshal(mapSnapshotLine{
				Key:       r.Key,
				Value:     json.RawMessage(r.Value),
				Rev:       r.Rev,
				UpdatedAt: r.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05.000Z"),
				UpdatedBy: r.UpdatedBy,
			})
			if err != nil {
				return nil, 0, fmt.Errorf("pgstore: map snapshot marshal %q: %w", r.Key, err)
			}
			buf.Write(line)
			buf.WriteByte('\n')
			count++
		}
		// No byte trimming on this path, so a short page IS the last page.
		// The whole map is capped at MapMaxBytesPerSlug, which bounds a page
		// regardless of its row count.
		if len(rows) < mapSnapshotPage {
			break
		}
		after = rows[len(rows)-1].Key
	}
	return []byte(buf.String()), count, nil
}

// mapSnapshotPage is the scan chunk for a snapshot. Smaller than the browse
// page because every value is carried whole here, not previewed.
const mapSnapshotPage = 500

// mapSnapshotLine fixes the field order of a snapshot line. Struct field order
// is the JSON key order, so this type IS the format contract.
type mapSnapshotLine struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Rev       int64           `json:"rev"`
	UpdatedAt string          `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
}

// MapBrowse backs the viewer's table. It is deliberately separate from
// MapList, which serves MCP and the CLI and must keep returning whole values:
// a table wants a page it can render, so this one truncates each value to a
// preview and reports the real size alongside.
type MapBrowseInput struct {
	MapID pgtype.UUID
	// Q matches a substring of the key OR of the value's text. `*` is a
	// wildcard, escaped the same way the catalog's search escapes it.
	Q      string
	Sort   string // key | rev | updated | size; anything else falls back to key
	Dir    string // asc | desc; anything else falls back to asc
	Limit  int
	Offset int
}

// MapBrowseRow is one table row. Value carries at most MapPreviewBytes of the
// stored value; Truncated says the caller must fetch the entry to see it all.
type MapBrowseRow struct {
	Key       string
	Value     string
	Truncated bool
	SizeBytes int32
	Rev       int64
	UpdatedAt time.Time
	UpdatedBy string
}

// MapPreviewBytes bounds a browse page: 200 rows of whole 64 KiB values would
// be 12 MiB on a pod limited to 1024 MiB, and a table cannot render that
// anyway. The full value is one get-by-key away.
const MapPreviewBytes = 1024

var mapSortColumns = map[string]string{
	"key":     "key",
	"rev":     "rev",
	"updated": "updated_at",
	"size":    "size_bytes",
}

// MapBrowse returns one page plus the total number of entries matching Q, so
// the caller can render a pager without a second query.
func (s *Store) MapBrowse(ctx context.Context, in MapBrowseInput) ([]MapBrowseRow, int64, error) {
	col, ok := mapSortColumns[in.Sort]
	if !ok {
		col = "key"
	}
	dir := "ASC"
	if strings.EqualFold(in.Dir, "desc") {
		dir = "DESC"
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 200 {
		in.Limit = 200
	}
	if in.Offset < 0 {
		in.Offset = 0
	}

	// Only ever a whitelisted column and a fixed direction reach the SQL; the
	// user's own text goes in as a parameter. Secondary sort on key keeps a
	// page stable when the primary column ties.
	q := fmt.Sprintf(`
		SELECT key,
		       left(value::text, %d) AS preview,
		       octet_length(value::text) > %d AS truncated,
		       size_bytes, rev, updated_at, updated_by,
		       count(*) OVER () AS total
		FROM artifact_map_entry
		WHERE map_id = $1
		  AND ($2 = '' OR key ILIKE $2 ESCAPE '\' OR value::text ILIKE $2 ESCAPE '\')
		ORDER BY %s %s, key ASC
		LIMIT $3 OFFSET $4`, MapPreviewBytes, MapPreviewBytes, col, dir)

	like := ""
	if in.Q != "" {
		like = "%" + globToLike(in.Q) + "%"
	}
	rows, err := s.pool.Query(ctx, q, in.MapID, like, in.Limit, in.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("pgstore: map browse: %w", err)
	}
	defer rows.Close()

	out := []MapBrowseRow{}
	var total int64
	for rows.Next() {
		var r MapBrowseRow
		if err := rows.Scan(&r.Key, &r.Value, &r.Truncated, &r.SizeBytes, &r.Rev, &r.UpdatedAt, &r.UpdatedBy, &total); err != nil {
			return nil, 0, fmt.Errorf("pgstore: map browse scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// total rides on the returned rows, so an EMPTY page carries no total at
	// all. Past the last page that is not "nothing matches", it is "you are
	// past the end" — and reporting 0 tells a reader with a stale offset that
	// the map is empty. Count separately for exactly that case; a page with
	// rows already has the answer, and offset 0 with no rows really is 0.
	if len(out) == 0 && in.Offset > 0 {
		if err := s.pool.QueryRow(ctx, `
			SELECT count(*) FROM artifact_map_entry
			WHERE map_id = $1
			  AND ($2 = '' OR key ILIKE $2 ESCAPE '\' OR value::text ILIKE $2 ESCAPE '\')`,
			in.MapID, like).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("pgstore: map browse count: %w", err)
		}
	}
	return out, total, nil
}
