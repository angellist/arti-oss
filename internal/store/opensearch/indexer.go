package opensearch

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"time"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Indexer bridges pgstore artifacts and the OpenSearch index. It knows
// how to convert a sqlc.Artifact row (plus its content) into an OpenSearch
// Doc and push it to the cluster. Callers that don't have content readily
// available can use IndexMeta (metadata only, no content_text).
type Indexer struct {
	client *Client
	store  *pgstore.Store
	logger *slog.Logger
}

// NewIndexer returns a ready indexer, or nil when the client is disabled.
func NewIndexer(client *Client, store *pgstore.Store, logger *slog.Logger) *Indexer {
	if client == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Indexer{client: client, store: store, logger: logger}
}

// Enabled reports whether the indexer is wired to a live cluster.
func (ix *Indexer) Enabled() bool { return ix != nil && ix.client.Enabled() }

// IndexArtifact converts a sqlc.Artifact to an OpenSearch doc and indexes it.
// For TEXT artifacts it fetches the content and indexes the body text.
// For PACKAGE/APP it indexes the file listing from metadata.
// Errors are logged but not returned — indexing is best-effort to avoid
// blocking writes.
func (ix *Indexer) IndexArtifact(ctx context.Context, row sqlc.Artifact) {
	if ix == nil {
		return
	}
	doc := rowToDoc(row)

	// An artifact labelled index:skip-fulltext keeps every metadata field —
	// title, description, labels, slug, creator — and only withholds its body
	// from content_text, so it stays findable by name but its contents can't
	// match a free-text query. This exists for machine-state artifacts: the
	// publisher state doc `couch-skill-publish-state` is ~315 KB of sha256s and
	// UUIDs, re-written daily, and every version was indexed in full — so a
	// search for a bare hash returned it, and the index carried a 256 KiB copy
	// per version of something no human ever full-text searches.
	//
	// Skipping also avoids the content read entirely, which for an over-inline
	// (>64 KiB) TEXT artifact is an S3 GET on every index write.
	if SkipFullText(doc.Labels) {
		ix.logger.Debug("opensearch: skipping content_text", "id", doc.ArtifactID, "label", SkipFullTextLabel)
	} else {
		switch row.ArtifactType {
		case pgstore.TypeText:
			body, err := ix.readContent(ctx, row)
			if err != nil {
				ix.logger.Warn("opensearch: read content for indexing", "id", doc.ArtifactID, "err", err)
			} else {
				doc.ContentText = ExtractText(body, row.ContentType)
			}
		case pgstore.TypePackage, pgstore.TypeApp:
			doc.ContentText = extractPackageText(row)
		}
	}

	if err := ix.client.Index(ctx, doc); err != nil {
		ix.logger.Error("opensearch: index artifact", "id", doc.ArtifactID, "err", err)
	}
}

// MarkDeleted updates the is_deleted flag in the index.
func (ix *Indexer) MarkDeleted(ctx context.Context, artifactID string, deleted bool) {
	if ix == nil {
		return
	}
	if err := ix.client.MarkDeleted(ctx, artifactID, deleted); err != nil {
		ix.logger.Error("opensearch: mark deleted", "id", artifactID, "err", err)
	}
}

// MarkSlugDeleted sets is_deleted=true on every indexed document for a slug
// via update-by-query. Fallback for ArchiveBySlug when the per-version IDs
// couldn't be read from Postgres before archiving — without it those docs keep
// is_deleted:false and leak into non-archived search until the next reindex.
func (ix *Indexer) MarkSlugDeleted(ctx context.Context, slug string) {
	if ix == nil || slug == "" {
		return
	}
	path := "/" + IndexName + "/_update_by_query"
	body := map[string]any{
		"script": map[string]any{
			"source": "ctx._source.is_deleted = true",
			"lang":   "painless",
		},
		"query": map[string]any{
			"term": map[string]any{"named_slug": slug},
		},
	}
	resp, uerr := ix.client.do(ctx, "POST", path, body)
	if uerr != nil {
		ix.logger.Warn("opensearch: mark slug deleted", "slug", slug, "err", uerr)
	} else if _, berr := readBody(resp); berr != nil {
		ix.logger.Warn("opensearch: mark slug deleted", "slug", slug, "err", berr)
	}
}

// HardDelete removes the document from the index entirely.
func (ix *Indexer) HardDelete(ctx context.Context, artifactID string) {
	if ix == nil {
		return
	}
	if err := ix.client.Delete(ctx, artifactID); err != nil {
		ix.logger.Error("opensearch: hard delete", "id", artifactID, "err", err)
	}
}

// latestFlagAttempts bounds the version-conflict retry loop in
// UpdateLatestFlags; latestFlagBackoff is the first retry's delay (doubled
// each attempt, so 4 attempts sleep at most 100+200+400 ms total).
const (
	latestFlagAttempts = 4
	latestFlagBackoff  = 100 * time.Millisecond
)

// UpdateLatestFlags re-computes the is_latest flag for all versions under
// a slug. Called after Put/Append to ensure only the highest non-deleted
// version has is_latest=true (for the LatestPerSlug filter).
//
// Concurrent writers race this: every IndexArtifact bumps its doc's seqNo, so
// the update-by-query can hit a version conflict (HTTP 409). The old
// clear-then-set implementation aborted outright on that (conflicts defaults
// to abort), leaving is_latest set on MORE than one version — a stale version
// could be served as "latest" by LatestPerSlug search until the next write of
// the slug. Observed in prod ~33×/day, mostly on high-churn slugs racing
// themselves (mcp-feed-watermark). Now the flags are written in ONE
// conflict-tolerant update-by-query, retried with backoff while conflicts
// remain, re-reading the version list from Postgres each attempt so the last
// write always reflects the freshest state.
func (ix *Indexer) UpdateLatestFlags(ctx context.Context, slug string) {
	if ix == nil || slug == "" {
		return
	}
	for attempt := 0; ; attempt++ {
		versions, err := ix.store.Versions(ctx, slug)
		if err != nil {
			ix.logger.Warn("opensearch: fetch versions for latest flags", "slug", slug, "err", err)
			return
		}
		// No live versions (all archived/deleted) ⇒ latestID "" matches no
		// doc _id and the script clears is_latest everywhere for the slug.
		latestID := ""
		if len(versions) > 0 {
			latestID = pgstore.UUIDFromPG(versions[0].ArtifactID).String()
		}
		conflicts, err := ix.client.SetLatestFlag(ctx, slug, latestID)
		if err != nil {
			ix.logger.Warn("opensearch: update latest flags", "slug", slug, "err", err)
			return
		}
		if conflicts == 0 {
			return
		}
		if attempt == latestFlagAttempts-1 {
			ix.logger.Warn("opensearch: update latest flags: version conflicts persisted",
				"slug", slug, "attempts", latestFlagAttempts, "conflicts", conflicts)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(latestFlagBackoff << attempt):
		}
	}
}

func (ix *Indexer) readContent(ctx context.Context, row sqlc.Artifact) ([]byte, error) {
	rc, err := ix.store.Content(ctx, row)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func rowToDoc(row sqlc.Artifact) Doc {
	id := pgstore.UUIDFromPG(row.ArtifactID).String()
	return Doc{
		ArtifactID:    id,
		ArtifactType:  row.ArtifactType,
		NamedSlug:     row.NamedSlug,
		Version:       row.Version,
		Title:         row.Title,
		Description:   row.Description,
		ContentType:   row.ContentType,
		Creator:       strings.ToLower(row.Creator),
		Scopes:        orEmpty(row.Scopes),
		Labels:        orEmpty(row.Labels),
		AllowedAccess: lowerAll(orEmpty(row.AllowedAccess)),
		CreatedAt:     row.CreatedAt.Time,
		ModifiedAt:    row.ModifiedAt.Time,
		IsDeleted:     row.DeletedAt.Valid,
		// Every write indexes is_latest:true; UpdateLatestFlags then clears the
		// flag on older versions asynchronously. During that window a slug can
		// briefly have >1 doc with is_latest:true, so a LatestPerSlug search can
		// return duplicate versions of a slug until the flags settle — unlike
		// Postgres, which collapses to MAX(version) in SQL (cursor-bot).
		// TODO(arti#87): make LatestPerSlug robust to flag lag via OpenSearch
		// field collapsing on named_slug (top hit by version desc) instead of
		// relying on the is_latest flag.
		IsLatest:  true,
		SizeBytes: row.SizeBytes,
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// lowerAll returns a lowercased copy of the slice, never nil. Used for
// allowed_access so the OpenSearch exact-term access filter (which queries
// with a lowercased caller email) matches entries regardless of the case
// they were stored with, consistent with pgstore.CanAccess (case-insensitive)
// and how creator is lowercased at index time. Group tokens are already
// canonical-lowercase and `*` is unaffected.
func lowerAll(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = strings.ToLower(v)
	}
	return out
}

// extractPackageText pulls searchable text from a PACKAGE/APP's metadata
// (file listing). We index the file paths.
func extractPackageText(row sqlc.Artifact) string {
	if len(row.Metadata) == 0 {
		return ""
	}
	var meta struct {
		Package struct {
			Entries []struct {
				Path string `json:"path"`
			} `json:"entries"`
		} `json:"package"`
	}
	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return ""
	}
	paths := make([]string, 0, len(meta.Package.Entries))
	for _, e := range meta.Package.Entries {
		paths = append(paths, e.Path)
	}
	return strings.Join(paths, "\n")
}
