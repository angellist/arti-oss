package artifacts

import (
	"encoding/json"
	"strconv"
	"time"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ArtifactInfo is the wire shape for a single artifact's metadata.
// JSON keys are snake_case to match the design + CLI expectations.
type ArtifactInfo struct {
	ArtifactID    string   `json:"artifact_id"`
	ArtifactType  string   `json:"artifact_type"`
	NamedSlug     *string  `json:"named_slug"`
	Version       *int32   `json:"version"`
	Title         string   `json:"title"`
	Description   *string  `json:"description"`
	ContentType   string   `json:"content_type"`
	SizeBytes     *int64   `json:"size_bytes"`
	SHA256        *string  `json:"sha256"`
	Creator       string   `json:"creator"`
	Scopes        []string `json:"scopes"`
	Labels        []string `json:"labels"`
	AllowedAccess []string `json:"allowed_access"`
	// AllowedWrite is the write-access list. nil → write follows read (the
	// back-compat default, serialized as JSON null); non-nil (incl. empty) is
	// authoritative, empty ([]) == creator-only. The nil-vs-empty distinction
	// is meaningful, so this field must NOT use omitempty — omitempty drops
	// both nil AND the empty slice, collapsing "mirror" and "creator-only" into
	// an absent field and letting the client mistake creator-only for mirror
	// (re-opening writes to readers). Always emit it.
	AllowedWrite []string `json:"allowed_write"`
	// CanWrite reports whether the requesting caller may write (version /
	// append / edit) THIS artifact — the effective result of checkWriteAccess,
	// so it accounts for creator, admin, allowed_write, groups, and `idp:`
	// membership without the client re-deriving any of it. Pointer + omitempty:
	// nil (omitted) on caller-agnostic paths (list/search) where it isn't
	// computed; set only on the single-artifact viewer paths (Get/GetBySlug).
	CanWrite   *bool          `json:"can_write,omitempty"`
	Metadata   map[string]any `json:"metadata"`
	CreatedAt  time.Time      `json:"created_at"`
	ModifiedAt time.Time      `json:"modified_at"`
	DeletedAt  *time.Time     `json:"deleted_at"`
	URL        string         `json:"url"`

	// CommentCount is the number of live (non-deleted) comments on THIS
	// version, and OpenThreadCount how many of its threads are unresolved.
	// Comments key off artifact_id (per-version), so both are version-scoped
	// — the same scoping the viewer shows. Populated only on the catalog
	// list/search responses, where the whole page is counted in one query;
	// omitted (nil) elsewhere so a single-artifact GET doesn't pay for a
	// count nobody renders.
	CommentCount    *int32 `json:"comment_count,omitempty"`
	OpenThreadCount *int32 `json:"open_thread_count,omitempty"`

	// Score is the BM25 relevance score from OpenSearch. Zero when search
	// is handled by Postgres or the result is from a non-search endpoint.
	Score float64 `json:"score,omitempty"`
	// Highlights are OpenSearch highlight fragments keyed by field name.
	// Nil/empty when highlights aren't available.
	Highlights map[string][]string `json:"highlights,omitempty"`
}

// ToInfo converts a sqlc.Artifact row to wire shape. baseURL must NOT
// have a trailing slash.
func ToInfo(row sqlc.Artifact, baseURL string) ArtifactInfo {
	id := pgstore.UUIDFromPG(row.ArtifactID).String()

	url := baseURL + "/a/" + id
	if row.NamedSlug != nil {
		url = baseURL + "/s/" + *row.NamedSlug
		if row.Version != nil {
			url += "/" + strconv.Itoa(int(*row.Version))
		}
	}

	meta := map[string]any{}
	if len(row.Metadata) > 0 {
		_ = json.Unmarshal(row.Metadata, &meta)
	}

	var deletedAt *time.Time
	if row.DeletedAt.Valid {
		t := row.DeletedAt.Time
		deletedAt = &t
	}

	labels := row.Labels
	if labels == nil {
		labels = []string{}
	}
	scopes := row.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	access := row.AllowedAccess
	if access == nil {
		access = []string{}
	}

	return ArtifactInfo{
		ArtifactID:    id,
		ArtifactType:  row.ArtifactType,
		NamedSlug:     row.NamedSlug,
		Version:       row.Version,
		Title:         row.Title,
		Description:   row.Description,
		ContentType:   row.ContentType,
		SizeBytes:     row.SizeBytes,
		SHA256:        row.SHA256,
		Creator:       row.Creator,
		Scopes:        scopes,
		Labels:        labels,
		AllowedAccess: access,
		AllowedWrite:  row.AllowedWrite, // nil stays nil (mirror) — do NOT normalize
		Metadata:      meta,
		CreatedAt:     row.CreatedAt.Time,
		ModifiedAt:    row.ModifiedAt.Time,
		DeletedAt:     deletedAt,
		URL:           url,
	}
}

// CreateRequest mirrors the POST body. `content` is base64 for binary
// uploads; for text it is the raw UTF-8 body.
type CreateRequest struct {
	ArtifactType  string   `json:"artifact_type"` // "TEXT" (default) | "PACKAGE"
	NamedSlug     *string  `json:"named_slug"`
	Title         string   `json:"title"`
	Description   *string  `json:"description"`
	ContentType   string   `json:"content_type"`
	Content       string   `json:"content"`        // raw text
	ContentBase64 string   `json:"content_base64"` // alias for binary
	Scopes        []string `json:"scopes"`
	// Scope is the deprecated single-scope input, folded into Scopes when
	// Scopes is absent. Remove in phase 2 (drop of the legacy scope column).
	Scope      *string         `json:"scope"`
	Labels     []string        `json:"labels"`
	Metadata   json.RawMessage `json:"metadata"`
	EntryPoint *string         `json:"entry_point"`
	// EnsureNew, when true and NamedSlug is set, makes the server
	// reject the upload (409) if the slug already has any non-deleted
	// version. Lets callers assert "this is a brand-new doc" so they
	// don't accidentally append v2 onto someone else's slug. Default
	// false preserves the existing auto-version-up behavior.
	EnsureNew bool `json:"ensure_new"`
	// AllowedAccess — glob-on-email patterns that gate read access.
	// nil (field absent) → inherit from prior version if any, else
	// server default `['*']` (everyone authenticated). Empty slice
	// → creator-only. See pgstore.buildWhere for matching semantics.
	AllowedAccess *[]string `json:"allowed_access,omitempty"`
	// AllowedWrite — tokens allowed to write (subset of AllowedAccess; unioned
	// in on save). nil (field absent) → write follows read; empty slice →
	// creator-only writes.
	AllowedWrite *[]string `json:"allowed_write,omitempty"`

	// rawContent carries binary bytes from a multipart upload, bypassing the
	// base64 round-trip. Set only by httpCreate's multipart branch; never
	// deserialized from JSON.
	rawContent []byte
}

// AppendRequest is the wire shape for POST /api/artifacts/by-slug/{slug}/append.
// Content is plain UTF-8 text; binary content can't be appended (use a
// fresh Create or a per-write artifact instead).
//
// IdempotencyKey lets clients retry safely: server caches the first
// successful response keyed by (key, creator) for 24h and replays it on
// retry. Recommended for any agent-driven write that might be retried
// after a crash or timeout. Stable hash of the logical event (e.g.
// `sha256(author + verbatim_body + posted_at)`) works well.
//
// Separator is inserted between the existing body and the new content
// (default "\n\n"). Pass "" explicitly for no separator.
//
// Auto-create: if Slug doesn't exist yet, the first append creates v1.
// Title + ContentType are required in that path (they're used to seed
// the new artifact); on subsequent appends both are optional overrides.
type AppendRequest struct {
	Content        string   `json:"content"`                   // raw text to append (UTF-8)
	Separator      *string  `json:"separator,omitempty"`       // default "\n\n"; pass "" for none
	IdempotencyKey string   `json:"idempotency_key,omitempty"` // optional, dedup key for retries
	Title          *string  `json:"title,omitempty"`           // required on auto-create; optional override otherwise
	Description    *string  `json:"description,omitempty"`     // optional override
	ContentType    string   `json:"content_type,omitempty"`    // required on auto-create; optional override otherwise
	Scopes         []string `json:"scopes,omitempty"`          // optional override; nil → inherit
	Labels         []string `json:"labels,omitempty"`          // optional override; nil → inherit
	// AllowedAccess: nil → inherit from prior version; empty slice → creator-only.
	AllowedAccess *[]string `json:"allowed_access,omitempty"`
	// AllowedWrite: nil → inherit prior version's write list; empty slice →
	// creator-only writes.
	AllowedWrite *[]string `json:"allowed_write,omitempty"`
}

// ListResponse is the catalog / search shape.
type ListResponse struct {
	Artifacts []ArtifactInfo `json:"artifacts"`
	Total     int64          `json:"total"`
}

// VersionsResponse mirrors /api/artifacts/by-slug/{slug}/versions.
type VersionsResponse struct {
	Versions []ArtifactInfo `json:"versions"`
}

// PackageManifestResponse is the body of /files.
type PackageManifestResponse struct {
	Entries    []PackageEntry `json:"entries"`
	EntryPoint string         `json:"entry_point,omitempty"`
}

type PackageEntry struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256,omitempty"`
	ContentType string `json:"content_type"`
}

type Error struct {
	Detail string `json:"detail"`
	Code   string `json:"code"`
}
