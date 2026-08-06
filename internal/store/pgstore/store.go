// Package pgstore is the read/write surface for artifacts. It composes
// the sqlc-generated Postgres queries with a blob.Store and applies the
// inline-vs-S3 policy: TEXT artifacts ≤ 64 KiB go inline; everything
// else (including all PACKAGE artifacts) lives in S3 with a blob_ref.
package pgstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/blob"
)

// SortKeys lists the API-facing sort keys.
var SortKeys = []string{"title", "type", "slug", "version", "creator", "scope", "created", "archived"}

// IsSortKey reports whether `s` is one of the SortKeys.
func IsSortKey(s string) bool {
	_, ok := sortableColumns[s]
	return ok
}

const (
	// InlineMaxBytes is the largest TEXT artifact that may live inline in
	// Postgres. Above this, content goes to S3.
	InlineMaxBytes = 64 * 1024

	TypeText    = "TEXT"
	TypePackage = "PACKAGE"
	// TypeAttachment is a user-uploaded file (chat attachment etc.). Always
	// stored in S3 (never inline), never slugged/versioned, creator-only by
	// default, and hidden from the catalog for non-admins.
	TypeAttachment = "ATTACHMENT"
	// TypeApp is an interactive single-page app: a PACKAGE-shaped zip
	// (entry_point + files) that additionally declares an arti-app.json
	// manifest (a tool allowlist) and gets the app bridge injected at serve
	// time so its JS can call MCP tools through arti's governed proxy.
	// Stored exactly like a PACKAGE.
	TypeApp = "APP"
)

// IsPackageLike reports whether an artifact type is stored and served as a zip
// package — PACKAGE or APP (an APP is a PACKAGE that also declares a tool
// manifest + gets the bridge injected). Use this instead of a bare
// `== TypePackage` for any package-behavior branch (serving files, listing the
// manifest, rejecting append, …) so APP never silently diverges from PACKAGE.
func IsPackageLike(artifactType string) bool {
	return artifactType == TypePackage || artifactType == TypeApp
}

var (
	ErrNotFound = errors.New("pgstore: not found")
	ErrConflict = errors.New("pgstore: conflict")
	// ErrInvalidInput marks callable-from-handler "client mistake" failures
	// (missing required field, semantic violations like appending to a
	// PACKAGE/ATTACHMENT) so the HTTP layer can return 400 instead of 500.
	// Wrap with %w to keep the per-call message attached.
	ErrInvalidInput = errors.New("pgstore: invalid input")
)

// Config tunes the pgstore. Default prefixes work fine for the common case.
type Config struct {
	BucketPrefixText       string // default "artifacts"
	BucketPrefixPackage    string // default "packages"
	BucketPrefixAttachment string // default "attachments"
	// IdPGroupsMaxAge bounds how stale a captured IdP-group snapshot may be
	// before its `idp:` tokens stop resolving (fail closed). Zero or negative
	// disables `idp:` resolution entirely — the safe default when the deployment
	// isn't capturing IdP groups.
	IdPGroupsMaxAge time.Duration
}

func (c *Config) defaults() {
	if c.BucketPrefixText == "" {
		c.BucketPrefixText = "artifacts"
	}
	if c.BucketPrefixPackage == "" {
		c.BucketPrefixPackage = "packages"
	}
	if c.BucketPrefixAttachment == "" {
		c.BucketPrefixAttachment = "attachments"
	}
}

// Store stitches Postgres + blob into the public CRUD API used by handlers.
type Store struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
	blob blob.Store
	cfg  Config

	// groups caches the (small) user_groups table in memory so per-request
	// access checks resolve a caller's group membership without a DB hit.
	// Zero value is ready to use (see groups.go). Invalidated on every group
	// mutation and cross-replica notification; it is served only while the
	// invalidation bus is healthy and also expires on a short TTL.
	groups groupCache

	// rbac caches the (small) roles + role_assignments tables so per-request
	// permission checks (HasPermission) avoid DB hits. Same cache discipline as
	// groups. Zero value is ready to use (see roles.go); without a healthy
	// invalidation bus, reads fail closed to the database.
	rbac rbacCache

	busMu sync.RWMutex
	bus   *invalidationBus

	busHealthy atomic.Bool
}

func New(pool *pgxpool.Pool, b blob.Store, cfg Config) *Store {
	cfg.defaults()
	return &Store{pool: pool, q: sqlc.New(pool), blob: b, cfg: cfg}
}

// PutInput is the input shape for creating or versioning an artifact.
// When NamedSlug is set, Version is always MAX(existing)+1 — callers
// cannot pin a version on upload.
type PutInput struct {
	ArtifactType  string // "TEXT" | "PACKAGE"
	NamedSlug     *string
	Title         string
	Description   *string
	ContentType   string // MIME
	Content       []byte // TEXT: body. PACKAGE: zip bytes.
	Creator       string
	Scopes        []string
	Labels        []string
	Metadata      json.RawMessage // optional, schema-less
	AllowedAccess []string        // glob-on-email patterns; nil → default '{*}'
	// AllowedWrite is the per-version write list. nil → SQL NULL (write
	// follows read — the back-compat default). Non-nil (incl. empty) is
	// authoritative; empty == creator-only. Unioned into AllowedAccess on
	// insert so it stays a subset (read paths untouched).
	AllowedWrite []string

	// CheckAccess, if set and NamedSlug is non-nil, is called against a
	// fresh read of the slug's current latest version — taken right here,
	// not whatever the caller looked up earlier — if one exists. Closes
	// the TOCTOU a caller-side pre-check would miss: the slug can come
	// into existence (as someone else's restricted artifact) in the
	// window between that check and this insert, since Put always
	// re-derives the next version number live rather than reusing
	// anything from an earlier read. Returning an error aborts the Put.
	CheckAccess func(ctx context.Context, prev sqlc.Artifact) error
}

// applyAttachmentInvariants forces the rules that make ATTACHMENT rows safe
// regardless of caller input or the server's slug-inherit path (which copies
// allowed_access from a prior version under the same slug): attachments are
// always slugless (so single-version, and no slug to inherit access from) and
// creator-only. No-op for other types.
func applyAttachmentInvariants(in PutInput) PutInput {
	if in.ArtifactType == TypeAttachment {
		in.NamedSlug = nil
		in.AllowedAccess = []string{} // empty (not nil) == creator-only
		in.AllowedWrite = []string{}  // creator-only writes too
	}
	return in
}

// Put creates a new artifact row. Returns the inserted row including the
// server-assigned UUID and version.
func (s *Store) Put(ctx context.Context, in PutInput) (sqlc.Artifact, error) {
	if in.ArtifactType != TypeText && in.ArtifactType != TypePackage && in.ArtifactType != TypeAttachment && in.ArtifactType != TypeApp {
		return sqlc.Artifact{}, fmt.Errorf("pgstore: unknown artifact_type %q", in.ArtifactType)
	}
	if in.Title == "" || in.ContentType == "" || in.Creator == "" {
		return sqlc.Artifact{}, fmt.Errorf("pgstore: title, content_type, creator are required")
	}

	if in.CheckAccess != nil && in.NamedSlug != nil {
		prev, err := s.GetBySlug(ctx, *in.NamedSlug, nil)
		switch {
		case err == nil:
			if err := in.CheckAccess(ctx, prev); err != nil {
				return sqlc.Artifact{}, err
			}
		case errors.Is(err, ErrNotFound):
			// No prior version to check access against — proceed as a
			// fresh create.
		default:
			return sqlc.Artifact{}, err
		}
	}

	// Enforce attachment invariants at the store — the single write
	// chokepoint — so no caller or slug-inherit path can violate them.
	in = applyAttachmentInvariants(in)

	id := uuid.New()
	sum := sha256.Sum256(in.Content)
	sumHex := hex.EncodeToString(sum[:])
	sz := int64(len(in.Content))

	var (
		inline  []byte
		blobRef *string
		shaPtr  = &sumHex
		szPtr   = &sz
	)
	switch {
	case in.ArtifactType == TypeText && sz <= InlineMaxBytes:
		inline = in.Content
		// inline rows still record sha+size for clients that care; check
		// constraint only enforces them when blob_ref is set, but we always
		// populate them for consistency.
	default:
		key := s.blobKey(in.ArtifactType, id)
		if _, err := s.blob.Put(ctx, key, bytes.NewReader(in.Content), blob.PutOpts{
			ContentType: in.ContentType,
		}); err != nil {
			return sqlc.Artifact{}, fmt.Errorf("pgstore: blob put: %w", err)
		}
		blobRef = &key
	}

	version, err := s.nextVersion(ctx, in.NamedSlug)
	if err != nil {
		return sqlc.Artifact{}, err
	}

	meta := in.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	if in.Labels == nil {
		in.Labels = []string{}
	}
	if in.Scopes == nil {
		in.Scopes = []string{}
	}
	// Dual-write the legacy scalar scope = scopes[0] so an old binary
	// reading this row during the phase-1 rollout still sees a scope.
	var legacyScope *string
	if len(in.Scopes) > 0 {
		sc := in.Scopes[0] // copy so the pointer doesn't alias the slice backing array
		legacyScope = &sc
	}
	// nil → default everyone-authenticated. Distinct from empty slice,
	// which is "creator-only on this row". Callers that want the
	// default explicitly should pass nil; those that want creator-only
	// pass []string{}.
	access := in.AllowedAccess
	if access == nil {
		access = []string{"*"}
	}
	// ⊆ invariant: a write grant always implies read, so union the write
	// tokens into access. nil AllowedWrite (mirror) leaves access untouched.
	if in.AllowedWrite != nil {
		access = unionTokens(access, in.AllowedWrite)
	}

	row, err := s.q.InsertArtifact(ctx, sqlc.InsertArtifactParams{
		ArtifactID:    pgUUID(id),
		ArtifactType:  in.ArtifactType,
		NamedSlug:     in.NamedSlug,
		Version:       version,
		Title:         in.Title,
		Description:   in.Description,
		ContentType:   in.ContentType,
		InlineContent: inline,
		BlobRef:       blobRef,
		SHA256:        shaPtr,
		SizeBytes:     szPtr,
		Creator:       in.Creator,
		Scope:         legacyScope,
		Scopes:        in.Scopes,
		Labels:        in.Labels,
		Metadata:      meta,
		AllowedAccess: access,
		AllowedWrite:  in.AllowedWrite,
	})
	if err != nil {
		return sqlc.Artifact{}, fmt.Errorf("pgstore: insert: %w", err)
	}
	return row, nil
}

// AppendInput is the input to Append. Mirrors PutInput except Content
// is the bytes to be appended (not the full body), and the metadata
// fields are optional overrides for the new version.
type AppendInput struct {
	NamedSlug   string // required
	Separator   []byte // inserted between existing body and new content; default "\n\n" if nil
	Content     []byte // bytes to append
	Creator     string
	ContentType string // optional override; defaults to prior version's content_type

	// These four override the prior version's metadata on the new
	// version. Nil → inherit. Pass empty slice / empty string to clear.
	Title         *string
	Description   *string
	Scopes        []string
	Labels        []string
	AllowedAccess *[]string
	AllowedWrite  *[]string // nil → inherit prior version's write list

	// CheckAccess, if set, is called every time the retry loop below finds
	// an existing prior version — including on a retry after losing the
	// auto-create race, not just the first iteration. A one-time check by
	// the caller before invoking Append would miss that retry: the slug
	// can look absent on the caller's own pre-check, then come into
	// existence (as someone else's restricted artifact) in the race window
	// before this loop's own lookup. Returning an error aborts the append.
	CheckAccess func(ctx context.Context, prev sqlc.Artifact) error
}

// Append fetches the latest non-deleted version of slug, concatenates
// (existing body || separator || new content), and writes the result
// as the next version. If the slug doesn't exist, behaves like Put
// (auto-create), seeded only with the new content (no separator since
// nothing precedes it).
//
// Concurrency: under racing Append calls on the same slug, the unique
// (slug, version) index forces one writer to lose with a unique
// violation. The loser must retry the read+concat+write. We do up to
// `appendMaxRetries` retries internally to absorb common races; higher
// contention surfaces ErrConflict to the caller.
//
// Only TEXT and PACKAGE artifacts can be appended; ATTACHMENT is
// slugless and single-version by design (see applyAttachmentInvariants).
func (s *Store) Append(ctx context.Context, in AppendInput) (sqlc.Artifact, error) {
	if in.NamedSlug == "" {
		return sqlc.Artifact{}, fmt.Errorf("%w: append requires named_slug", ErrInvalidInput)
	}
	if in.Creator == "" {
		return sqlc.Artifact{}, fmt.Errorf("%w: append requires creator", ErrInvalidInput)
	}
	sep := in.Separator
	if sep == nil {
		sep = []byte("\n\n")
	}

	var lastErr error
	for attempt := 0; attempt < appendMaxRetries; attempt++ {
		prev, getErr := s.GetBySlug(ctx, in.NamedSlug, nil)

		// Slug doesn't exist → seed as v1 with just the new content. We
		// require the caller to also supply title + content_type in this
		// path; otherwise we can't construct a valid Put.
		if errors.Is(getErr, ErrNotFound) {
			if in.Title == nil || *in.Title == "" {
				return sqlc.Artifact{}, fmt.Errorf("%w: append-to-new-slug requires title", ErrInvalidInput)
			}
			if in.ContentType == "" {
				return sqlc.Artifact{}, fmt.Errorf("%w: append-to-new-slug requires content_type", ErrInvalidInput)
			}
			slug := in.NamedSlug
			row, err := s.Put(ctx, PutInput{
				ArtifactType:  TypeText,
				NamedSlug:     &slug,
				Title:         *in.Title,
				Description:   in.Description,
				ContentType:   in.ContentType,
				Content:       in.Content,
				Creator:       in.Creator,
				Scopes:        in.Scopes,
				Labels:        in.Labels,
				AllowedAccess: dereferenceSlice(in.AllowedAccess),
				AllowedWrite:  dereferenceSlice(in.AllowedWrite),
			})
			if err == nil || !isUniqueViolation(err) {
				return row, err
			}
			// Lost the create-race; another writer just inserted v1.
			// Retry the loop and we'll see it via GetBySlug this time.
			lastErr = err
			continue
		}
		if getErr != nil {
			return sqlc.Artifact{}, getErr
		}
		if in.CheckAccess != nil {
			if err := in.CheckAccess(ctx, prev); err != nil {
				return sqlc.Artifact{}, err
			}
		}
		if prev.ArtifactType == TypeAttachment {
			return sqlc.Artifact{}, fmt.Errorf("%w: cannot append to ATTACHMENT artifact (slug %q); ATTACHMENT is slugless + single-version by design", ErrInvalidInput, in.NamedSlug)
		}
		if IsPackageLike(prev.ArtifactType) {
			return sqlc.Artifact{}, fmt.Errorf("%w: cannot append to %s artifact (slug %q); it is a zip and concat would corrupt the archive", ErrInvalidInput, prev.ArtifactType, in.NamedSlug)
		}

		// Read existing body. For inline rows it's in memory; for blob
		// rows we stream from S3. Either way we need the full byte slice
		// to concat.
		rc, err := s.Content(ctx, prev)
		if err != nil {
			return sqlc.Artifact{}, fmt.Errorf("pgstore: read prior body: %w", err)
		}
		existing, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return sqlc.Artifact{}, fmt.Errorf("pgstore: read prior body: %w", err)
		}

		combined := make([]byte, 0, len(existing)+len(sep)+len(in.Content))
		combined = append(combined, existing...)
		if len(existing) > 0 && len(in.Content) > 0 {
			combined = append(combined, sep...)
		}
		combined = append(combined, in.Content...)

		// Inherit metadata from prior version unless explicitly overridden.
		title := prev.Title
		if in.Title != nil {
			title = *in.Title
		}
		description := prev.Description
		if in.Description != nil {
			description = in.Description
		}
		contentType := prev.ContentType
		if in.ContentType != "" {
			contentType = in.ContentType
		}
		scopes := in.Scopes
		if scopes == nil {
			scopes = prev.Scopes
		}
		labels := in.Labels
		if labels == nil {
			labels = prev.Labels
		}
		var access []string
		if in.AllowedAccess != nil {
			access = *in.AllowedAccess
		} else {
			access = prev.AllowedAccess
		}
		var write []string
		if in.AllowedWrite != nil {
			write = *in.AllowedWrite
		} else {
			write = prev.AllowedWrite
		}

		slug := in.NamedSlug
		row, err := s.Put(ctx, PutInput{
			ArtifactType:  prev.ArtifactType,
			NamedSlug:     &slug,
			Title:         title,
			Description:   description,
			ContentType:   contentType,
			Content:       combined,
			Creator:       in.Creator,
			Scopes:        scopes,
			Labels:        labels,
			AllowedAccess: access,
			AllowedWrite:  write,
		})
		if err == nil {
			return row, nil
		}
		if !isUniqueViolation(err) {
			return sqlc.Artifact{}, err
		}
		// Lost a (slug, version) race. Retry from a fresh read.
		lastErr = err
	}
	if lastErr != nil {
		return sqlc.Artifact{}, fmt.Errorf("%w: append after %d retries: %v", ErrConflict, appendMaxRetries, lastErr)
	}
	return sqlc.Artifact{}, fmt.Errorf("%w: append after %d retries", ErrConflict, appendMaxRetries)
}

// appendMaxRetries caps the Append retry budget on (slug, version)
// unique-violation races. Sized to absorb N concurrent appenders on
// the same slug — under N writers, the unlucky one may have to retry
// up to N-1 times waiting for prior versions to commit. 8 covers
// reasonable contention; pathological hot-spotting still surfaces as
// ErrConflict so the caller can decide to back off or shard.
const appendMaxRetries = 8

// isUniqueViolation reports whether err is a Postgres unique-violation
// (23505) — used by Append to detect (slug, version) races.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key")
}

// dereferenceSlice unwraps a *[]string for Put's AllowedAccess input:
// nil pointer → nil slice (means "default"); non-nil pointer → the
// pointed slice (empty means "creator-only").
func dereferenceSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// IdempotencyHit is what GetIdempotency returns on a cache hit. Bare
// pointer to the cached artifact_id + version so the caller can re-fetch
// and re-serve the original response.
type IdempotencyHit struct {
	ArtifactID uuid.UUID
	Version    *int32
}

// GetIdempotency returns the cached write-response for (key, creator)
// if a non-expired entry exists. ErrNotFound on miss (treat as
// "proceed with the write"); any other error is a real DB error.
func (s *Store) GetIdempotency(ctx context.Context, key, creator string) (IdempotencyHit, error) {
	if key == "" {
		return IdempotencyHit{}, ErrNotFound
	}
	row, err := s.q.GetIdempotencyKey(ctx, sqlc.GetIdempotencyKeyParams{
		IdempotencyKey: key,
		Creator:        creator,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyHit{}, ErrNotFound
	}
	if err != nil {
		return IdempotencyHit{}, fmt.Errorf("pgstore: get idempotency: %w", err)
	}
	return IdempotencyHit{
		ArtifactID: UUIDFromPG(row.ResponseArtifactID),
		Version:    row.ResponseVersion,
	}, nil
}

// RecordIdempotency stores (key, creator) → (artifact_id, version) and
// returns whether the insert actually took effect. Race-tolerant via
// ON CONFLICT DO NOTHING: if a concurrent writer recorded the same
// (key, creator) first, `inserted` is false and the caller should
// re-fetch the winner via GetIdempotency and return that response,
// so all retries of the same logical write converge on a single
// canonical artifact_id.
//
// `inserted=false` means the row already existed when we tried to write.
// `inserted=true` means our row is now the canonical one.
func (s *Store) RecordIdempotency(ctx context.Context, key, creator string, artifactID uuid.UUID, version *int32) (inserted bool, err error) {
	if key == "" {
		return false, nil
	}
	rows, err := s.q.InsertIdempotencyKey(ctx, sqlc.InsertIdempotencyKeyParams{
		IdempotencyKey:     key,
		Creator:            creator,
		ResponseArtifactID: pgUUID(artifactID),
		ResponseVersion:    version,
	})
	if err != nil {
		return false, fmt.Errorf("pgstore: record idempotency: %w", err)
	}
	return rows > 0, nil
}

// CleanupIdempotencyKeys deletes rows older than the 24h TTL. Returns
// number of rows deleted. Safe to run from a cron or admin endpoint.
func (s *Store) CleanupIdempotencyKeys(ctx context.Context) (int64, error) {
	return s.q.DeleteExpiredIdempotencyKeys(ctx)
}

// GetByID fetches an artifact (including soft-deleted) by UUID.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (sqlc.Artifact, error) {
	row, err := s.q.GetArtifact(ctx, pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Artifact{}, ErrNotFound
	}
	return row, err
}

// GetLatestBySlugForCaller fetches the latest non-deleted version of a
// slug that `caller` is allowed to read. Use for `/s/<slug>` (no
// version pinned) by a non-admin caller — if a newer version locked
// the caller out, we silently fall back to the latest version they
// can read, so they never learn about restricted newer versions.
// Admins should keep using GetBySlug with version=nil so they see the
// absolute latest.
func (s *Store) GetLatestBySlugForCaller(ctx context.Context, slug, caller string) (sqlc.Artifact, error) {
	// Resolve the caller's group tokens so a version granted via a group is
	// readable here too — keeping this single-row path consistent with both
	// List (buildWhere) and CanAccess.
	groups, err := s.CallerGroups(ctx, caller)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	slugPtr := &slug
	row, err := s.q.GetLatestArtifactBySlugForCaller(ctx, sqlc.GetLatestArtifactBySlugForCallerParams{
		NamedSlug:    slugPtr,
		Caller:       caller,
		CallerGroups: groups,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Artifact{}, ErrNotFound
	}
	return row, err
}

// GetBySlug fetches the latest non-deleted version (version==nil) or a
// specific version.
func (s *Store) GetBySlug(ctx context.Context, slug string, version *int32) (sqlc.Artifact, error) {
	slugPtr := &slug
	if version != nil {
		row, err := s.q.GetArtifactBySlugVersion(ctx, sqlc.GetArtifactBySlugVersionParams{
			NamedSlug: slugPtr,
			Version:   version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Artifact{}, ErrNotFound
		}
		return row, err
	}
	row, err := s.q.GetLatestArtifactBySlug(ctx, slugPtr)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Artifact{}, ErrNotFound
	}
	return row, err
}

// SlugLiveVersionStats reports, across all NON-deleted versions of a slug, the
// total count and how many were created by someone other than `caller`
// (case-insensitive). Used to gate slug-wide archive: a non-admin may archive
// a slug only when they created every live version (foreign == 0), since
// ArchiveBySlug soft-deletes them all.
func (s *Store) SlugLiveVersionStats(ctx context.Context, slug, caller string) (total, foreign int, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE lower(creator) <> lower($2))
		   FROM artifacts WHERE named_slug = $1 AND deleted_at IS NULL`,
		slug, caller).Scan(&total, &foreign)
	return total, foreign, err
}

// Content returns an io.ReadCloser over the artifact's bytes. For inline
// rows, the reader is in-memory; for blob_ref rows it streams from S3.
// Caller must Close.
func (s *Store) Content(ctx context.Context, row sqlc.Artifact) (io.ReadCloser, error) {
	if row.InlineContent != nil {
		return io.NopCloser(bytes.NewReader(row.InlineContent)), nil
	}
	if row.BlobRef == nil {
		return nil, fmt.Errorf("pgstore: row %s has no content", uuidFrom(row.ArtifactID))
	}
	rc, _, err := s.blob.Get(ctx, *row.BlobRef)
	return rc, err
}

// ListInput shapes a paginated list / filter query. OrderBy is one of
// the keys in sortableColumns; an unknown value falls back to
// created_at. OrderDir is "asc" or "desc" (case-insensitive);
// anything else falls back to desc.
type ListInput struct {
	Limit        int32
	Offset       int32
	ArtifactType *string
	ContentType  *string // optional content-type filter; supports `text/markdown*` glob
	Creator      *string
	Scope        *string
	Slug         *string
	Labels       []string
	// Not* fields are exclusions parsed from `-field:value` tokens. Each
	// value is an independent NOT condition (ANDed together), so
	// `-label:a -label:b` excludes rows carrying either. Glob (`*`) is
	// honored the same way as the positive filters. slug is intentionally
	// not negatable. See buildWhere.
	NotArtifactType []string
	NotCreator      []string
	NotScope        []string
	NotLabels       []string
	NotContentType  []string
	IncludeArchived bool
	OnlyArchived    bool   // when true, returns ONLY archived rows (deleted_at IS NOT NULL)
	LatestPerSlug   bool   // when true, collapse each named_slug to its highest version; slug-less rows always pass through
	Q               string // optional substring; empty == no substring filter
	OrderBy         string
	OrderDir        string
	// CallerEmail is the requester's identity. When set, results are
	// filtered to artifacts where caller matches allowed_access (or is
	// the creator). Empty == no access filter (callers responsible for
	// applying it externally, e.g. admin bypass).
	CallerEmail string

	// CallerGroups are the caller's current group tokens ("group:<name>").
	// Folded into the access filter so artifacts granted to a group the
	// caller belongs to show up in the catalog. Only consulted when
	// CallerEmail is set. Populated by the handler via Store.CallerGroups.
	CallerGroups []string

	// HideAppCouch drops rows scoped `app:couch` from the result. Set by the
	// catalog handlers for non-admin callers: that scope marks the couch
	// app's private state (stored as attachments), which shouldn't clutter
	// the human catalog. Ordinary attachments are visible. Admins (empty
	// CallerEmail) never set this, so they can still search everything.
	HideAppCouch bool
}

// ScopeAppCouch marks an artifact as the couch app's private data. Rows with
// this scope are hidden from the non-admin catalog (see HideAppCouch).
const ScopeAppCouch = "app:couch"

// ListResult bundles rows + total count.
type ListResult struct {
	Rows  []sqlc.Artifact
	Total int64
}

// sortableColumns maps API-facing sort keys to SQL column names. Anything
// not in this map is rejected at the API layer; nothing in this map is
// user input, so direct interpolation into the ORDER BY clause is safe.
var sortableColumns = map[string]string{
	"title":   "title",
	"type":    "artifact_type",
	"slug":    "named_slug",
	"version": "version",
	"creator": "creator",
	// Sort by the primary (first) scope, not the whole array: scopes[1] is
	// NULL for scopeless rows so the ORDER BY's NULLS LAST sends them to the
	// end (matching the old nullable scalar), and multi-scope rows order by
	// the same first scope the UI + dual-write treat as primary.
	"scope":   "scopes[1]",
	"created": "created_at",
	// Archive time; NULL on live rows (NULLS LAST parks them at the end).
	// The archived-only listing defaults to this, most recent first.
	"archived": "deleted_at",
}

// List builds the rows + count queries dynamically so sort fields stay
// in the SQL plan (vs. layering CASE-WHEN over a sqlc-generated query).
// Filters are joined with AND; an empty input returns every non-deleted
// artifact ordered by created_at desc.
func (s *Store) List(ctx context.Context, in ListInput) (ListResult, error) {
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}

	where, args := buildWhere(in)
	orderCol := sortableColumns[in.OrderBy]
	if orderCol == "" {
		// Archived-only listings order by archive time — "what did I
		// archive most recently" — everything else by creation time.
		if in.OnlyArchived {
			orderCol = "deleted_at"
		} else {
			orderCol = "created_at"
		}
	}
	orderDir := "DESC"
	if strings.EqualFold(in.OrderDir, "asc") {
		orderDir = "ASC"
	}

	// $N placeholders for LIMIT/OFFSET — appended last so existing $1..$N
	// from buildWhere don't shift.
	args = append(args, in.Limit, in.Offset)
	limitPos := len(args) - 1
	offsetPos := len(args)

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	rowsSQL := fmt.Sprintf(
		"SELECT * FROM artifacts %s ORDER BY %s %s NULLS LAST, artifact_id LIMIT $%d OFFSET $%d",
		whereSQL, orderCol, orderDir, limitPos, offsetPos,
	)
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM artifacts %s", whereSQL)

	rs, err := s.pool.Query(ctx, rowsSQL, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("pgstore: list: %w", err)
	}
	rows, err := pgx.CollectRows(rs, pgx.RowToStructByPos[sqlc.Artifact])
	if err != nil {
		return ListResult{}, fmt.Errorf("pgstore: list collect: %w", err)
	}

	// COUNT uses the same filter args minus the two trailing limit/offset values.
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, args[:len(args)-2]...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("pgstore: count: %w", err)
	}
	return ListResult{Rows: rows, Total: total}, nil
}

// Search is List + a substring filter on title/description/slug. The
// substring is also wired through buildWhere so it composes with every
// other filter.
func (s *Store) Search(ctx context.Context, q string, in ListInput) (ListResult, error) {
	in.Q = q
	return s.List(ctx, in)
}

// buildWhere translates a ListInput's filters into SQL fragments + the
// matching positional args. Caller must extend args with LIMIT / OFFSET
// after; the returned `args` already number from $1.
//
// Glob handling: any string filter (slug / scope / creator / type) that
// contains `*` is treated as a wildcard pattern and emitted as
// `column ILIKE $N` with `*` translated to SQL `%`. Without a `*` the
// comparison stays as exact equality. Existing `%` and `_` characters
// in user input are escaped so they don't smuggle in extra wildcards.
func buildWhere(in ListInput) (where []string, args []any) {
	addEq := func(col string, val string) {
		args = append(args, val)
		where = append(where, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	addLike := func(col string, val string) {
		args = append(args, globToLike(val))
		where = append(where, fmt.Sprintf("%s ILIKE $%d", col, len(args)))
	}
	addFilter := func(col string, val string) {
		if hasGlob(val) {
			addLike(col, val)
		} else {
			addEq(col, val)
		}
	}
	// addArray emits a membership test against an array column (scopes /
	// labels): exact values use `@> ARRAY[val]` (index-friendly), glob values
	// match any element via `unnest … ILIKE`. negate wraps the whole thing in
	// NOT for `-field:value` exclusions. Both columns are NOT NULL DEFAULT
	// '{}', so the negated form correctly keeps empty-array rows.
	addArray := func(col, val string, negate bool) {
		var cond string
		if hasGlob(val) {
			args = append(args, globToLike(val))
			cond = fmt.Sprintf("EXISTS (SELECT 1 FROM unnest(%s) e WHERE e ILIKE $%d)", col, len(args))
		} else {
			args = append(args, []string{val})
			cond = fmt.Sprintf("%s @> $%d", col, len(args))
		}
		if negate {
			cond = "NOT (" + cond + ")"
		}
		where = append(where, cond)
	}
	// addNotScalar excludes rows where a scalar column matches val. IS
	// DISTINCT FROM / IS NULL keep rows whose column is NULL (e.g. a slug-less
	// row under a future negation), since `col <> val` / `NOT col ILIKE val`
	// would evaluate to NULL and drop them.
	addNotScalar := func(col, val string) {
		if hasGlob(val) {
			args = append(args, globToLike(val))
			where = append(where, fmt.Sprintf("(%s IS NULL OR %s NOT ILIKE $%d)", col, col, len(args)))
		} else {
			args = append(args, val)
			where = append(where, fmt.Sprintf("%s IS DISTINCT FROM $%d", col, len(args)))
		}
	}
	if in.ArtifactType != nil && *in.ArtifactType != "" {
		addFilter("artifact_type", *in.ArtifactType)
	}
	if in.HideAppCouch {
		// Hide couch's private app state (scope app:couch) from the catalog.
		// addArray with negate emits NOT (scopes @> ARRAY['app:couch']),
		// which keeps every row that lacks the scope (incl. empty arrays).
		addArray("scopes", ScopeAppCouch, true)
	}
	if in.ContentType != nil && *in.ContentType != "" {
		addFilter("content_type", *in.ContentType)
	}
	if in.Creator != nil && *in.Creator != "" {
		addFilter("creator", *in.Creator)
	}
	if in.Scope != nil && *in.Scope != "" {
		addArray("scopes", *in.Scope, false)
	}
	if in.Slug != nil && *in.Slug != "" {
		addFilter("named_slug", *in.Slug)
	}
	// Multiple labels are ANDed: every label (exact or glob) is its own
	// membership test, so `label:a label:b*` requires both to be present.
	for _, l := range in.Labels {
		addArray("labels", l, false)
	}
	// Negated filters parsed from `-field:value`. Each is an independent
	// exclusion ANDed with the rest.
	for _, l := range in.NotLabels {
		addArray("labels", l, true)
	}
	for _, sc := range in.NotScope {
		addArray("scopes", sc, true)
	}
	for _, c := range in.NotCreator {
		addNotScalar("creator", c)
	}
	for _, at := range in.NotArtifactType {
		addNotScalar("artifact_type", at)
	}
	for _, ct := range in.NotContentType {
		addNotScalar("content_type", ct)
	}
	// Free text is split into words on whitespace and the slug separators
	// `-`/`_`, so "vc agent", "vc-agent", and "vc_agent" all tokenize to
	// [vc, agent] and match a title written any of those ways, in any word
	// order. Each word must appear as a substring in at least one column
	// (logical AND across words, OR across columns). Labels are searched via
	// array_to_string so free text finds artifacts by label without the
	// explicit `label:` operator; creator stays in the set so searching a
	// coworker's name ("derek") matches creator = "derek.xx@abc.com". Words
	// are separator-free, so an array_to_string match can't straddle two
	// labels across the joining space.
	for _, word := range strings.FieldsFunc(in.Q, isFreeTextSep) {
		args = append(args, "%"+globToLike(word)+"%")
		i := len(args)
		where = append(where, fmt.Sprintf(
			"(title ILIKE $%d OR description ILIKE $%d OR named_slug ILIKE $%d OR creator ILIKE $%d OR array_to_string(labels, ' ') ILIKE $%d)",
			i, i, i, i, i,
		))
	}
	// Archive-state predicate defining the candidate set. Computed once and
	// reused by the latest-per-slug MAX subquery below so the two agree: when
	// archived rows are included, they widen the set the latest is chosen
	// from, and a newer archived version can be the one kept. Empty string
	// means "no archive filter" (every version is a candidate).
	archPred := "deleted_at IS NULL" // default: exclude archived
	switch {
	case in.OnlyArchived:
		archPred = "deleted_at IS NOT NULL"
	case in.IncludeArchived:
		archPred = ""
	}
	if archPred != "" {
		where = append(where, archPred)
	}
	if in.LatestPerSlug {
		// Collapse each slug to its highest version *within the candidate set*
		// (same archive predicate as the outer query). Slug-less rows
		// (named_slug IS NULL) each stand alone and always pass. The
		// correlated MAX subquery composes with every other filter and
		// applies identically to the rows + COUNT queries. No bind args
		// needed — it's a pure self-join on named_slug.
		subArch := ""
		if archPred != "" {
			subArch = " AND b." + archPred
		}
		where = append(where, `(
			named_slug IS NULL
			OR version = (
				SELECT MAX(b.version) FROM artifacts b
				WHERE b.named_slug = artifacts.named_slug`+subArch+`
			)
		)`)
	}
	if in.CallerEmail != "" {
		// Access filter — caller must be the creator OR match one of
		// the allowed_access patterns. Matching is case-insensitive and
		// whole-string-anchored. `*` (alone, the default) matches all
		// authenticated callers. Patterns containing `*` / `?` are
		// translated to SQL `%` / `_`; existing wildcards in `p` are
		// preserved literally because translate() runs after escape.
		//
		// The EXISTS subquery short-circuits per row; with typical
		// allowed_access arrays of 1–5 entries the cost is negligible.
		args = append(args, in.CallerEmail)
		i := len(args)
		// Group access: the row grants the caller if allowed_access overlaps
		// the caller's group tokens. `&&` is the Postgres array-overlap
		// operator; tokens are canonical lowercase on both sides (group names
		// are normalized on write; CallerGroups builds tokens from them), so a
		// plain overlap is exact. An empty/NULL token array can't overlap, so
		// non-members simply don't get the extra grant.
		args = append(args, in.CallerGroups)
		gi := len(args)
		// Pattern translation:
		//   1. Escape pre-existing `%` / `_` in p so e.g. `alice_test@a.com`
		//      doesn't become a wildcard.
		//   2. Map glob `*`→`%` and `?`→`_`.
		// LIKE matches with ESCAPE '\' so the escaped wildcards stay literal.
		where = append(where, fmt.Sprintf(`(
			creator = $%d
			OR allowed_access && $%d::text[]
			OR EXISTS (
				SELECT 1 FROM unnest(allowed_access) p
				WHERE p = '*'
				   OR lower(p) = lower($%d)
				   OR lower($%d) LIKE lower(translate(replace(replace(p, '%%', '\%%'), '_', '\_'), '*?', '%%_')) ESCAPE '\'
			)
		)`, i, gi, i, i))
	}
	return where, args
}

// isFreeTextSep reports whether r separates words in a free-text search
// query. Whitespace plus the slug separators `-`/`_` all split, so a query
// and a stored value that differ only in those separators still match.
func isFreeTextSep(r rune) bool {
	return unicode.IsSpace(r) || r == '-' || r == '_'
}

// hasGlob reports whether the user-facing string contains a `*` glob.
func hasGlob(s string) bool { return strings.Contains(s, "*") }

// globToLike escapes SQL wildcards in user input and converts `*` → `%`.
// Returns a string safe to pass directly into an ILIKE comparison.
func globToLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return strings.ReplaceAll(s, "*", "%")
}

func (s *Store) Versions(ctx context.Context, slug string) ([]sqlc.Artifact, error) {
	return s.q.ListArtifactVersions(ctx, &slug)
}

// GetByIDs fetches artifacts by a set of UUIDs. The result order is
// arbitrary — callers that need a specific order (e.g. relevance) must
// re-sort. Missing IDs are silently omitted.
func (s *Store) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]sqlc.Artifact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	pgIDs := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		pgIDs[i] = pgUUID(id)
	}
	rows, err := s.q.GetArtifactsByIDs(ctx, pgIDs)
	if err != nil {
		return nil, fmt.Errorf("pgstore: get by ids: %w", err)
	}
	return rows, nil
}

func (s *Store) ArchiveByID(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.SoftDeleteArtifact(ctx, pgUUID(id))
}

func (s *Store) ArchiveBySlug(ctx context.Context, slug string) (int64, error) {
	return s.q.SoftDeleteArtifactBySlug(ctx, &slug)
}

// UnarchiveByID clears deleted_at on a previously archived row.
// Returns 0 affected rows when the artifact is missing or not archived.
func (s *Store) UnarchiveByID(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.UnarchiveArtifact(ctx, pgUUID(id))
}

// HardDeleteByID removes the row permanently. Admin-gated at the HTTP
// layer; the orphan S3 blob (if any) is left for a separate sweep.
func (s *Store) HardDeleteByID(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.HardDeleteArtifact(ctx, pgUUID(id))
}

// UpdateTitle changes the human-facing title on an artifact.
// Permission is enforced at the HTTP layer (creator or admin).
func (s *Store) UpdateTitle(ctx context.Context, id uuid.UUID, title string) (int64, error) {
	return s.q.UpdateArtifactTitle(ctx, sqlc.UpdateArtifactTitleParams{
		ArtifactID: pgUUID(id),
		Title:      title,
	})
}

// UpdateDescription replaces the description on an artifact. An empty string
// clears it (stored as NULL), matching how Put treats an absent description.
// Permission is enforced at the HTTP layer (creator or admin).
func (s *Store) UpdateDescription(ctx context.Context, id uuid.UUID, desc string) (int64, error) {
	var d *string
	if desc != "" {
		d = &desc
	}
	return s.q.UpdateArtifactDescription(ctx, sqlc.UpdateArtifactDescriptionParams{
		ArtifactID:  pgUUID(id),
		Description: d,
	})
}

// UpdateLabels replaces the labels array on an artifact. nil labels is
// normalized to an empty slice so the column never holds NULL.
// Permission is enforced at the HTTP layer (creator or admin).
func (s *Store) UpdateLabels(ctx context.Context, id uuid.UUID, labels []string) (int64, error) {
	if labels == nil {
		labels = []string{}
	}
	return s.q.UpdateArtifactLabels(ctx, sqlc.UpdateArtifactLabelsParams{
		ArtifactID: pgUUID(id),
		Labels:     labels,
	})
}

// UpdateScopes replaces the scopes array on an artifact. nil scopes is
// normalized to empty. The legacy scalar scope column is dual-written as
// scopes[0] (NULL when empty) for phase-1 rollout safety.
func (s *Store) UpdateScopes(ctx context.Context, id uuid.UUID, scopes []string) (int64, error) {
	if scopes == nil {
		scopes = []string{}
	}
	var legacyScope *string
	if len(scopes) > 0 {
		sc := scopes[0] // copy so the pointer doesn't alias the slice backing array
		legacyScope = &sc
	}
	return s.q.UpdateArtifactScopes(ctx, sqlc.UpdateArtifactScopesParams{
		ArtifactID: pgUUID(id),
		Scopes:     scopes,
		Scope:      legacyScope,
	})
}

// UpdateAccess replaces allowed_access AND allowed_write on a single artifact
// version. access nil → `['*']` (the everyone-default, never NULL). write nil
// → SQL NULL (write follows read — the back-compat default); non-nil (incl.
// empty) is authoritative, empty == creator-only. Enforces the ⊆ invariant:
// when write is non-nil it is unioned into access so read paths stay a
// superset. Permission enforced at the HTTP layer; per-version, not per-slug.
func (s *Store) UpdateAccess(ctx context.Context, id uuid.UUID, access []string, write []string) (int64, error) {
	// Attachments stay creator-only — an access update must never widen them.
	// Enforced here (the single update chokepoint) so every caller is covered,
	// mirroring applyAttachmentInvariants on create.
	if row, err := s.GetByID(ctx, id); err == nil && row.ArtifactType == TypeAttachment {
		access, write = []string{}, []string{}
	}
	if access == nil {
		access = []string{"*"}
	}
	if write != nil {
		access = unionTokens(access, write)
	}
	return s.q.UpdateArtifactAccess(ctx, sqlc.UpdateArtifactAccessParams{
		ArtifactID:    pgUUID(id),
		AllowedAccess: access,
		AllowedWrite:  write,
	})
}

// unionTokens returns a ∪ b, preserving a's order then b's new entries, with
// exact (case-sensitive) dedupe — matching how tokens are compared everywhere
// else (SQL array overlap + matchPatterns group equality).
func unionTokens(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	for _, s := range b {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// CanAccess reports whether `caller` matches an artifact's allowed_access
// patterns or is the creator. Mirrors the buildWhere clause used by List
// so single-row reads share the same semantics. Empty caller (e.g.
// auth-disabled test mode) is treated as "no", except admin-bypass which
// is applied at the service layer.
// callerGroups is the caller's current group tokens ("group:<name>"), as
// returned by Store.CallerGroups — precomputed once per request so the
// per-row check stays a small set comparison rather than a member expansion.
// Pass nil when the caller belongs to no groups (or for non-group callers
// like tests that don't exercise groups).
func CanAccess(row sqlc.Artifact, caller string, callerGroups []string) bool {
	if caller == "" {
		return false
	}
	if strings.EqualFold(row.Creator, caller) {
		return true
	}
	return matchPatterns(row.AllowedAccess, caller, callerGroups)
}

// matchPatterns reports whether caller (or one of callerGroups) is granted by
// the given token list. Shared by CanAccess (read) and CanWrite (write) so the
// glob / email / group / `*` semantics stay identical across both. Does NOT
// include the creator check — callers handle that.
func matchPatterns(patterns []string, caller string, callerGroups []string) bool {
	lower := strings.ToLower(caller)
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if strings.EqualFold(p, caller) {
			return true
		}
		if matchGlob(strings.ToLower(p), lower) {
			return true
		}
	}
	// Group grants: pass if the row references any group the caller currently
	// belongs to. Matched by EXACT token equality (not EqualFold) to stay
	// consistent with the SQL paths, whose `allowed_access && $::text[]`
	// overlap is case-sensitive. Both sides are canonical lowercase — group
	// names are normalized on write (normalizeGroupName) and CallerGroups
	// builds tokens from them — so exact match is correct, and a stray
	// non-canonical token like `group:Eng` fails closed everywhere alike.
	for _, g := range callerGroups {
		for _, p := range patterns {
			if p == g {
				return true
			}
		}
	}
	return false
}

// CanWrite reports whether caller may create a new version / append / edit the
// content of row. The creator always may. When allowed_write is NULL (nil),
// write access follows read access (same tokens as CanAccess). When non-nil
// (including the empty slice), allowed_write is the authoritative write list —
// empty means creator-only. Because allowed_write is a subset of allowed_access
// (enforced on save), a write grant always implies read.
func CanWrite(row sqlc.Artifact, caller string, callerGroups []string) bool {
	if caller == "" {
		return false
	}
	if strings.EqualFold(row.Creator, caller) {
		return true
	}
	if row.AllowedWrite == nil {
		return matchPatterns(row.AllowedAccess, caller, callerGroups)
	}
	return matchPatterns(row.AllowedWrite, caller, callerGroups)
}

// matchGlob is a minimal glob matcher supporting `*` (any run) and `?`
// (one char). Anchored to the full string. Both args are expected to
// be already-lowercased.
func matchGlob(pattern, s string) bool {
	// Quick exits.
	if pattern == "" {
		return s == ""
	}
	if pattern == "*" {
		return true
	}
	// Iterative DP-style match with backtracking on `*`.
	pi, si := 0, 0
	starPi, starSi := -1, -1
	for si < len(s) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starPi = pi
			starSi = si
			pi++
			continue
		}
		if starPi != -1 {
			pi = starPi + 1
			starSi++
			si = starSi
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// ─── MCP OAuth (RFC 7591 DCR + authorization-code grant) ─────────────

func (s *Store) InsertMCPClient(ctx context.Context, arg sqlc.InsertMCPClientParams) error {
	return s.q.InsertMCPClient(ctx, arg)
}

func (s *Store) GetMCPClient(ctx context.Context, clientID string) (sqlc.McpOauthClient, error) {
	return s.q.GetMCPClient(ctx, clientID)
}

func (s *Store) TouchMCPClient(ctx context.Context, clientID string) error {
	return s.q.TouchMCPClient(ctx, clientID)
}

func (s *Store) InsertMCPCode(ctx context.Context, arg sqlc.InsertMCPCodeParams) error {
	return s.q.InsertMCPCode(ctx, arg)
}

func (s *Store) TakeMCPCode(ctx context.Context, code string) (sqlc.McpOauthCode, error) {
	return s.q.TakeMCPCode(ctx, code)
}

// DeviceStore methods — thin forwarders so *Store satisfies auth.DeviceStore.

func (s *Store) InsertDeviceCode(ctx context.Context, arg sqlc.InsertDeviceCodeParams) error {
	return s.q.InsertDeviceCode(ctx, arg)
}

func (s *Store) GetDeviceByUserCode(ctx context.Context, userCode string) (sqlc.DeviceAuth, error) {
	return s.q.GetDeviceByUserCode(ctx, userCode)
}

func (s *Store) GetDeviceCode(ctx context.Context, deviceCode string) (sqlc.DeviceAuth, error) {
	return s.q.GetDeviceCode(ctx, deviceCode)
}

func (s *Store) ApproveDeviceCode(ctx context.Context, arg sqlc.ApproveDeviceCodeParams) error {
	return s.q.ApproveDeviceCode(ctx, arg)
}

func (s *Store) TakeApprovedDeviceCode(ctx context.Context, deviceCode string) (sqlc.DeviceAuth, error) {
	return s.q.TakeApprovedDeviceCode(ctx, deviceCode)
}

func (s *Store) InsertDeviceTokenFamily(ctx context.Context, arg sqlc.InsertDeviceTokenFamilyParams) error {
	return s.q.InsertDeviceTokenFamily(ctx, arg)
}

func (s *Store) GetDeviceTokenFamily(ctx context.Context, familyID string) (sqlc.DeviceToken, error) {
	return s.q.GetDeviceTokenFamily(ctx, familyID)
}

func (s *Store) RotateDeviceTokenRefresh(ctx context.Context, arg sqlc.RotateDeviceTokenRefreshParams) (int64, error) {
	return s.q.RotateDeviceTokenRefresh(ctx, arg)
}

func (s *Store) RevokeDeviceTokensForEmail(ctx context.Context, email string) error {
	return s.q.RevokeDeviceTokensForEmail(ctx, email)
}

func (s *Store) RevokeDeviceTokenFamily(ctx context.Context, familyID string) error {
	return s.q.RevokeDeviceTokenFamily(ctx, familyID)
}

func (s *Store) InsertAPIKey(ctx context.Context, arg sqlc.InsertAPIKeyParams) (sqlc.ApiKey, error) {
	return s.q.InsertAPIKey(ctx, arg)
}

func (s *Store) GetAPIKeyByHash(ctx context.Context, hash []byte) (sqlc.ApiKey, error) {
	return s.q.GetAPIKeyByHash(ctx, hash)
}

func (s *Store) ListAPIKeysByOwner(ctx context.Context, email string) ([]sqlc.ApiKey, error) {
	return s.q.ListAPIKeysByOwner(ctx, email)
}

func (s *Store) ListAllAPIKeys(ctx context.Context) ([]sqlc.ApiKey, error) {
	return s.q.ListAllAPIKeys(ctx)
}

func (s *Store) RevokeAPIKey(ctx context.Context, arg sqlc.RevokeAPIKeyParams) (int64, error) {
	return s.q.RevokeAPIKey(ctx, arg)
}

func (s *Store) RevokeAPIKeyByID(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.q.RevokeAPIKeyByID(ctx, id)
}

func (s *Store) TouchAPIKey(ctx context.Context, id pgtype.UUID) error {
	return s.q.TouchAPIKey(ctx, id)
}

// blobKey builds the S3 object key. Two-character shard prefix keeps
// listing cheap.
func (s *Store) blobKey(kind string, id uuid.UUID) string {
	hex := strings.ReplaceAll(id.String(), "-", "")
	shard := hex[:2]
	prefix := s.cfg.BucketPrefixText
	suffix := ""
	switch kind {
	case TypePackage, TypeApp:
		prefix = s.cfg.BucketPrefixPackage
		suffix = ".zip"
	case TypeAttachment:
		prefix = s.cfg.BucketPrefixAttachment
	}
	return fmt.Sprintf("%s/%s/%s%s", prefix, shard, id.String(), suffix)
}

// nextVersion picks the version number to assign to a write.
//
//   - slug==nil  → nil (unnamed artifact)
//   - else       → MAX(non-deleted version under slug) + 1
//
// Callers cannot pin a specific version on upload; uploads always
// append the next monotonic number. Tombstoned versions don't count.
// SlugCreator returns the creator of a slug's EARLIEST version — the immutable
// owner of the slug. Unlike a per-version creator (which versioning reassigns
// to whoever pushed that version), this is stable across the slug's life, so it
// is the correct authority for who may change a slug's access. Ignores
// deleted_at so archiving v1 doesn't transfer ownership. ErrNotFound if the
// slug has no versions.
func (s *Store) SlugCreator(ctx context.Context, slug string) (string, error) {
	var creator string
	err := s.pool.QueryRow(ctx,
		`SELECT creator FROM artifacts WHERE named_slug = $1 ORDER BY version ASC LIMIT 1`,
		slug).Scan(&creator)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return creator, nil
}

func (s *Store) nextVersion(ctx context.Context, slug *string) (*int32, error) {
	if slug == nil {
		return nil, nil
	}
	n, err := s.q.NextVersionForSlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			one := int32(1)
			return &one, nil
		}
		return nil, fmt.Errorf("pgstore: next version: %w", err)
	}
	v := int32(n)
	return &v, nil
}

// pgUUID wraps a google/uuid.UUID as pgtype.UUID for sqlc.
func pgUUID(id uuid.UUID) pgtype.UUID {
	var p pgtype.UUID
	copy(p.Bytes[:], id[:])
	p.Valid = true
	return p
}

// uuidFrom unpacks pgtype.UUID back to a google/uuid.UUID.
func uuidFrom(p pgtype.UUID) uuid.UUID {
	var u uuid.UUID
	copy(u[:], p.Bytes[:])
	return u
}

// UUIDFromPG is exported for handlers.
func UUIDFromPG(p pgtype.UUID) uuid.UUID { return uuidFrom(p) }

// AggregatesInput tunes the aggregates query. Limit caps each result
// list independently; 0 = use default (20). CallerEmail, when set,
// restricts the counts to artifacts the caller can read — otherwise a
// restricted artifact's labels/scope leak via the histogram.
type AggregatesInput struct {
	Limit       int
	CallerEmail string
}

// ScopeTypeCount is one row of the scope-type histogram.
type ScopeTypeCount struct {
	Type  string
	Count int64
}

// ScopeCount is one row of the full-scope histogram.
type ScopeCount struct {
	Scope string
	Count int64
}

// LabelCount is one row of the label histogram.
type LabelCount struct {
	Label string
	Count int64
}

// ContentTypeCount is one row of the content_type histogram.
type ContentTypeCount struct {
	ContentType string
	Count       int64
}

// AggregatesResult is the shape returned by Aggregates.
type AggregatesResult struct {
	ScopeTypes   []ScopeTypeCount
	Scopes       []ScopeCount
	Labels       []LabelCount
	ContentTypes []ContentTypeCount
}

// Aggregates returns the most-used scope types, full scopes, and labels
// across all non-deleted artifacts. The scope-type histogram unnests the
// `scopes` array and splits each element on the first ':' — e.g.
// "topic:platform:auth" → "topic". The full-scope histogram counts whole
// scope strings. All lists are ordered by count desc, then alphabetical
// for stable ties.
//
// Counts are per-slug, not per-version: each slug contributes at most once,
// via its highest non-deleted version (slug-less rows stand alone). A slug
// whose every version is archived (deleted) therefore contributes 0. This
// mirrors the default LatestPerSlug list view, so a label's count matches
// the number of artifacts you'd see filtering on it.
func (s *Store) Aggregates(ctx context.Context, in AggregatesInput) (AggregatesResult, error) {
	limit := in.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// Mirror buildWhere's access predicate so histograms exclude
	// artifacts the caller can't read. Empty caller (admin path) skips
	// the filter entirely. Must stay in sync with buildWhere — including
	// the group-overlap branch, so a group member's histograms count the
	// artifacts they can actually see.
	accessClause := ""
	args := []any{limit}
	if in.CallerEmail != "" {
		groups, err := s.CallerGroups(ctx, in.CallerEmail)
		if err != nil {
			return AggregatesResult{}, err
		}
		args = append(args, in.CallerEmail, groups)
		accessClause = ` AND (
			creator = $2
			OR allowed_access && $3::text[]
			OR EXISTS (
				SELECT 1 FROM unnest(allowed_access) p
				WHERE p = '*'
				   OR lower(p) = lower($2)
				   OR lower($2) LIKE lower(translate(replace(replace(p, '%', '\%'), '_', '\_'), '*?', '%_')) ESCAPE '\'
			)
		)`
	}

	// Collapse each slug to its highest non-deleted version so a slug is
	// counted once regardless of how many versions it has; slug-less rows
	// stand alone. Mirrors buildWhere's LatestPerSlug clause. A slug whose
	// versions are all deleted has no surviving row and contributes 0.
	latestClause := `
			AND (
				named_slug IS NULL
				OR version = (
					SELECT MAX(b.version) FROM artifacts b
					WHERE b.named_slug = artifacts.named_slug AND b.deleted_at IS NULL
				)
			)`

	scopeSQL := `
		SELECT split_part(s, ':', 1) AS scope_type, COUNT(*) AS n
		FROM (
			SELECT unnest(scopes) AS s
			FROM artifacts
			WHERE deleted_at IS NULL` + accessClause + latestClause + `
		) t
		WHERE s <> ''
		GROUP BY scope_type
		ORDER BY n DESC, scope_type ASC
		LIMIT $1
	`
	scopesSQL := `
		SELECT s AS scope, COUNT(*) AS n
		FROM (
			SELECT unnest(scopes) AS s
			FROM artifacts
			WHERE deleted_at IS NULL` + accessClause + latestClause + `
		) t
		WHERE s <> ''
		GROUP BY s
		ORDER BY n DESC, s ASC
		LIMIT $1
	`
	labelSQL := `
		SELECT label, COUNT(*) AS n
		FROM (
			SELECT unnest(labels) AS label
			FROM artifacts
			WHERE deleted_at IS NULL` + accessClause + latestClause + `
		) t
		GROUP BY label
		ORDER BY n DESC, label ASC
		LIMIT $1
	`
	// content_type is a scalar column (no unnest needed), unlike scopes/labels.
	contentTypeSQL := `
		SELECT content_type, COUNT(*) AS n
		FROM artifacts
		WHERE deleted_at IS NULL` + accessClause + latestClause + `
		GROUP BY content_type
		ORDER BY n DESC, content_type ASC
		LIMIT $1
	`

	out := AggregatesResult{
		ScopeTypes:   []ScopeTypeCount{},
		Scopes:       []ScopeCount{},
		Labels:       []LabelCount{},
		ContentTypes: []ContentTypeCount{},
	}

	rs, err := s.pool.Query(ctx, scopeSQL, args...)
	if err != nil {
		return out, fmt.Errorf("pgstore: aggregates scope: %w", err)
	}
	for rs.Next() {
		var r ScopeTypeCount
		if err := rs.Scan(&r.Type, &r.Count); err != nil {
			rs.Close()
			return out, fmt.Errorf("pgstore: aggregates scope scan: %w", err)
		}
		out.ScopeTypes = append(out.ScopeTypes, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return out, fmt.Errorf("pgstore: aggregates scope iter: %w", err)
	}

	rs, err = s.pool.Query(ctx, scopesSQL, args...)
	if err != nil {
		return out, fmt.Errorf("pgstore: aggregates scopes: %w", err)
	}
	for rs.Next() {
		var r ScopeCount
		if err := rs.Scan(&r.Scope, &r.Count); err != nil {
			rs.Close()
			return out, fmt.Errorf("pgstore: aggregates scopes scan: %w", err)
		}
		out.Scopes = append(out.Scopes, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return out, fmt.Errorf("pgstore: aggregates scopes iter: %w", err)
	}

	rs, err = s.pool.Query(ctx, labelSQL, args...)
	if err != nil {
		return out, fmt.Errorf("pgstore: aggregates label: %w", err)
	}
	for rs.Next() {
		var r LabelCount
		if err := rs.Scan(&r.Label, &r.Count); err != nil {
			rs.Close()
			return out, fmt.Errorf("pgstore: aggregates label scan: %w", err)
		}
		out.Labels = append(out.Labels, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return out, fmt.Errorf("pgstore: aggregates label iter: %w", err)
	}

	rs, err = s.pool.Query(ctx, contentTypeSQL, args...)
	if err != nil {
		return out, fmt.Errorf("pgstore: aggregates content_type: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var r ContentTypeCount
		if err := rs.Scan(&r.ContentType, &r.Count); err != nil {
			return out, fmt.Errorf("pgstore: aggregates content_type scan: %w", err)
		}
		out.ContentTypes = append(out.ContentTypes, r)
	}
	if err := rs.Err(); err != nil {
		return out, fmt.Errorf("pgstore: aggregates content_type iter: %w", err)
	}
	return out, nil
}

// browseFacetColumns maps a Browse-page facet key to the SQL producing its
// distinct values: a bare column for scalar facets (type, content_type), or
// an unnest(...) for array facets (label, scope). Only these four keys are
// valid; anything else is a caller bug (validated by the HTTP layer's own
// allow-list before it ever reaches here).
var browseFacetColumns = map[string]string{
	"type":         "artifact_type",
	"content_type": "content_type",
	"label":        "unnest(labels)",
	"scope":        "unnest(scopes)",
	"owner":        "creator",
}

// BrowseValueCount is one row of a BrowseAggregates result: a distinct facet
// value and how many (latest, readable) artifacts carry it.
type BrowseValueCount struct {
	Value string
	Count int64
}

// BrowseAggregatesResult is the shape returned by BrowseAggregates.
type BrowseAggregatesResult struct {
	Values []BrowseValueCount
	Total  int64 // total distinct values for the facet, for pagination
}

// BrowseAggregatesInput selects and pages one facet's complete value+count
// list for the Browse page. Unlike Aggregates (a fixed top-N for the
// sidebar), this returns every distinct value, sorted and paged so the UI
// can page through all of them.
type BrowseAggregatesInput struct {
	Facet       string // one of the keys in browseFacetColumns
	Sort        string // "count" or "name"; anything else falls back to "count"
	Dir         string // "asc" or "desc" (case-insensitive); anything else falls back to "desc"
	Limit       int32
	Offset      int32
	CallerEmail string
}

// BrowseAggregates returns every distinct value (and its count) for one
// facet, access-filtered and collapsed per-slug exactly like Aggregates, but
// paged and sorted instead of capped at a fixed top-N. The total distinct
// count rides along via COUNT(*) OVER() so the caller can paginate without a
// second query.
func (s *Store) BrowseAggregates(ctx context.Context, in BrowseAggregatesInput) (BrowseAggregatesResult, error) {
	col, ok := browseFacetColumns[in.Facet]
	if !ok {
		return BrowseAggregatesResult{}, fmt.Errorf("pgstore: unknown browse facet %q", in.Facet)
	}
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}

	// Mirrors Aggregates' access + latest-version clauses exactly — see its
	// comments for why.
	accessClause := ""
	args := []any{}
	if in.CallerEmail != "" {
		groups, err := s.CallerGroups(ctx, in.CallerEmail)
		if err != nil {
			return BrowseAggregatesResult{}, err
		}
		args = append(args, in.CallerEmail, groups)
		accessClause = ` AND (
			creator = $1
			OR allowed_access && $2::text[]
			OR EXISTS (
				SELECT 1 FROM unnest(allowed_access) p
				WHERE p = '*'
				   OR lower(p) = lower($1)
				   OR lower($1) LIKE lower(translate(replace(replace(p, '%', '\%'), '_', '\_'), '*?', '%_')) ESCAPE '\'
			)
		)`
	}
	latestClause := `
		AND (
			named_slug IS NULL
			OR version = (
				SELECT MAX(b.version) FROM artifacts b
				WHERE b.named_slug = artifacts.named_slug AND b.deleted_at IS NULL
			)
		)`

	// label/scope unnest an array and so can produce '' for an empty
	// element; type/content_type are NOT NULL scalars and never empty.
	// Filtering '' only for the array facets mirrors Aggregates' scopeSQL.
	emptyFilter := ""
	if in.Facet == "label" || in.Facet == "scope" {
		emptyFilter = "WHERE value <> ''"
	}

	// order is built from a fixed allow-list (Sort/Dir), never interpolated
	// user input, so direct string concatenation into ORDER BY is safe —
	// same reasoning as sortableColumns in List.
	order := "n DESC, value ASC"
	switch in.Sort {
	case "name":
		if strings.EqualFold(in.Dir, "desc") {
			order = "value DESC"
		} else {
			order = "value ASC"
		}
	default: // "count" (default)
		if strings.EqualFold(in.Dir, "asc") {
			order = "n ASC, value ASC"
		} else {
			order = "n DESC, value ASC"
		}
	}

	// Total is queried separately from the paged rows: COUNT(*) OVER() only
	// materializes on rows the LIMIT/OFFSET actually return, so a page past
	// the last one (0 rows) would otherwise report Total=0 even though the
	// facet has values — breaking "showing X-Y of Z" and look-ahead
	// pagination on the Browse page.
	totalSQL := fmt.Sprintf(`
		SELECT COUNT(*) FROM (
			SELECT value
			FROM (
				SELECT %s AS value
				FROM artifacts
				WHERE deleted_at IS NULL%s%s
			) t
			%s
			GROUP BY value
		) counted
	`, col, accessClause, latestClause, emptyFilter)

	var total int64
	if err := s.pool.QueryRow(ctx, totalSQL, args...).Scan(&total); err != nil {
		return BrowseAggregatesResult{}, fmt.Errorf("pgstore: browse aggregates %s total: %w", in.Facet, err)
	}

	pageArgs := append(append([]any{}, args...), in.Limit, in.Offset)
	limitPos := len(pageArgs) - 1
	offsetPos := len(pageArgs)

	pageSQL := fmt.Sprintf(`
		SELECT value, COUNT(*) AS n
		FROM (
			SELECT %s AS value
			FROM artifacts
			WHERE deleted_at IS NULL%s%s
		) t
		%s
		GROUP BY value
		ORDER BY %s
		LIMIT $%d OFFSET $%d
	`, col, accessClause, latestClause, emptyFilter, order, limitPos, offsetPos)

	rs, err := s.pool.Query(ctx, pageSQL, pageArgs...)
	if err != nil {
		return BrowseAggregatesResult{}, fmt.Errorf("pgstore: browse aggregates %s: %w", in.Facet, err)
	}
	defer rs.Close()
	out := BrowseAggregatesResult{Values: []BrowseValueCount{}, Total: total}
	for rs.Next() {
		var r BrowseValueCount
		if err := rs.Scan(&r.Value, &r.Count); err != nil {
			return BrowseAggregatesResult{}, fmt.Errorf("pgstore: browse aggregates %s scan: %w", in.Facet, err)
		}
		out.Values = append(out.Values, r)
	}
	if err := rs.Err(); err != nil {
		return BrowseAggregatesResult{}, fmt.Errorf("pgstore: browse aggregates %s iter: %w", in.Facet, err)
	}
	return out, nil
}
