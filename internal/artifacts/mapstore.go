package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// MapEntryDTO is one entry as the API reports it.
type MapEntryDTO struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Rev       int64           `json:"rev"`
	UpdatedAt string          `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
}

func mapEntryDTO(e sqlc.ArtifactMapEntry) MapEntryDTO {
	return MapEntryDTO{
		Key:       e.Key,
		Value:     json.RawMessage(e.Value),
		Rev:       e.Rev,
		UpdatedAt: e.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05.000Z"),
		UpdatedBy: e.UpdatedBy,
	}
}

// MapPutEntry is one entry in a put request. At most one guard may be set.
type MapPutEntry struct {
	Key      string          `json:"key"`
	Value    json.RawMessage `json:"value"`
	IfAbsent bool            `json:"if_absent,omitempty"`
	IfRev    *int64          `json:"if_rev,omitempty"`
}

// MapPutRequest is the body of a put. A single-entry put and a batch are the
// same shape so a caller never has to switch endpoints to add a second key.
// Title/Description/Labels/Scopes are read ONLY when the slug does not exist
// yet and this call is therefore creating the MAP, mirroring how append
// auto-creates v1 (see pgstore.Append).
type MapPutRequest struct {
	Entries     []MapPutEntry `json:"entries"`
	Title       string        `json:"title,omitempty"`
	Description *string       `json:"description,omitempty"`
	Labels      []string      `json:"labels,omitempty"`
	Scopes      []string      `json:"scopes,omitempty"`
	// AllowedAccess / AllowedWrite set the new MAP's ACL at creation. A
	// pointer so that "omitted" and "explicitly empty" (creator-only) are
	// different requests. Without these a caller could only restrict a map
	// AFTER creating it, and the entries written in between would be
	// readable by everyone for that window.
	AllowedAccess *[]string `json:"allowed_access,omitempty"`
	AllowedWrite  *[]string `json:"allowed_write,omitempty"`
}

// MapWriteResultDTO reports one entry's outcome. Conflict marks a guard that
// lost, and Incumbent then carries the row that won, so a claim-lease caller
// learns who holds the claim without a second round trip.
type MapWriteResultDTO struct {
	Key       string       `json:"key"`
	Written   bool         `json:"written"`
	Conflict  bool         `json:"conflict,omitempty"`
	Entry     *MapEntryDTO `json:"entry,omitempty"`
	Incumbent *MapEntryDTO `json:"incumbent,omitempty"`
}

type MapPutResponse struct {
	Results []MapWriteResultDTO `json:"results"`
	Stats   MapStatsDTO         `json:"stats"`
}

type MapStatsDTO struct {
	Keys     int64 `json:"keys"`
	Bytes    int64 `json:"bytes"`
	MaxKeys  int64 `json:"max_keys"`
	MaxBytes int64 `json:"max_bytes"`
}

type MapListResponse struct {
	Entries []MapEntryDTO `json:"entries"`
	Cursor  string        `json:"cursor,omitempty"`
	Stats   MapStatsDTO   `json:"stats"`
}

// decodeMapKey resolves a key from a URL path segment. chi hands back the RAW
// segment, so a client that percent-encodes (encodeURIComponent escapes ':',
// the prefix separator) would otherwise be validated as "seen%3Ax" and
// refused. Decoding never widens the charset: an encoded '/' decodes to '/'
// and is still rejected, which is what keeps a key inside one segment.
func decodeMapKey(raw string) string {
	if dec, err := url.PathUnescape(raw); err == nil {
		return dec
	}
	return raw
}

// mapSlugFor loads a MAP slug's latest version and applies the read gate.
// Every map route starts here, so the access decision is made in exactly one
// place and a non-MAP slug can never be driven through a map route.
func (s *Service) mapSlugFor(ctx context.Context, slug, caller string) (sqlc.Artifact, error) {
	if !s.mapEnabled {
		return sqlc.Artifact{}, errBadRequest("MAP artifacts are not enabled on this server")
	}
	row, err := s.store.GetBySlug(ctx, slug, nil)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return sqlc.Artifact{}, err
	}
	if row.ArtifactType != pgstore.TypeMap {
		return sqlc.Artifact{}, errBadRequest(fmt.Sprintf(
			"slug %q is a %s, not a MAP", slug, row.ArtifactType))
	}
	return row, nil
}

// mapSlugForWrite adds the write gate. A caller who can read the slug but not
// write it gets 403 rather than 404: it already knows the artifact exists, so
// hiding it says nothing and reads as a bug. A caller with no read access
// never reaches here — mapSlugFor already returned not-found.
func (s *Service) mapSlugForWrite(ctx context.Context, slug, caller string) (sqlc.Artifact, error) {
	row, err := s.mapSlugFor(ctx, slug, caller)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	if err := s.checkWriteAccess(ctx, row, caller); err != nil {
		if errors.Is(err, pgstore.ErrNotFound) {
			return sqlc.Artifact{}, errForbidden(fmt.Sprintf("no write access to slug %q", slug))
		}
		return sqlc.Artifact{}, err
	}
	return row, nil
}

// mapVersionSpec is the part that legitimately differs between the two
// writers of a MAP version. Everything NOT here is an invariant, and lives in
// putMapVersion so neither writer can get it wrong on its own.
type mapVersionSpec struct {
	// MapID is the map this version belongs to: minted for a new map,
	// carried from the version being snapshotted otherwise.
	MapID       pgtype.UUID
	Title       string
	Description *string
	Labels      []string
	Scopes      []string
	Content     []byte
	// Access / Write are the pair to write. InheritAccess / InheritWrite mark
	// that pair as a fallback resolved from an EARLIER read, so a live prior
	// version's pair wins at insert time. Creation resolves it from the
	// request, a snapshot from the version it just read; both are stale by the
	// time Put runs, which is what the flags are for.
	Access        []string
	Write         []string
	InheritAccess bool
	InheritWrite  bool
}

// putMapVersion writes a new version of a MAP slug. It owns every invariant a
// MAP version must satisfy, because each one of them has been a bug at least
// once when the two callers each carried their own copy: the type and content
// type, and the CheckAccess hook that re-runs against the FRESH row inside
// Put's slug critical section and refuses both a caller who may not write and
// a slug that is no longer a MAP.
//
// Deliberately NOT here: creation's probe for an invisible slug and its
// stale-entry check. Those are preconditions for creating, not invariants of
// a version, and folding them in behind a flag is how this collapses back
// into one function with two behaviours.
func (s *Service) putMapVersion(ctx context.Context, slug, caller string, spec mapVersionSpec) (sqlc.Artifact, error) {
	writeAuth, err := s.resolveWriteAuthority(ctx, caller, slug)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	return s.store.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeMap,
		ContentType:   pgstore.MapContentType,
		NamedSlug:     &slug,
		Creator:       caller,
		Title:         spec.Title,
		Description:   spec.Description,
		Labels:        spec.Labels,
		Scopes:        spec.Scopes,
		Content:       spec.Content,
		AllowedAccess: spec.Access,
		AllowedWrite:  spec.Write,
		InheritAccess: spec.InheritAccess,
		InheritWrite:  spec.InheritWrite,
		MapID:         spec.MapID,
		CheckAccess:   s.mapWriteCheck(slug, spec.MapID, caller, writeAuth),
	})
}

// mapWriteCheck is the authorization the store re-runs inside its writing
// transaction, against the fresh row. Same two conditions the routes gate on:
// checking here and writing later leaves a window in which access is revoked
// or the document archived and the write still lands.
// The authority is resolved by the caller, before the store opens its writing
// transaction: the check below runs while that transaction holds a pool
// connection, so a lookup here would need a second one (see writeAuthority).
func (s *Service) mapWriteCheck(slug string, mapID pgtype.UUID, caller string, auth writeAuthority) pgstore.MapAccessCheck {
	return func(ctx context.Context, fresh sqlc.Artifact) error {
		if fresh.ArtifactType != pgstore.TypeMap {
			return errBadRequest(fmt.Sprintf("slug %q is a %s, not a MAP", slug, fresh.ArtifactType))
		}
		// The slug resolves to a different map than the one this write was
		// authorized against, so the map it names is gone.
		if fresh.MapID != mapID {
			return pgstore.ErrNotFound
		}
		return checkWriteAccessWith(fresh, caller, auth)
	}
}

func (s *Service) mapStats(ctx context.Context, mapID pgtype.UUID) MapStatsDTO {
	st, err := s.store.MapStats(ctx, mapID)
	if err != nil {
		return MapStatsDTO{MaxKeys: pgstore.MapMaxKeysPerSlug, MaxBytes: pgstore.MapMaxBytesPerSlug}
	}
	return MapStatsDTO{
		Keys: st.Keys, Bytes: st.Bytes,
		MaxKeys: pgstore.MapMaxKeysPerSlug, MaxBytes: pgstore.MapMaxBytesPerSlug,
	}
}

// httpMapGet handles GET /api/artifacts/by-slug/{slug}/map/keys/{key}.
func (s *Service) httpMapGet(w http.ResponseWriter, r *http.Request) {
	e, err := s.MapGet(r.Context(), chi.URLParam(r, "slug"), chi.URLParam(r, "key"), auth.EmailFromContext(r.Context()))
	if err != nil {
		status, code, msg := WriteStatus(err)
		writeError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// MapBrowseRowDTO is one row of the viewer's table.
type MapBrowseRowDTO struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Truncated bool   `json:"truncated"`
	SizeBytes int32  `json:"size_bytes"`
	Rev       int64  `json:"rev"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

type MapBrowseResponse struct {
	Rows    []MapBrowseRowDTO `json:"rows"`
	Total   int64             `json:"total"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
	Sort    string            `json:"sort"`
	Dir     string            `json:"dir"`
	Q       string            `json:"q"`
	Stats   MapStatsDTO       `json:"stats"`
	Preview int               `json:"preview_bytes"`
}

// MapBrowse pages, searches and sorts head for the viewer. Separate from
// MapList, which MCP and the CLI use and which must keep whole values.
func (s *Service) MapBrowse(ctx context.Context, slug string, in pgstore.MapBrowseInput, caller string) (MapBrowseResponse, error) {
	row, err := s.mapSlugFor(ctx, slug, caller)
	if err != nil {
		return MapBrowseResponse{}, err
	}
	in.MapID = row.MapID
	rows, total, err := s.store.MapBrowse(ctx, in)
	if err != nil {
		return MapBrowseResponse{}, err
	}
	out := MapBrowseResponse{
		Rows: make([]MapBrowseRowDTO, 0, len(rows)), Total: total,
		Limit: in.Limit, Offset: in.Offset, Sort: in.Sort, Dir: in.Dir, Q: in.Q,
		Stats: s.mapStats(ctx, in.MapID), Preview: pgstore.MapPreviewBytes,
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, MapBrowseRowDTO{
			Key: r.Key, Value: r.Value, Truncated: r.Truncated, SizeBytes: r.SizeBytes,
			Rev: r.Rev, UpdatedAt: r.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			UpdatedBy: r.UpdatedBy,
		})
	}
	return out, nil
}

// httpMapBrowse handles GET /api/artifacts/by-slug/{slug}/map/browse.
func (s *Service) httpMapBrowse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	atoi := func(k string, def int) int {
		if v := q.Get(k); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	}
	out, err := s.MapBrowse(r.Context(), chi.URLParam(r, "slug"), pgstore.MapBrowseInput{
		Q:      q.Get("q"),
		Sort:   q.Get("sort"),
		Dir:    q.Get("dir"),
		Limit:  atoi("limit", 50),
		Offset: atoi("offset", 0),
	}, auth.EmailFromContext(r.Context()))
	if err != nil {
		status, code, msg := WriteStatus(err)
		writeError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// httpMapList handles GET /api/artifacts/by-slug/{slug}/map.
func (s *Service) httpMapList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "bad-request", "limit must be a positive integer")
			return
		}
		limit = n
	}
	out, err := s.MapList(r.Context(), chi.URLParam(r, "slug"), q.Get("prefix"), q.Get("cursor"), limit, auth.EmailFromContext(r.Context()))
	if err != nil {
		status, code, msg := WriteStatus(err)
		writeError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// httpMapPutKey handles PUT /api/artifacts/by-slug/{slug}/map/keys/{key}. The
// body is the value; guards ride as query params so a value is never wrapped.
func (s *Service) httpMapPutKey(w http.ResponseWriter, r *http.Request) {
	// Deliberately generous: the per-value limit is enforced in the store so
	// the error can name it. A reader capped at exactly the limit would cut
	// the body off first and produce a decode error instead, which tells the
	// caller nothing about which ceiling it hit.
	r.Body = http.MaxBytesReader(w, r.Body, 4*pgstore.MapMaxValueBytes)
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "too-large",
				fmt.Sprintf("value exceeds the limit of %d bytes per value", pgstore.MapMaxValueBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "bad-request", "body must be a JSON value: "+err.Error())
		return
	}
	entry := MapPutEntry{Key: decodeMapKey(chi.URLParam(r, "key")), Value: raw}
	q := r.URL.Query()
	if q.Get("if_absent") == "true" {
		entry.IfAbsent = true
	}
	if v := q.Get("if_rev"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad-request", "if_rev must be an integer")
			return
		}
		entry.IfRev = &n
	}
	// A single-key PUT never creates the MAP: creation needs a title, which
	// this route has nowhere to carry. Use POST .../map to create.
	s.respondMapPut(w, r, chi.URLParam(r, "slug"), MapPutRequest{Entries: []MapPutEntry{entry}})
}

// httpMapPutBatch handles POST /api/artifacts/by-slug/{slug}/map. It is also
// the create path: when the slug does not exist it mints the MAP at v1 from
// the request's title, the same way append auto-creates v1 for a new slug.
// That keeps creation out of POST /api/artifacts, where nothing would
// validate keys or require a slug.
func (s *Service) httpMapPutBatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(pgstore.MapMaxBatch)*(pgstore.MapMaxValueBytes+1024))
	var body MapPutRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "decode body: "+err.Error())
		return
	}
	s.respondMapPut(w, r, chi.URLParam(r, "slug"), body)
}

// respondMapPut is the shared tail of both put routes. The status is 200 when
// every entry was written and 409 when any guard lost, so a single-entry
// caller can branch on the status code alone.
func (s *Service) respondMapPut(w http.ResponseWriter, r *http.Request, slug string, req MapPutRequest) {
	out, anyConflict, err := s.MapPut(r.Context(), slug, req, auth.EmailFromContext(r.Context()))
	if err != nil {
		status, code, msg := WriteStatus(err)
		writeError(w, status, code, msg)
		return
	}
	status := http.StatusOK
	if anyConflict {
		status = http.StatusConflict
	}
	writeJSON(w, status, out)
}

// createMap mints a MAP: a fresh map id and an empty NDJSON body, so the
// version chain starts at a real artifact even before the first snapshot.
//
// Archive, reclaim and retype need no rule here. Each one of them used to,
// because the head was keyed by the slug and so survived the document: a
// reclaimed slug had to be gated to its owner, and its access list carried
// forward from the archived version, or the new owner would publish someone
// else's keys. A new map is a new id, so a reclaimed slug is empty and takes
// the default ACL like any other new document.
func (s *Service) createMap(ctx context.Context, slug, caller string, body MapPutRequest) (sqlc.Artifact, error) {
	if !s.mapEnabled {
		return sqlc.Artifact{}, errBadRequest("MAP artifacts are not enabled on this server")
	}
	if body.Title == "" {
		return sqlc.Artifact{}, errBadRequest("creating a MAP requires a title")
	}
	// not-found means "absent" OR "unreadable", so probe without the access
	// filter; answering anything but the same not-found leaks existence.
	if _, err := s.store.GetBySlug(ctx, slug, nil); err == nil {
		return sqlc.Artifact{}, pgstore.ErrNotFound
	} else if !errors.Is(err, pgstore.ErrNotFound) {
		return sqlc.Artifact{}, err
	}
	var access, write []string
	if body.AllowedAccess != nil {
		access = *body.AllowedAccess
	}
	if body.AllowedWrite != nil {
		write = *body.AllowedWrite
	}
	// No inherit flags: a live prior version appearing between the probe and
	// the insert is a DIFFERENT map, and putMapVersion's check refuses the
	// write outright rather than letting this one land under its ACL.
	return s.putMapVersion(ctx, slug, caller, mapVersionSpec{
		MapID:       pgstore.NewMapID(),
		Title:       body.Title,
		Description: body.Description,
		Labels:      body.Labels,
		Scopes:      body.Scopes,
		Content:     []byte{},
		Access:      access,
		Write:       write,
	})
}

// httpMapSnapshot handles POST /api/artifacts/by-slug/{slug}/map/snapshot.
func (s *Service) httpMapSnapshot(w http.ResponseWriter, r *http.Request) {
	res, err := s.MapSnapshot(r.Context(), chi.URLParam(r, "slug"), auth.EmailFromContext(r.Context()))
	if err != nil {
		status, code, msg := WriteStatus(err)
		writeError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// httpMapDelete handles DELETE /api/artifacts/by-slug/{slug}/map/{key}.
func (s *Service) httpMapDelete(w http.ResponseWriter, r *http.Request) {
	n, stats, err := s.MapDelete(r.Context(), chi.URLParam(r, "slug"), []string{chi.URLParam(r, "key")}, auth.EmailFromContext(r.Context()))
	if err != nil {
		status, code, msg := WriteStatus(err)
		writeError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n, "stats": stats})
}

// ---- Service methods. The HTTP handlers above and the MCP tools both go
// through these, so the access gate and the limit checks exist once.

// MapGet reads one entry.
func (s *Service) MapGet(ctx context.Context, slug, key, caller string) (MapEntryDTO, error) {
	row, err := s.mapSlugFor(ctx, slug, caller)
	if err != nil {
		return MapEntryDTO{}, err
	}
	key = decodeMapKey(key)
	if !pgstore.MapKeyValid(key) {
		return MapEntryDTO{}, errBadRequest("illegal map key")
	}
	e, err := s.store.MapGet(ctx, row.MapID, key)
	if err != nil {
		return MapEntryDTO{}, err
	}
	return mapEntryDTO(e), nil
}

// MapList reads one page of entries under prefix.
func (s *Service) MapList(ctx context.Context, slug, prefix, cursor string, limit int, caller string) (MapListResponse, error) {
	row, err := s.mapSlugFor(ctx, slug, caller)
	if err != nil {
		return MapListResponse{}, err
	}
	if prefix != "" && !pgstore.MapKeyValid(prefix) {
		return MapListResponse{}, errBadRequest("illegal map key prefix")
	}
	rows, next, err := s.store.MapList(ctx, row.MapID, prefix, cursor, limit)
	if err != nil {
		return MapListResponse{}, err
	}
	out := MapListResponse{Entries: make([]MapEntryDTO, 0, len(rows)), Cursor: next, Stats: s.mapStats(ctx, row.MapID)}
	for _, e := range rows {
		out.Entries = append(out.Entries, mapEntryDTO(e))
	}
	return out, nil
}

// MapPut applies a batch, creating the MAP first when the slug is absent and
// the request carries a title. anyConflict reports whether any guard lost, so
// a transport can map it to 409 without walking the results.
func (s *Service) MapPut(ctx context.Context, slug string, req MapPutRequest, caller string) (MapPutResponse, bool, error) {
	row, err := s.mapSlugForWrite(ctx, slug, caller)
	if err == nil && (req.AllowedAccess != nil || req.AllowedWrite != nil) {
		// Honoured only when this call CREATES the map. Accepting and ignoring
		// it lets a caller believe they locked a map that is still open.
		// Access is per-document and changing it is the access API's job, so
		// say that rather than silently doing nothing.
		return MapPutResponse{}, false, errBadRequest(
			"allowed_access/allowed_write apply only when this call creates the map; " +
				"slug " + slug + " already exists — change its access with the access API")
	}
	if errors.Is(err, pgstore.ErrNotFound) {
		if row, err = s.createMap(ctx, slug, caller, req); err != nil {
			return MapPutResponse{}, false, err
		}
	} else if err != nil {
		return MapPutResponse{}, false, err
	}
	out := MapPutResponse{Results: []MapWriteResultDTO{}}
	if len(req.Entries) == 0 {
		out.Stats = s.mapStats(ctx, row.MapID)
		return out, false, nil
	}
	writes := make([]pgstore.MapEntryWrite, 0, len(req.Entries))
	for _, e := range req.Entries {
		writes = append(writes, pgstore.MapEntryWrite{
			Key: e.Key, Value: e.Value, IfAbsent: e.IfAbsent, IfRev: e.IfRev,
		})
	}
	writeAuth, err := s.resolveWriteAuthority(ctx, caller, slug)
	if err != nil {
		return MapPutResponse{}, false, err
	}
	results, err := s.store.MapPut(ctx, slug, row.MapID, writes, caller, s.mapWriteCheck(slug, row.MapID, caller, writeAuth))
	if err != nil {
		return MapPutResponse{}, false, err
	}
	anyConflict := false
	for _, res := range results {
		dto := MapWriteResultDTO{Key: res.Key, Written: res.Entry != nil, Conflict: res.Conflict}
		if res.Entry != nil {
			e := mapEntryDTO(*res.Entry)
			dto.Entry = &e
		}
		if res.Incumbent != nil {
			e := mapEntryDTO(*res.Incumbent)
			dto.Incumbent = &e
		}
		if res.Conflict {
			anyConflict = true
		}
		out.Results = append(out.Results, dto)
	}
	out.Stats = s.mapStats(ctx, row.MapID)
	return out, anyConflict, nil
}

// MapDelete removes keys. Deleting an absent key is a success with count 0.
// The stats ride back because the caller already resolved the map here, and a
// second lookup to report occupancy would re-run the access check.
func (s *Service) MapDelete(ctx context.Context, slug string, keys []string, caller string) (int64, MapStatsDTO, error) {
	row, err := s.mapSlugForWrite(ctx, slug, caller)
	if err != nil {
		return 0, MapStatsDTO{}, err
	}
	dec := make([]string, 0, len(keys))
	for _, k := range keys {
		dec = append(dec, decodeMapKey(k))
	}
	writeAuth, err := s.resolveWriteAuthority(ctx, caller, slug)
	if err != nil {
		return 0, MapStatsDTO{}, err
	}
	n, err := s.store.MapDelete(ctx, slug, row.MapID, dec, s.mapWriteCheck(slug, row.MapID, caller, writeAuth))
	if err != nil {
		return 0, MapStatsDTO{}, err
	}
	return n, s.mapStats(ctx, row.MapID), nil
}

// MapSnapshotResult reports what a snapshot did. Unchanged marks the no-op
// path, where head already matches the latest version.
type MapSnapshotResult struct {
	Unchanged  bool   `json:"unchanged"`
	Version    *int32 `json:"version"`
	Entries    int    `json:"entries"`
	ArtifactID string `json:"artifact_id"`
	SizeBytes  int    `json:"size_bytes"`
}

// MapSnapshot freezes head as an NDJSON version.
func (s *Service) MapSnapshot(ctx context.Context, slug, caller string) (MapSnapshotResult, error) {
	prev, err := s.mapSlugForWrite(ctx, slug, caller)
	if err != nil {
		return MapSnapshotResult{}, err
	}
	body, count, err := s.store.MapSnapshotBody(ctx, prev.MapID)
	if err != nil {
		return MapSnapshotResult{}, err
	}
	// The scan is the long part, and an archive landing inside it would leave
	// Put with no live prior — which skips CheckAccess entirely and inserts a
	// new LIVE version, un-archiving the document. Re-reading here narrows
	// that window from the whole scan to the microseconds after it. It does
	// NOT close it: see bl-arti-map-snapshot-resurrects-archive.
	cur, err := s.mapSlugForWrite(ctx, slug, caller)
	if err != nil {
		return MapSnapshotResult{}, err
	}
	if cur.MapID != prev.MapID {
		return MapSnapshotResult{}, pgstore.ErrNotFound
	}
	sum := sha256.Sum256(body)
	if prev.SHA256 != nil && *prev.SHA256 == hex.EncodeToString(sum[:]) {
		return MapSnapshotResult{
			Unchanged: true, Version: prev.Version, Entries: count,
			ArtifactID: prev.ArtifactID.String(), SizeBytes: len(body),
		}, nil
	}
	row, err := s.putMapVersion(ctx, slug, caller, mapVersionSpec{
		MapID: prev.MapID,
		Title: prev.Title,
		// Per-version, so omitting it CLEARS it.
		Description: prev.Description,
		Labels:      prev.Labels,
		Scopes:      prev.Scopes,
		Content:     body,
		// Read before the scan, so it is a fallback: Put inherits only from a
		// LIVE prior, and an archive landing inside the scan window would
		// otherwise default this version to everyone.
		Access:        prev.AllowedAccess,
		Write:         prev.AllowedWrite,
		InheritAccess: true,
		InheritWrite:  true,
	})
	if err != nil {
		return MapSnapshotResult{}, err
	}
	return MapSnapshotResult{
		Unchanged: false, Version: row.Version, Entries: count,
		ArtifactID: row.ArtifactID.String(), SizeBytes: len(body),
	}, nil
}
