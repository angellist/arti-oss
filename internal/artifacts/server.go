// Package artifacts implements the REST handler set + the shared
// service layer used by both HTTP and MCP surfaces.
package artifacts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/pkgzip"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/opensearch"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Service is the business-layer surface used by REST and MCP handlers.
// Methods are deliberately HTTP-agnostic — they take/return plain
// structs + sentinel errors, never `http.Request`.
type Service struct {
	store   *pgstore.Store
	baseURL string
	// embedToken, when set, mints a scoped comment token so the comments
	// overlay can be injected into served HTML pages. Wired in cmd_serve to
	// the comments service; nil disables injection.
	embedToken func(email, artifactID, name, picture string) (string, error)
	// appToken, when set, mints a scoped app token so the app bridge
	// (window.arti.callTool) can be injected into served APP pages. Wired in
	// cmd_serve to the apps service; nil disables the bridge. Also used (see
	// filesBaseFor) to mint the cookie-free sibling-asset path every rendered
	// PACKAGE/APP page needs in production.
	appToken func(email, artifactID string) (string, error)
	// appTokenVerify, when set, verifies a token minted by appToken and
	// returns the (email, artifactID) it authorizes. Wired in cmd_serve to the
	// apps service; backs the public files-token route (MountFileToken). nil
	// makes that route 404, and filesBaseFor falls back to the cookie path.
	appTokenVerify func(tok string) (email, artifactID string, err error)
	// embedFilesToken, when set, mints the email-less files token user-mode
	// embed surfaces use for sibling assets (there is no serve-time caller to
	// mint appToken for). Wired in cmd_serve to the apps service; nil makes
	// embedFilesBaseUser fall back to the cookie path (local dev).
	embedFilesToken func(artifactID string) (string, error)
	// meta, when set, lets POST /api/artifacts/suggest-metadata ask the
	// built-in LLM (haiku) to name/slug/label an upload from its content.
	// Wired in cmd_serve; nil falls back to filename-derived suggestions.
	meta MetadataCompleter
	// appFrameAncestors is the CSP `frame-ancestors` source list applied to
	// served APP HTML — it controls which origins may iframe an arti app (the
	// embedding use case). Empty ⇒ "'self'" (same-origin only, the prior
	// behavior); deployments widen it to internal origins. Scoped to APP
	// responses; regular artifacts keep X-Frame-Options: SAMEORIGIN.
	appFrameAncestors string

	// osClient is the OpenSearch HTTP client (nil = disabled).
	osClient *opensearch.Client
	// osIndexer syncs artifact writes to the OpenSearch index (nil = disabled).
	osIndexer *opensearch.Indexer
}

// SetEmbedTokenFn wires the comment-embed token minter (from the comments
// service). When set, served text/html package files get the comments
// overlay injected.
func (s *Service) SetEmbedTokenFn(fn func(email, artifactID, name, picture string) (string, error)) {
	s.embedToken = fn
}

// SetAppTokenFn wires the app-token minter (from the apps service). When set,
// served APP HTML gets the window.arti.callTool bridge injected.
func (s *Service) SetAppTokenFn(fn func(email, artifactID string) (string, error)) {
	s.appToken = fn
}

// SetAppTokenVerifyFn wires the app-token verifier (from the apps service),
// backing the public files-token route registered by MountFileToken.
func (s *Service) SetAppTokenVerifyFn(fn func(tok string) (email, artifactID string, err error)) {
	s.appTokenVerify = fn
}

// SetEmbedFilesTokenFn wires the user-mode embed files-token minter (from the
// apps service). See the embedFilesToken field.
func (s *Service) SetEmbedFilesTokenFn(fn func(artifactID string) (string, error)) {
	s.embedFilesToken = fn
}

// SetAppFrameAncestors sets the CSP frame-ancestors source list for served APP
// HTML, controlling which origins may embed an arti app in an iframe (e.g.
// "'self' https://*.example.com"). Empty keeps the same-origin-only
// default. Other artifact types are unaffected.
func (s *Service) SetAppFrameAncestors(v string) {
	s.appFrameAncestors = strings.TrimSpace(v)
}

func NewService(store *pgstore.Store, baseURL string, osClient *opensearch.Client, osIndexer *opensearch.Indexer) *Service {
	return &Service{
		store:     store,
		baseURL:   strings.TrimRight(baseURL, "/"),
		osClient:  osClient,
		osIndexer: osIndexer,
	}
}

// parseLegacyScope folds the deprecated singular `scope` input into a scopes
// slice, mirroring the backfill in 0005_scopes_add.sql: a JSON-array string
// expands to its elements, a JSON-quoted string unwraps, and anything else
// (a bare scope, or malformed JSON) is kept verbatim as a single scope.
// Elements are trimmed and empties dropped. This keeps an old client that
// still sends a mis-encoded `scope` from re-introducing the phantom bracket
// scope-type the migration heals. Removed in phase 2 with the `scope` field.
func parseLegacyScope(raw string) []string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return []string{}
	}
	clean := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, e := range in {
			if t := strings.TrimSpace(e); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	switch s[0] {
	case '[':
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return clean(arr)
		}
	case '"':
		var str string
		if err := json.Unmarshal([]byte(s), &str); err == nil {
			return clean([]string{str})
		}
	}
	// Bare value, or unparseable JSON — keep verbatim as a single scope.
	return []string{s}
}

// Create persists a new artifact (or a new version of an existing slug).
// `creator` is taken from the auth context by the HTTP layer; the
// service does not enforce identity.
func (s *Service) Create(ctx context.Context, req CreateRequest, creator string) (ArtifactInfo, error) {
	if creator == "" {
		return ArtifactInfo{}, errBadRequest("missing creator (auth required)")
	}
	if req.Title == "" || req.ContentType == "" {
		return ArtifactInfo{}, errBadRequest("title and content_type are required")
	}

	at := req.ArtifactType
	if at == "" {
		at = pgstore.TypeText
	}

	// TEXT artifacts must carry a textual content_type — otherwise the raw
	// bytes (a PDF, image, zip, …) render as garbage in the viewer. Non-text
	// single files belong in an ATTACHMENT; multi-file bundles in a PACKAGE.
	// (Per PR #32; the CLI auto-promotes when the caller didn't pick a type,
	// so this server-side rejection only fires when the caller explicitly
	// asked for TEXT with binary content.)
	if at == pgstore.TypeText && !isTextualContentType(req.ContentType) {
		return ArtifactInfo{}, errBadRequest(fmt.Sprintf(
			"content_type %q is not textual; a TEXT artifact must be text/* (or application/json, yaml, javascript). "+
				"Upload it as an ATTACHMENT, or inside a PACKAGE.", req.ContentType))
	}

	var content []byte
	switch {
	case req.rawContent != nil:
		content = req.rawContent
	case req.ContentBase64 != "":
		b, err := base64.StdEncoding.DecodeString(req.ContentBase64)
		if err != nil {
			return ArtifactInfo{}, errBadRequest("bad content_base64: " + err.Error())
		}
		content = b
	case req.Content != "":
		content = []byte(req.Content)
	default:
		return ArtifactInfo{}, errBadRequest("content (or content_base64) required")
	}

	meta := req.Metadata
	if pgstore.IsPackageLike(at) {
		// Normalize a single wrapping directory ("zip -r app.zip app/") to a
		// flat package BEFORE manifest/storage, so landing-page + arti-app.json
		// detection and the file rail all see root-level paths.
		flat, ferr := pkgzip.FlattenSingleRoot(content)
		if ferr != nil {
			return ArtifactInfo{}, errBadRequest("invalid zip: " + ferr.Error())
		}
		content = flat
		manifest, err := pkgzip.BuildManifest(content)
		if err != nil {
			return ArtifactInfo{}, errBadRequest("invalid zip: " + err.Error())
		}
		if req.EntryPoint != nil && *req.EntryPoint != "" {
			manifest.EntryPoint = *req.EntryPoint
		}
		// For an APP, arti-app.json's `entry` is the authoritative launch page.
		// Stamp it onto the package entry_point at upload so the viewer (file
		// rail) and full-page /app open the SAME HTML — no divergence.
		if at == pgstore.TypeApp {
			if mb, _, e := pkgzip.ReadEntry(content, "arti-app.json"); e == nil {
				var m struct {
					Entry string `json:"entry"`
				}
				if json.Unmarshal(mb, &m) == nil && m.Entry != "" {
					manifest.EntryPoint = m.Entry
				}
			}
		}
		meta = mergeMeta(meta, map[string]any{"package": manifest})
	}

	// Inherit scope, labels, and allowed_access from the previous version
	// under this slug when the caller didn't explicitly supply them.
	// Avoids the foot-gun where `arti add --slug foo file` silently drops
	// state set on v1 just because the user forgot to re-pass it. Caller
	// can still clear by sending an explicit empty value.
	//
	// EnsureNew gates the reverse foot-gun: caller asserts "this is a
	// brand-new doc, fail if the slug is taken." Checked first so we
	// don't waste a GetBySlug round-trip for the inherit path.
	// Scopes takes precedence; the deprecated singular Scope is folded in
	// only when Scopes is absent. The legacy value is parsed the same way the
	// migration heals stored scopes, so an old client still sending a
	// mis-encoded scope (e.g. a JSON-array string) doesn't re-introduce the
	// phantom bracket scope-type. Removed in phase 2.
	scopes := req.Scopes
	if scopes == nil && req.Scope != nil {
		scopes = parseLegacyScope(*req.Scope)
	}
	labels := req.Labels
	var prevLabels []string
	var access []string
	if req.AllowedAccess != nil {
		access = *req.AllowedAccess
	}
	var write []string
	if req.AllowedWrite != nil {
		write = *req.AllowedWrite
	}
	if req.NamedSlug != nil && *req.NamedSlug != "" {
		if prev, err := s.store.GetBySlug(ctx, *req.NamedSlug, nil); err == nil {
			// Versioning an existing slug requires WRITE access to it (creator,
			// admin, or a member of allowed_write / — in mirror mode —
			// allowed_access); otherwise a caller could push a new version onto
			// an artifact they can't write (or even see).
			if err := s.checkWriteAccess(ctx, prev, creator); err != nil {
				return ArtifactInfo{}, err
			}
			prevLabels = prev.Labels
			if req.EnsureNew {
				var v int32
				if prev.Version != nil {
					v = *prev.Version
				}
				return ArtifactInfo{}, slugConflict{
					slug:            *req.NamedSlug,
					existingVersion: v,
					creator:         prev.Creator,
				}
			}
			if scopes == nil {
				scopes = prev.Scopes
			}
			if labels == nil {
				labels = prev.Labels
			}
			if req.AllowedAccess == nil {
				access = prev.AllowedAccess
			}
			if req.AllowedWrite == nil {
				write = prev.AllowedWrite
			}
			// The ACL-change authority check runs in the CheckAccess hook below
			// (against a FRESH prev, so a concurrent version can't be straddled),
			// validating the resolved access/write we're about to persist.
		}
		// Errors (e.g. no prior version) leave nil-as-nil and proceed;
		// Store.Put normalizes labels to empty and access to ['*'].
	}

	// Skill artifacts (kind:skill) are write-guarded — create or version only by
	// MANAGE_SKILLS / MANAGE_ARTIFACTS holders. Check the final labels AND the
	// prior version's labels, so versioning a skill while dropping the tag from
	// the request still trips the guard.
	if err := s.requireSkillWrite(ctx, creator, labels, prevLabels); err != nil {
		return ArtifactInfo{}, err
	}

	row, err := s.store.Put(ctx, pgstore.PutInput{
		ArtifactType:  at,
		NamedSlug:     req.NamedSlug,
		Title:         req.Title,
		Description:   req.Description,
		ContentType:   req.ContentType,
		Content:       content,
		Creator:       creator,
		Scopes:        scopes,
		Labels:        labels,
		Metadata:      meta,
		AllowedAccess: access,
		AllowedWrite:  write,
		// Closes the race the check above can't: if the slug looked absent
		// on our own pre-check but a concurrent writer created it (as a
		// restricted artifact) before this call landed, Put re-checks
		// access itself against a fresh read taken right before it writes.
		CheckAccess: func(ctx context.Context, prev sqlc.Artifact) error {
			if err := s.checkWriteAccess(ctx, prev, creator); err != nil {
				return err
			}
			// ACL-change authority against the FRESH prev (owner/admin only),
			// validating the access/write about to be persisted — race-safe.
			return s.requireAclChangeAuthority(ctx, creator, *req.NamedSlug, prev, access, write)
		},
	})
	if err != nil {
		return ArtifactInfo{}, fmt.Errorf("create: %w", err)
	}

	// Sync to OpenSearch (best-effort, fire-and-forget).
	if s.osIndexer.Enabled() {
		go func() {
			s.osIndexer.IndexArtifact(context.Background(), row)
			if req.NamedSlug != nil && *req.NamedSlug != "" {
				s.osIndexer.UpdateLatestFlags(context.Background(), *req.NamedSlug)
			}
		}()
	}

	return ToInfo(row, s.baseURL), nil
}

// Append appends content to an existing slug, creating a new version
// whose body is (prior_body || separator || new_content). If the slug
// doesn't exist yet, behaves like Create with the new content as the
// initial body (auto-create); in that path Title + ContentType are
// required.
//
// Idempotency: if req.IdempotencyKey is non-empty, the server first
// checks the (key, creator) idempotency table. On hit, the original
// response is replayed verbatim and no write happens. On miss, the
// write proceeds and the response is cached for 24h.
//
// Race policy: under concurrent appends on the same slug, the underlying
// (slug, version) unique constraint forces a retry loop; persistent
// contention surfaces as ErrConflict (mapped to HTTP 409).
func (s *Service) Append(ctx context.Context, slug string, req AppendRequest, creator string) (info ArtifactInfo, cached bool, err error) {
	if creator == "" {
		return ArtifactInfo{}, false, errBadRequest("missing creator (auth required)")
	}
	if slug == "" {
		return ArtifactInfo{}, false, errBadRequest("slug is required")
	}
	if req.Content == "" {
		return ArtifactInfo{}, false, errBadRequest("content is required")
	}
	if req.ContentType != "" && !isValidContentType(req.ContentType) {
		return ArtifactInfo{}, false, errBadRequest(
			"content_type must look like type/subtype with an optional charset")
	}
	if req.ContentType != "" && !isTextualContentType(req.ContentType) {
		return ArtifactInfo{}, false, errBadRequest(
			"append only supports text content_types (text/*, application/json, etc.); " +
				"for binary content use Create with artifact_type=ATTACHMENT")
	}

	// Idempotency replay check. The contract is "same (key, creator)
	// always sees the same response_artifact." Three outcomes to
	// distinguish:
	//   1. real DB error    → surface; safer to fail than to write again
	//                         because the failed lookup might be hiding an
	//                         existing record we'd duplicate (per cursor-bot
	//                         "Idempotency lookup errors ignored")
	//   2. cache miss       → proceed to the append
	//   3. cache hit        → re-fetch the cached artifact:
	//      3a. fetch ok     → replay verbatim
	//      3b. fetch failed → SURFACE the error rather than re-appending.
	//                         Falling through here used to loop: retry hits
	//                         the cache, fails the fetch, appends again,
	//                         until the 24h TTL expires (per cursor-bot
	//                         "Idempotency replay re-appends loop").
	if req.IdempotencyKey != "" {
		hit, gerr := s.store.GetIdempotency(ctx, req.IdempotencyKey, creator)
		switch {
		case gerr == nil:
			cachedInfo, ferr := s.Get(ctx, hit.ArtifactID, creator)
			if ferr == nil {
				return cachedInfo, true, nil
			}
			return ArtifactInfo{}, false, errIdempotencyStale(req.IdempotencyKey, hit.ArtifactID, ferr)
		case errors.Is(gerr, pgstore.ErrNotFound):
			// Genuine miss — fall through to the write.
		default:
			return ArtifactInfo{}, false, fmt.Errorf("idempotency lookup: %w", gerr)
		}
	}

	// Skill write-guard — placed AFTER the idempotency replay so a retry of an
	// already-cached append replays verbatim instead of being rejected (the
	// first, successful append already passed this check). Versioning OR
	// auto-creating a kind:skill artifact requires MANAGE_SKILLS /
	// MANAGE_ARTIFACTS; check the request's labels and the existing slug's
	// labels (if any).
	var prevLabels []string
	if prev, perr := s.store.GetBySlug(ctx, slug, nil); perr == nil {
		// Appending to an existing slug requires WRITE access to it — see the
		// matching check in Create.
		if err := s.checkWriteAccess(ctx, prev, creator); err != nil {
			return ArtifactInfo{}, false, err
		}
		// ACL-change authority is enforced race-safely in the Append
		// CheckAccess hook below (re-run against fresh prev each retry).
		prevLabels = prev.Labels
	}
	if err := s.requireSkillWrite(ctx, creator, req.Labels, prevLabels); err != nil {
		return ArtifactInfo{}, false, err
	}

	var sep []byte
	if req.Separator != nil {
		sep = []byte(*req.Separator)
	}

	row, perr := s.store.Append(ctx, pgstore.AppendInput{
		NamedSlug:     slug,
		Separator:     sep,
		Content:       []byte(req.Content),
		Creator:       creator,
		ContentType:   req.ContentType,
		Title:         req.Title,
		Description:   req.Description,
		Scopes:        req.Scopes,
		Labels:        req.Labels,
		AllowedAccess: req.AllowedAccess,
		AllowedWrite:  req.AllowedWrite,
		// Closes the race the check above can't: if the slug looked absent
		// on our own pre-check but a concurrent writer created it (as a
		// restricted artifact) before this call landed, the retry loop
		// inside Store.Append re-checks access itself the moment it
		// discovers that row.
		CheckAccess: func(ctx context.Context, prev sqlc.Artifact) error {
			if err := s.checkWriteAccess(ctx, prev, creator); err != nil {
				return err
			}
			fa := prev.AllowedAccess
			if req.AllowedAccess != nil {
				fa = *req.AllowedAccess
			}
			fw := prev.AllowedWrite
			if req.AllowedWrite != nil {
				fw = *req.AllowedWrite
			}
			return s.requireAclChangeAuthority(ctx, creator, slug, prev, fa, fw)
		},
	})
	if perr != nil {
		if errors.Is(perr, pgstore.ErrConflict) {
			return ArtifactInfo{}, false, perr
		}
		// Client-mistake errors (missing required field, appending to
		// PACKAGE/ATTACHMENT) come back as pgstore.ErrInvalidInput so the
		// HTTP layer can return 400 instead of 500 (per cursor-bot
		// "Append validation returns HTTP 500"). Preserve the underlying
		// message so the caller sees the specific reason.
		if errors.Is(perr, pgstore.ErrInvalidInput) {
			return ArtifactInfo{}, false, errBadRequest(perr.Error())
		}
		return ArtifactInfo{}, false, fmt.Errorf("append: %w", perr)
	}

	// Sync to OpenSearch (best-effort, fire-and-forget). This must run
	// before the idempotency bookkeeping below, whose early returns (e.g.
	// RecordIdempotency error, or returning a race winner's response) would
	// otherwise skip indexing a row that IS committed in Postgres, leaving
	// it unsearchable until the next reindex.
	if s.osIndexer.Enabled() {
		go func() {
			s.osIndexer.IndexArtifact(context.Background(), row)
			s.osIndexer.UpdateLatestFlags(context.Background(), slug)
		}()
	}

	// Record the (key, creator) → response mapping, then converge on the
	// winner if we lost a concurrent-record race (per cursor-bot
	// "Concurrent idempotency double append"). The slug may have BOTH
	// writes physically committed — that's the cost we accept for not
	// running a 2-phase reserve/finalize, since the actual collision rate
	// is tiny — but the idempotency layer always returns ONE canonical
	// response so retries don't see ping-ponging artifact_ids.
	if req.IdempotencyKey != "" {
		artifactID, perr := uuid.Parse(pgstore.UUIDFromPG(row.ArtifactID).String())
		if perr == nil {
			inserted, rerr := s.store.RecordIdempotency(ctx, req.IdempotencyKey, creator, artifactID, row.Version)
			if rerr != nil {
				// Couldn't record — log via the returned info but don't fail
				// the write. Worst case: future retries with the same key
				// re-append (which is what the un-keyed path does anyway).
				return ToInfo(row, s.baseURL), false, nil
			}
			if !inserted {
				// Another writer beat us to it. Look up their row and
				// return THEIR artifact so all callers with this key see
				// the same response. Our just-written row is an orphan
				// version in the slug history.
				winner, werr := s.store.GetIdempotency(ctx, req.IdempotencyKey, creator)
				if werr == nil {
					winnerInfo, ferr := s.Get(ctx, winner.ArtifactID, creator)
					if ferr == nil {
						return winnerInfo, true, nil
					}
				}
				// Fall through and return our own response on lookup
				// failure; degraded but the write did succeed.
			}
		}
	}

	return ToInfo(row, s.baseURL), false, nil
}

// errIdempotencyStale signals an idempotency-cache hit whose recorded
// artifact is no longer accessible (archived, deleted, or access
// revoked). HTTP layer maps to 410 Gone so the caller knows to pick a
// fresh idempotency key instead of looping on the stale row until the
// 24h TTL clears.
type idempotencyStale struct {
	key        string
	artifactID uuid.UUID
	cause      error
}

func (e idempotencyStale) Error() string {
	return fmt.Sprintf("idempotency key %q points to artifact %s which is no longer accessible: %v",
		e.key, e.artifactID, e.cause)
}

func errIdempotencyStale(key string, artifactID uuid.UUID, cause error) error {
	return idempotencyStale{key: key, artifactID: artifactID, cause: cause}
}

// (isTextualContentType — the textual-vs-binary guard — is defined
// near the other content-type helpers below, alongside isValidContentType.)

// checkAccess returns nil if caller can read row, ErrNotFound otherwise.
// Admins always pass. Returns 404 (not 403) so non-admin callers can't
// probe existence of restricted artifacts.
//
// Also used to gate versioning an existing slug (Create/Append): read and
// write access are the same thing — anyone who can see a version can push
// the next one. Metadata edits (title/labels/scopes/allowed_access) and
// archive/unarchive stay creator-or-admin only; this does not touch those.
//
// Archive policy: non-admins can't see soft-deleted artifacts EVEN IF
// they would otherwise have access — only the creator (and admins) get
// to view archived versions. This matches the rule confirmed by the
// product owner: "normal people cannot see archived docs that's not
// their own, even if they HAD access." A side effect here: a non-creator
// can't version an archived slug either, since checkAccess denies them
// the row entirely.
func (s *Service) checkAccess(ctx context.Context, row sqlc.Artifact, caller string) error {
	if ok, err := s.canManageArtifacts(ctx, caller); err != nil {
		return err
	} else if ok {
		return nil
	}
	if row.DeletedAt.Valid && !strings.EqualFold(row.Creator, caller) {
		return pgstore.ErrNotFound
	}
	groups, err := s.store.CallerGroups(ctx, caller)
	if err != nil {
		// A real lookup failure must not silently deny (which a non-admin
		// would see as 404); surface it so the handler returns 500.
		return err
	}
	if pgstore.CanAccess(row, caller, groups) {
		return nil
	}
	return pgstore.ErrNotFound
}

// aclChanged reports whether the resolved (finalAccess, finalWrite) differ from
// the prior version's EFFECTIVE ACL. finalAccess/finalWrite are what the store
// will persist (request value, or the prior version's when omitted). Because
// the store unions the write list into allowed_access (the ⊆ invariant), the
// comparison mirrors that union, so re-sending the same logical ACL (or the
// CLI's default_access) reads as a no-op, not a change.
func aclChanged(prev sqlc.Artifact, finalAccess, finalWrite []string) bool {
	eff := finalAccess
	if finalWrite != nil {
		eff = append(append([]string{}, finalAccess...), finalWrite...)
	}
	if !sameTokenSet(eff, prev.AllowedAccess) {
		return true
	}
	// Write list: nil (mirror) vs non-nil (explicit) is itself a change.
	return (finalWrite == nil) != (prev.AllowedWrite == nil) || !sameTokenSet(finalWrite, prev.AllowedWrite)
}

// requireAclChangeAuthority returns a forbidden error if the resolved ACL
// changes a slug's access and the caller is neither an admin nor the slug's
// OWNER (its earliest-version creator). A no-op is always allowed, so delegated
// writers can still publish content.
//
// Authority is the immutable slug owner, NOT prev.Creator: versioning reassigns
// each version's creator to whoever pushed it, so a delegated writer could push
// a content-only version, become the latest version's creator, and then pass a
// per-version creator check (on Create/Append OR PATCH) to escalate the ACL.
// SlugCreator pins authority to the original creator, closing that on every
// mutation path. Callers run this against a FRESH prev (inside the store's
// race-checking hook, or the loaded row for PATCH) so a concurrent ACL change
// can't be straddled.
func (s *Service) requireAclChangeAuthority(ctx context.Context, caller, slug string, prev sqlc.Artifact, finalAccess, finalWrite []string) error {
	if !aclChanged(prev, finalAccess, finalWrite) {
		return nil
	}
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return err
	}
	if admin {
		return nil
	}
	if slug != "" {
		owner, oerr := s.store.SlugCreator(ctx, slug)
		if oerr != nil && !errors.Is(oerr, pgstore.ErrNotFound) {
			return oerr
		}
		if owner != "" && strings.EqualFold(owner, caller) {
			return nil
		}
	}
	return errForbidden("changing access requires being the artifact's owner or an admin; omit allowed_access/allowed_write to keep the current access, or ask the owner to change it")
}

// sameTokenSet reports whether two token lists are set-equal (order- and
// duplicate-insensitive) — used to decide whether a version request actually
// changes an ACL versus re-sending the same values.
func sameTokenSet(a, b []string) bool {
	am := make(map[string]struct{}, len(a))
	for _, x := range a {
		am[x] = struct{}{}
	}
	bm := make(map[string]struct{}, len(b))
	for _, x := range b {
		bm[x] = struct{}{}
	}
	if len(am) != len(bm) {
		return false
	}
	for k := range am {
		if _, ok := bm[k]; !ok {
			return false
		}
	}
	return true
}

// checkWriteAccess gates content WRITES (new version / append / edit). Same
// shape as checkAccess — admins pass, archived rows stay creator-only, a real
// lookup error surfaces rather than silently 404ing — but it uses CanWrite:
// when allowed_write is set it is authoritative, otherwise write follows read.
func (s *Service) checkWriteAccess(ctx context.Context, row sqlc.Artifact, caller string) error {
	if ok, err := s.canManageArtifacts(ctx, caller); err != nil {
		return err
	} else if ok {
		return nil
	}
	if row.DeletedAt.Valid && !strings.EqualFold(row.Creator, caller) {
		return pgstore.ErrNotFound
	}
	groups, err := s.store.CallerGroups(ctx, caller)
	if err != nil {
		return err
	}
	if pgstore.CanWrite(row, caller, groups) {
		return nil
	}
	return pgstore.ErrNotFound
}

// Get returns the metadata DTO for one artifact. Returns ErrNotFound
// if `caller` lacks access — surfaced as 404 by the HTTP layer.
func (s *Service) Get(ctx context.Context, id uuid.UUID, caller string) (ArtifactInfo, error) {
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return ArtifactInfo{}, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return ArtifactInfo{}, err
	}
	return s.infoWithWrite(ctx, row, caller), nil
}

// infoWithWrite builds the DTO and stamps CanWrite from the SAME gate the write
// endpoints use (checkWriteAccess == nil), so the viewer's Edit affordance
// matches exactly what the server will allow — no client-side re-derivation of
// creator/admin/group/idp logic. Only for single-artifact caller-aware paths.
func (s *Service) infoWithWrite(ctx context.Context, row sqlc.Artifact, caller string) ArtifactInfo {
	info := ToInfo(row, s.baseURL)
	cw := s.checkWriteAccess(ctx, row, caller) == nil
	// Versioning a kind:skill artifact additionally requires the skill
	// write-guard (MANAGE_SKILLS / MANAGE_ARTIFACTS); reflect that here so the
	// viewer doesn't offer Edit to a plain writer whose Create would 403.
	if cw {
		if err := s.requireSkillWrite(ctx, caller, row.Labels); err != nil {
			cw = false
		}
	}
	info.CanWrite = &cw
	return info
}

// GetBySlug returns the metadata DTO for the slug. For non-admin callers
// with version==nil, picks the latest version the caller can read
// (silently hiding restricted newer versions). Pinned version requests
// still 404 if the caller lacks access to that exact version.
func (s *Service) GetBySlug(ctx context.Context, slug string, version *int32, caller string) (ArtifactInfo, error) {
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return ArtifactInfo{}, err
	}
	if version == nil && !admin {
		row, err := s.store.GetLatestBySlugForCaller(ctx, slug, caller)
		if err != nil {
			return ArtifactInfo{}, err
		}
		return s.infoWithWrite(ctx, row, caller), nil
	}
	row, err := s.store.GetBySlug(ctx, slug, version)
	if err != nil {
		return ArtifactInfo{}, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return ArtifactInfo{}, err
	}
	return s.infoWithWrite(ctx, row, caller), nil
}

// Versions returns every non-deleted version under a slug that `caller`
// can see. Admins see all versions including archived.
func (s *Service) Versions(ctx context.Context, slug string, caller string) ([]ArtifactInfo, error) {
	rows, err := s.store.Versions(ctx, slug)
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactInfo, 0, len(rows))
	for _, r := range rows {
		if err := s.checkAccess(ctx, r, caller); err != nil {
			// ErrNotFound means "caller can't see this version" — skip it.
			// Any other error (e.g. a group-resolution DB failure) is real
			// and must surface as a 500, not a silently truncated list.
			if errors.Is(err, pgstore.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, ToInfo(r, s.baseURL))
	}
	return out, nil
}

// List + Search return ListResponse-shaped data.
//
// `app:couch`-scoped artifacts (the couch app's private state) are hidden from
// the catalog for non-admins here, at the service layer, so every surface
// (REST, MCP, future callers) is covered uniformly — CallerEmail is empty only
// for admins (see each surface's callerForList), so a non-empty caller means
// "hide app:couch". Ordinary attachments are visible; admins still search
// everything.
func (s *Service) List(ctx context.Context, in pgstore.ListInput) (ListResponse, error) {
	hide, err := s.appCouchHidden(ctx, in.CallerEmail)
	if err != nil {
		return ListResponse{}, err
	}
	in.HideAppCouch = hide
	res, err := s.store.List(ctx, in)
	if err != nil {
		return ListResponse{}, err
	}
	return s.listResp(ctx, res), nil
}

func (s *Service) Search(ctx context.Context, q string, in pgstore.ListInput) (ListResponse, error) {
	hide, herr := s.appCouchHidden(ctx, in.CallerEmail)
	if herr != nil {
		return ListResponse{}, herr
	}
	in.HideAppCouch = hide

	// When OpenSearch is available and there's free-text, route through it
	// for BM25 relevance ranking + content search. Fall back to Postgres on
	// error or when OpenSearch is disabled. An explicit sort (in.OrderBy) is
	// honored only by the Postgres path — OpenSearch ranks by relevance and
	// can't, so a caller asking to sort skips it (label/scope-only q=="" queries
	// already go to Postgres).
	//
	// The (latest-per-slug + include-archived) combination also stays on
	// Postgres: OpenSearch collapses via a precomputed `is_latest` flag that
	// only ever marks the latest *non-deleted* version, so it can't surface an
	// archived latest version. Postgres computes the latest over the
	// archive-inclusive candidate set, which is the correct composition.
	if q != "" && in.OrderBy == "" && s.osClient.Enabled() && !(in.LatestPerSlug && in.IncludeArchived) {
		osResult, err := s.osSearch(ctx, q, in)
		if err == nil {
			return osResult, nil
		}
		slog.Warn("opensearch search path failed, falling back to postgres", "query", q, "err", err)
	}

	res, err := s.store.Search(ctx, q, in)
	if err != nil {
		return ListResponse{}, err
	}
	return s.listResp(ctx, res), nil
}

// osSearch executes the query via OpenSearch and fetches full rows from Postgres.
func (s *Service) osSearch(ctx context.Context, q string, in pgstore.ListInput) (ListResponse, error) {
	osIn := opensearch.SearchInput{
		Q:               q,
		ArtifactType:    in.ArtifactType,
		ContentType:     in.ContentType,
		Creator:         in.Creator,
		Scope:           in.Scope,
		Slug:            in.Slug,
		Labels:          in.Labels,
		NotArtifactType: in.NotArtifactType,
		NotCreator:      in.NotCreator,
		NotScope:        in.NotScope,
		NotLabels:       in.NotLabels,
		NotContentType:  in.NotContentType,
		CallerEmail:     in.CallerEmail,
		CallerGroups:    in.CallerGroups,
		IncludeArchived: in.IncludeArchived,
		OnlyArchived:    in.OnlyArchived,
		LatestPerSlug:   in.LatestPerSlug,
		HideAppCouch:    in.HideAppCouch,
		Limit:           int(in.Limit),
		Offset:          int(in.Offset),
	}

	osResult, err := s.osClient.Search(ctx, osIn)
	if err != nil {
		return ListResponse{}, err
	}

	if len(osResult.Hits) == 0 {
		return ListResponse{Total: osResult.Total, Artifacts: []ArtifactInfo{}}, nil
	}

	// Fetch full rows from Postgres by ID, preserving OpenSearch's relevance order.
	ids := make([]uuid.UUID, 0, len(osResult.Hits))
	scoreMap := make(map[string]float64, len(osResult.Hits))
	highlightMap := make(map[string]map[string][]string, len(osResult.Hits))
	for _, h := range osResult.Hits {
		id, perr := uuid.Parse(h.ArtifactID)
		if perr != nil {
			continue
		}
		ids = append(ids, id)
		scoreMap[h.ArtifactID] = h.Score
		if len(h.Highlights) > 0 {
			highlightMap[h.ArtifactID] = h.Highlights
		}
	}

	rows, err := s.store.GetByIDs(ctx, ids)
	if err != nil {
		return ListResponse{}, err
	}

	// Build a map for ordering and attach scores/highlights.
	rowMap := make(map[string]sqlc.Artifact, len(rows))
	for _, r := range rows {
		rowMap[pgstore.UUIDFromPG(r.ArtifactID).String()] = r
	}

	// Total starts from OpenSearch's hit count and is decremented for rows
	// dropped below by the stale-index re-checks (access/archive). The OS query
	// already applies the same access, archive, and latest-per-slug filters, so
	// in steady state Total is accurate and the page is full; decrements only
	// happen when the index lags Postgres. Known limitations under a stale index
	// (cursor-bot): Total only corrects for the current page's dropped rows, not
	// dropped rows on other pages; and a page can return fewer than `limit` rows
	// without back-filling from lower-ranked hits.
	// TODO(arti#87): if stale-window accuracy matters, over-fetch (request >limit),
	// drop, then trim to limit, and compute Total via a filtered count.
	out := ListResponse{Total: osResult.Total, Artifacts: make([]ArtifactInfo, 0, len(ids))}
	for _, id := range ids {
		idStr := id.String()
		row, ok := rowMap[idStr]
		if !ok {
			out.Total--
			continue
		}
		// Re-verify access against Postgres state in case the OpenSearch
		// index is stale (e.g. allowed_access was narrowed after indexing).
		if in.CallerEmail != "" && !pgstore.CanAccess(row, in.CallerEmail, in.CallerGroups) {
			out.Total--
			continue
		}
		// Re-verify archive state against Postgres too: the index updates
		// asynchronously (MarkDeleted runs in a goroutine), so a row archived
		// after its last index write can still carry is_deleted:false in
		// OpenSearch. Match the Postgres list path (pgstore buildWhere:
		// `deleted_at IS NULL` when !IncludeArchived) — which hides archived
		// rows from EVERYONE, creator included — rather than checkAccess's
		// single-get rule. A creator-visible exception here would let a stale
		// index surface a creator's archived rows in search when Postgres
		// would not.
		if !in.IncludeArchived && !in.OnlyArchived && row.DeletedAt.Valid {
			out.Total--
			continue
		}
		// Symmetric check for archived-only queries: a stale index can carry
		// is_deleted:true for a row that Postgres shows as live (e.g. unarchived
		// after its last index write), which would leak a non-archived row into
		// OnlyArchived results. Postgres uses `deleted_at IS NOT NULL` here, so
		// drop rows that aren't actually archived.
		if in.OnlyArchived && !row.DeletedAt.Valid {
			out.Total--
			continue
		}
		info := ToInfo(row, s.baseURL)
		info.Score = scoreMap[idStr]
		info.Highlights = highlightMap[idStr]
		out.Artifacts = append(out.Artifacts, info)
	}
	// Same annotation the Postgres list path applies, so the catalog's comment
	// column doesn't blank out the moment a query routes through OpenSearch.
	s.fillCommentCounts(ctx, out.Artifacts)
	return out, nil
}

func (s *Service) listResp(ctx context.Context, res pgstore.ListResult) ListResponse {
	out := ListResponse{Total: res.Total, Artifacts: make([]ArtifactInfo, 0, len(res.Rows))}
	for _, r := range res.Rows {
		out.Artifacts = append(out.Artifacts, ToInfo(r, s.baseURL))
	}
	s.fillCommentCounts(ctx, out.Artifacts)
	return out
}

// fillCommentCounts annotates a page of list/search results with their comment
// activity, in ONE query for the whole page (never per row — that is the N+1
// this exists to avoid). The catalog renders it as an optional column.
//
// Best-effort by design: a failure here leaves the counts nil and the column
// renders as "—". A comment-count widget is not worth failing a catalog page
// over, and the artifacts themselves are already fetched and correct.
func (s *Service) fillCommentCounts(ctx context.Context, infos []ArtifactInfo) {
	if len(infos) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(infos))
	for _, a := range infos {
		id, err := uuid.Parse(a.ArtifactID)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	counts, err := s.store.CommentCounts(ctx, ids)
	if err != nil {
		slog.Warn("comment counts for catalog page failed", "err", err, "rows", len(infos))
		return
	}
	for i := range infos {
		id, err := uuid.Parse(infos[i].ArtifactID)
		if err != nil {
			continue
		}
		// Zero is meaningful ("no discussion yet"), so emit it for every row
		// rather than only for rows present in the map — otherwise the client
		// cannot tell "no comments" from "counts unavailable".
		c := counts[id]
		comments, open := c.Comments, c.OpenThreads
		infos[i].CommentCount = &comments
		infos[i].OpenThreadCount = &open
	}
}

// Content returns a reader + content-type for the artifact body.
// Caller closes the reader. PACKAGE artifacts return the raw zip.
// 404s for callers without access.
func (s *Service) Content(ctx context.Context, id uuid.UUID, caller string) (io.ReadCloser, string, sqlc.Artifact, error) {
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, "", row, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return nil, "", row, err
	}
	rc, err := s.store.Content(ctx, row)
	if err != nil {
		return nil, "", row, err
	}
	return rc, row.ContentType, row, nil
}

// ContentBySlug is the slug variant of Content. version==nil for a
// non-admin caller returns the latest version the caller can read.
func (s *Service) ContentBySlug(ctx context.Context, slug string, version *int32, caller string) (io.ReadCloser, string, sqlc.Artifact, error) {
	var row sqlc.Artifact
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return nil, "", row, err
	}
	if version == nil && !admin {
		row, err = s.store.GetLatestBySlugForCaller(ctx, slug, caller)
	} else {
		row, err = s.store.GetBySlug(ctx, slug, version)
		if err == nil {
			err = s.checkAccess(ctx, row, caller)
		}
	}
	if err != nil {
		return nil, "", row, err
	}
	rc, err := s.store.Content(ctx, row)
	if err != nil {
		return nil, "", row, err
	}
	return rc, row.ContentType, row, nil
}

// ListPackageFiles returns the manifest for a PACKAGE artifact.
func (s *Service) ListPackageFiles(ctx context.Context, row sqlc.Artifact) (PackageManifestResponse, error) {
	if !pgstore.IsPackageLike(row.ArtifactType) {
		return PackageManifestResponse{}, errBadRequest("not a PACKAGE artifact")
	}
	var meta struct {
		Package pkgzip.Manifest `json:"package"`
	}
	_ = json.Unmarshal(row.Metadata, &meta)
	out := PackageManifestResponse{
		EntryPoint: meta.Package.EntryPoint,
		Entries:    make([]PackageEntry, 0, len(meta.Package.Entries)),
	}
	for _, e := range meta.Package.Entries {
		// Older uploads have macOS resource-fork sidecars baked into
		// the stored manifest; filter them at response time so the UI
		// doesn't surface __MACOSX/* or ._* entries.
		if pkgzip.IsJunk(e.Path) {
			continue
		}
		out.Entries = append(out.Entries, PackageEntry(e))
	}
	return out, nil
}

// ReadPackageFile fetches a single entry from a PACKAGE.
func (s *Service) ReadPackageFile(ctx context.Context, row sqlc.Artifact, path string) ([]byte, string, error) {
	if !pgstore.IsPackageLike(row.ArtifactType) {
		return nil, "", errBadRequest("not a PACKAGE artifact")
	}
	rc, err := s.store.Content(ctx, row)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	zipBytes, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", err
	}
	return pkgzip.ReadEntry(zipBytes, path)
}

// ArchiveByID soft-deletes one version.
func (s *Service) ArchiveByID(ctx context.Context, id uuid.UUID) (int64, error) {
	// Look up the slug before archiving so we can refresh latest flags.
	var slug string
	if s.osIndexer.Enabled() {
		if row, gerr := s.store.GetByID(ctx, id); gerr == nil && row.NamedSlug != nil && *row.NamedSlug != "" {
			slug = *row.NamedSlug
		}
	}
	n, err := s.store.ArchiveByID(ctx, id)
	if err == nil && n > 0 && s.osIndexer.Enabled() {
		go func() {
			s.osIndexer.MarkDeleted(context.Background(), id.String(), true)
			if slug != "" {
				s.osIndexer.UpdateLatestFlags(context.Background(), slug)
			}
		}()
	}
	return n, err
}

// ArchiveBySlug soft-deletes every version under a slug.
func (s *Service) ArchiveBySlug(ctx context.Context, slug string) (int64, error) {
	// Collect version IDs before archiving — Versions() only returns
	// non-deleted rows, so we must read them first.
	var versionIDs []string
	if s.osIndexer.Enabled() {
		if versions, verr := s.store.Versions(ctx, slug); verr == nil {
			versionIDs = make([]string, len(versions))
			for i, v := range versions {
				versionIDs[i] = pgstore.UUIDFromPG(v.ArtifactID).String()
			}
		}
	}
	n, err := s.store.ArchiveBySlug(ctx, slug)
	if err == nil && n > 0 && s.osIndexer.Enabled() {
		go func() {
			if len(versionIDs) > 0 {
				for _, id := range versionIDs {
					s.osIndexer.MarkDeleted(context.Background(), id, true)
				}
			} else {
				// Versions() failed before archiving, so we have no per-version
				// IDs to flip. Mark every indexed doc for the slug deleted via
				// update-by-query, otherwise they keep is_deleted:false and leak
				// into non-archived search until the next reindex.
				s.osIndexer.MarkSlugDeleted(context.Background(), slug)
			}
			// Always update latest flags — handles both the normal case
			// and the fallback (update-by-query clears is_latest for all
			// archived docs under the slug).
			s.osIndexer.UpdateLatestFlags(context.Background(), slug)
		}()
	}
	return n, err
}

// UnarchiveByID clears deleted_at on a previously archived artifact.
func (s *Service) UnarchiveByID(ctx context.Context, id uuid.UUID) (int64, error) {
	n, err := s.store.UnarchiveByID(ctx, id)
	if err == nil && n > 0 && s.osIndexer.Enabled() {
		go func() {
			row, gerr := s.store.GetByID(context.Background(), id)
			if gerr != nil {
				// Fallback: clear the deleted flag on the existing doc.
				s.osIndexer.MarkDeleted(context.Background(), id.String(), false)
				return
			}
			// Full re-index (upsert) rather than a flag flip: a row that was
			// never indexed, or was hard-deleted from the index, must reappear
			// in BM25 search after unarchive — MarkDeleted alone can't create it.
			s.osIndexer.IndexArtifact(context.Background(), row)
			if row.NamedSlug != nil && *row.NamedSlug != "" {
				s.osIndexer.UpdateLatestFlags(context.Background(), *row.NamedSlug)
			}
		}()
	}
	return n, err
}

// HardDeleteByID permanently deletes the artifact row. Caller must
// enforce admin-only access at the HTTP layer.
func (s *Service) HardDeleteByID(ctx context.Context, id uuid.UUID) (int64, error) {
	// Look up the slug before deleting so we can refresh latest flags.
	var slug string
	if s.osIndexer.Enabled() {
		if row, gerr := s.store.GetByID(ctx, id); gerr == nil && row.NamedSlug != nil && *row.NamedSlug != "" {
			slug = *row.NamedSlug
		}
	}
	n, err := s.store.HardDeleteByID(ctx, id)
	if err == nil && n > 0 && s.osIndexer.Enabled() {
		go func() {
			s.osIndexer.HardDelete(context.Background(), id.String())
			if slug != "" {
				s.osIndexer.UpdateLatestFlags(context.Background(), slug)
			}
		}()
	}
	return n, err
}

// resolveSlugForCaller returns the artifact row for /s/<slug>[/<ver>]
// honoring the caller's access. For non-admin callers with no pinned
// version, picks the latest version they can read (so a newer
// restricted version doesn't hide an accessible older one).
func (s *Service) resolveSlugForCaller(ctx context.Context, slug string, version *int32, caller string) (sqlc.Artifact, error) {
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	if version == nil && !admin {
		return s.store.GetLatestBySlugForCaller(ctx, slug, caller)
	}
	row, err := s.store.GetBySlug(ctx, slug, version)
	if err != nil {
		return row, err
	}
	if err := s.checkAccess(ctx, row, caller); err != nil {
		return sqlc.Artifact{}, err
	}
	return row, nil
}

// callerForList returns the email to use as ListInput.CallerEmail.
// Admins get "" (no filter — they see everything). Non-admins get
// their own email so the SQL filter restricts to artifacts they're
// allowed to read.
func (s *Service) callerForList(ctx context.Context) (string, error) {
	caller := auth.EmailFromContext(ctx)
	ok, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return "", err
	}
	if ok {
		return "", nil // MANAGE_ARTIFACTS → no access filter (sees everything)
	}
	return caller, nil
}

// appCouchHidden reports whether scope:app:couch artifacts should be hidden
// from this list/search caller. Admins (MANAGE_ARTIFACTS → empty caller via
// callerForList) already see everything. MANAGE_SKILLS holders are also exempt
// so an app's service identity (e.g. couch's service@) can build its skill
// catalog from List(kind:skill). Everyone else is hidden.
func (s *Service) appCouchHidden(ctx context.Context, caller string) (bool, error) {
	if caller == "" {
		return false, nil
	}
	// Surface lookup errors rather than silently hiding — a transient DB error
	// must not quietly give a MANAGE_SKILLS holder an incomplete catalog.
	ok, err := s.store.HasPermission(ctx, caller, rbac.ManageSkills)
	if err != nil {
		return false, err
	}
	return !ok, nil
}

// canManageArtifacts reports whether the caller holds MANAGE_ARTIFACTS — the
// permission that grants read/write to ALL artifacts (the former admin bypass
// of per-artifact access control). Resolution errors propagate so a transient
// failure surfaces rather than silently granting or denying access.
func (s *Service) canManageArtifacts(ctx context.Context, caller string) (bool, error) {
	return s.store.HasPermission(ctx, caller, rbac.ManageArtifacts)
}

// HasPermission proxies the store's permission check so other HTTP layers (the
// MCP server) can gate on capabilities without their own store handle.
func (s *Service) HasPermission(ctx context.Context, caller string, p rbac.Permission) (bool, error) {
	return s.store.HasPermission(ctx, caller, p)
}

// CallerGroups proxies the store's group-token resolution so the MCP list/
// search surface can populate ListInput.CallerGroups (group-granted artifacts
// must show in the catalog there too).
func (s *Service) CallerGroups(ctx context.Context, caller string) ([]string, error) {
	return s.store.CallerGroups(ctx, caller)
}

// CanArchiveSlug decides whether `caller` may archive the WHOLE slug. exists is
// false when the slug has no live version (→ 404). allowed requires either
// MANAGE_ARTIFACTS or that the caller created every live version (since
// slug-archive soft-deletes them all). Shared by the REST + MCP archive paths.
func (s *Service) CanArchiveSlug(ctx context.Context, caller, slug string) (allowed, exists bool, err error) {
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return false, false, err
	}
	total, foreign, err := s.store.SlugLiveVersionStats(ctx, slug, caller)
	if err != nil {
		return false, false, err
	}
	if total == 0 {
		return false, false, nil
	}
	return admin || foreign == 0, true, nil
}

// fillCallerGroups populates in.CallerGroups so the access filter includes
// artifacts granted to a group the caller belongs to. No-op when CallerEmail
// is empty (admin bypass, or no access filter), keeping the query unchanged
// for those callers.
func (s *Service) fillCallerGroups(ctx context.Context, in *pgstore.ListInput) error {
	if in.CallerEmail == "" {
		return nil
	}
	groups, err := s.store.CallerGroups(ctx, in.CallerEmail)
	if err != nil {
		return err
	}
	in.CallerGroups = groups
	return nil
}

// CreatorOf returns the creator email for the given artifact, or "" if
// the artifact doesn't exist. Includes archived rows so archive-related
// permission checks can use it.
func (s *Service) CreatorOf(ctx context.Context, id uuid.UUID) (string, error) {
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	return row.Creator, nil
}

// ─── HTTP handler set ────────────────────────────────────────────────

// Mount attaches all artifact routes to the chi router. The router is
// expected to have auth middleware applied at a higher level.
func Mount(r chi.Router, svc *Service) {
	r.Post("/api/artifacts", svc.httpCreate)
	// suggest-metadata must be registered before the {id} param routes; it's
	// a distinct static path under /api/artifacts, POST-only.
	r.Post("/api/artifacts/suggest-metadata", svc.httpSuggestMetadata)
	r.Post("/api/artifacts/by-slug/{slug}/append", svc.httpAppendBySlug)
	r.Get("/api/artifacts", svc.httpList)
	r.Get("/api/artifacts/search", svc.httpSearch)
	r.Get("/api/artifacts/aggregates", svc.httpAggregates)
	r.Get("/api/artifacts/aggregates/browse", svc.httpBrowseAggregates)

	r.Get("/api/artifacts/by-slug/{slug}", svc.httpGetBySlug)
	r.Get("/api/artifacts/by-slug/{slug}/raw", svc.httpGetBySlugRaw)
	r.Get("/api/artifacts/by-slug/{slug}/versions", svc.httpVersions)
	r.Delete("/api/artifacts/by-slug/{slug}", svc.httpArchiveBySlug)
	r.Get("/api/artifacts/by-slug/{slug}/files", svc.httpFilesBySlug)
	r.Get("/api/artifacts/by-slug/{slug}/files/*", svc.httpFileBySlug)

	// {id}/meta and {id}/files and {id}/files/* must precede {id} so
	// chi doesn't swallow them as a single path param. Order in chi
	// is by registration; chi treats /{id} and /{id}/meta as distinct
	// routes once both are registered, so ordering is not strictly
	// required, but we list specific first for clarity.
	r.Get("/api/artifacts/{id}/meta", svc.httpGetMeta)
	r.Get("/api/artifacts/{id}/files", svc.httpFilesByID)
	r.Get("/api/artifacts/{id}/files/*", svc.httpFileByID)
	r.Get("/api/artifacts/{id}", svc.httpGetContent)
	r.Delete("/api/artifacts/{id}", svc.httpArchive)
	r.Post("/api/artifacts/{id}/unarchive", svc.httpUnarchive)
	r.Patch("/api/artifacts/{id}", svc.httpPatch)

	r.Get("/api/me", svc.httpMe)
}

// MountApp attaches the full-page APP routes to the chi router.
func MountApp(r chi.Router, svc *Service) {
	// /app/{ident} serves an APP artifact full-page at its entry point — the
	// app IS the page (no viewer chrome) — with the bridge + sandbox injected.
	// ident is a slug or UUID. Same single origin as the rest (Go edge in prod;
	// FE rewrite in dev). The ergonomic "open this app" URL.
	r.Get("/app/{ident}", svc.httpApp)
	r.Get("/app/{ident}/{version}", svc.httpApp)
}

// ─── handlers ────────────────────────────────────────────────────────

// MaxUploadBytes caps the JSON body of POST /api/artifacts. Generous
// for any single TEXT or PACKAGE artifact we want to support but tight
// enough to refuse a multi-GB body before we OOM the server.
const MaxUploadBytes = 200 << 20 // 200 MiB

func (s *Service) httpCreate(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		s.httpCreateMultipart(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)
	var body CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// MaxBytesReader surfaces as "http: request body too large"
		// post-Go-1.19; map to 413 for clarity.
		if strings.Contains(err.Error(), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "too-large",
				fmt.Sprintf("upload exceeds %d-byte limit", MaxUploadBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "bad-request", "decode body: "+err.Error())
		return
	}
	if !isValidContentType(body.ContentType) {
		writeError(w, http.StatusBadRequest, "bad-request",
			"content_type must look like type/subtype with an optional charset (e.g. text/markdown or text/html; charset=utf-8)")
		return
	}
	email := auth.EmailFromContext(r.Context())
	info, err := s.Create(r.Context(), body, email)
	if err != nil {
		var br badRequest
		if errors.As(err, &br) {
			writeError(w, http.StatusBadRequest, "bad-request", br.msg)
			return
		}
		var fb forbidden
		if errors.As(err, &fb) {
			writeError(w, http.StatusForbidden, "forbidden", fb.msg)
			return
		}
		var sc slugConflict
		if errors.As(err, &sc) {
			writeError(w, http.StatusConflict, "slug-exists", sc.Error())
			return
		}
		if errors.Is(err, pgstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not-found", "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(info)
}

// httpCreateMultipart handles a multipart/form-data POST /api/artifacts:
// a `file` part (raw bytes, no base64) plus text form fields that map to
// CreateRequest. The body is capped by MaxBytesReader as a backstop.
func (s *Service) httpCreateMultipart(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil { // 8 MiB in-memory, rest to temp file
		if strings.Contains(err.Error(), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "too-large",
				fmt.Sprintf("upload exceeds %d-byte limit", MaxUploadBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "bad-request", "parse multipart: "+err.Error())
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "missing `file` part")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "read file: "+err.Error())
		return
	}
	ct := r.FormValue("content_type")
	if ct == "" {
		ct = hdr.Header.Get("Content-Type") // fall back to the part's own type
	}
	req := CreateRequest{
		Title:        r.FormValue("title"),
		ContentType:  ct,
		ArtifactType: r.FormValue("artifact_type"),
		Labels:       r.Form["labels"],
		rawContent:   data,
	}
	if v := r.FormValue("named_slug"); v != "" {
		req.NamedSlug = &v
	}
	if v := r.FormValue("description"); v != "" {
		req.Description = &v
	}
	if !isValidContentType(req.ContentType) {
		writeError(w, http.StatusBadRequest, "bad-request",
			"content_type must look like type/subtype (e.g. image/png)")
		return
	}
	info, err := s.Create(r.Context(), req, auth.EmailFromContext(r.Context()))
	if err != nil {
		var br badRequest
		if errors.As(err, &br) {
			writeError(w, http.StatusBadRequest, "bad-request", br.msg)
			return
		}
		var fb forbidden
		if errors.As(err, &fb) {
			writeError(w, http.StatusForbidden, "forbidden", fb.msg)
			return
		}
		var sc slugConflict
		if errors.As(err, &sc) {
			writeError(w, http.StatusConflict, "slug-exists", sc.Error())
			return
		}
		if errors.Is(err, pgstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not-found", "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(info)
}

// httpAppendBySlug handles POST /api/artifacts/by-slug/{slug}/append.
// Returns 200 on success (with the new ArtifactInfo), 200 + header
// `X-Arti-Idempotent-Replay: true` on an idempotent replay, 409 on
// persistent (slug, version) contention, 400 on bad input.
func (s *Service) httpAppendBySlug(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)
	slug := chi.URLParam(r, "slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "bad-request", "missing slug")
		return
	}
	var body AppendRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "too-large",
				fmt.Sprintf("upload exceeds %d-byte limit", MaxUploadBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "bad-request", "decode body: "+err.Error())
		return
	}
	email := auth.EmailFromContext(r.Context())
	info, replayed, err := s.Append(r.Context(), slug, body, email)
	if err != nil {
		var br badRequest
		if errors.As(err, &br) {
			writeError(w, http.StatusBadRequest, "bad-request", br.msg)
			return
		}
		var fb forbidden
		if errors.As(err, &fb) {
			writeError(w, http.StatusForbidden, "forbidden", fb.msg)
			return
		}
		var stale idempotencyStale
		if errors.As(err, &stale) {
			// 410 Gone — the idempotency-cached artifact is no longer
			// accessible (archived or access revoked). Caller should pick
			// a fresh idempotency_key rather than retry with the same one.
			writeError(w, http.StatusGone, "idempotency-stale", stale.Error())
			return
		}
		if errors.Is(err, pgstore.ErrConflict) {
			writeError(w, http.StatusConflict, "conflict",
				"append lost the (slug, version) race after retries; try again")
			return
		}
		if errors.Is(err, pgstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not-found", "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if replayed {
		w.Header().Set("X-Arti-Idempotent-Replay", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(info)
}

func (s *Service) httpList(w http.ResponseWriter, r *http.Request) {
	in := parseList(r)
	caller, err := s.callerForList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	in.CallerEmail = caller
	if err := s.fillCallerGroups(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	in.LatestPerSlug = latestPerSlugFor(r, in)
	res, err := s.List(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// latestPerSlugFor decides whether the catalog should collapse to one row
// per slug (latest version). Default yes — the main catalog shows each
// slug once. Turned OFF when the caller has drilled into a specific slug
// (so they see that slug's full version history), when they ask for all
// versions explicitly via ?all_versions=true, or on archived-only
// listings: versions archive independently, and an archived row can
// never be its slug's latest LIVE version, so the collapse would hide
// every slugged archived row (only slug-less ones would survive).
//
// The drill-in signal is the dedicated ?slug= query param, NOT in.Slug: a
// `slug:` token typed in the search box also populates in.Slug (via
// mergeFilters, so it still filters) but is a *search*, which keeps the
// collapse on so a glob like `slug:pr-review*` shows one row per matching
// slug. This mirrors catalogView on the web side (drilledIntoSlug keys off
// the same dedicated param) so the two can't disagree about what a drill-in is.
func latestPerSlugFor(r *http.Request, in pgstore.ListInput) bool {
	if r.URL.Query().Get("all_versions") == "true" {
		return false
	}
	if r.URL.Query().Get("slug") != "" {
		return false
	}
	if in.OnlyArchived {
		return false
	}
	return true
}

func (s *Service) httpSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	in := parseList(r)
	caller, cerr := s.callerForList(r.Context())
	if cerr != nil {
		writeError(w, http.StatusInternalServerError, "internal", cerr.Error())
		return
	}
	in.CallerEmail = caller
	if err := s.fillCallerGroups(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Pull `slug:smoke-test foo bar` style tokens out of the search box and
	// fold them into the structured filters. Whatever is left becomes the
	// substring search.
	filters, negated, remainder := parseQuery(q)
	mergeFilters(&in, filters, negated)
	if remainder == "" && len(filters) == 0 && len(negated) == 0 && q != "" {
		// User typed bare text — keep it as the substring.
		remainder = q
	}
	// Same latest-per-slug policy as the plain list: collapse to one row
	// per slug unless the caller drilled in via the dedicated ?slug= param
	// or asked for ?all_versions=true. A `slug:` token in the search box is
	// a search (mergeFilters has set in.Slug so it still filters), NOT a
	// drill-in — so latestPerSlugFor deliberately keys off the raw ?slug=
	// param, not in.Slug, and a slug: search stays collapsed by default.
	in.LatestPerSlug = latestPerSlugFor(r, in)
	res, err := s.Search(r.Context(), remainder, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Service) httpGetMeta(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller := auth.EmailFromContext(r.Context())
	info, err := s.Get(r.Context(), id, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Service) httpGetContent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller := auth.EmailFromContext(r.Context())
	rc, ct, row, err := s.Content(r.Context(), id, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	defer rc.Close()
	setContentDisposition(w, r, row.Title, ct, row.ArtifactType)
	s.writeServedContent(w, rc, ct, id.String(), row.ArtifactType, caller, auth.NameFromContext(r.Context()), auth.PictureFromContext(r.Context()), row.SizeBytes, fullPageContext(r))
}

func (s *Service) httpArchive(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller := auth.EmailFromContext(r.Context())
	admin, err := s.canManageArtifacts(r.Context(), caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// MANAGE_ARTIFACTS-or-creator gate. We fetch the row first so that a
	// non-admin trying to archive someone else's artifact gets 403, not
	// silent success on a 404 (which would leak existence).
	creator, err := s.CreatorOf(r.Context(), id)
	if err != nil {
		writeMaybeNotFound(w, err)
		return
	}
	if !admin && !strings.EqualFold(creator, caller) {
		writeError(w, http.StatusForbidden, "forbidden", "only the creator or an admin may archive")
		return
	}
	// Hard delete requires MANAGE_ARTIFACTS and is selected by `?hard=true`.
	if r.URL.Query().Get("hard") == "true" {
		if !admin {
			writeError(w, http.StatusForbidden, "forbidden", "hard delete is admin-only")
			return
		}
		if _, err := s.HardDeleteByID(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	n, err := s.ArchiveByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not-found", "no such artifact")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) httpGetBySlug(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	ver := parseVersion(r)
	caller := auth.EmailFromContext(r.Context())
	info, err := s.GetBySlug(r.Context(), slug, ver, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Service) httpGetBySlugRaw(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	ver := parseVersion(r)
	caller := auth.EmailFromContext(r.Context())
	rc, ct, row, err := s.ContentBySlug(r.Context(), slug, ver, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	defer rc.Close()
	setContentDisposition(w, r, row.Title, ct, row.ArtifactType)
	s.writeServedContent(w, rc, ct, pgstore.UUIDFromPG(row.ArtifactID).String(), row.ArtifactType, caller, auth.NameFromContext(r.Context()), auth.PictureFromContext(r.Context()), row.SizeBytes, false)
}

func (s *Service) httpVersions(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	caller := auth.EmailFromContext(r.Context())
	v, err := s.Versions(r.Context(), slug, caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, VersionsResponse{Versions: v})
}

func (s *Service) httpArchiveBySlug(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	caller := auth.EmailFromContext(r.Context())
	allowed, exists, err := s.CanArchiveSlug(r.Context(), caller, slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !exists {
		writeMaybeNotFound(w, pgstore.ErrNotFound)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "only the creator of every version or an admin may archive this slug")
		return
	}
	n, err := s.ArchiveBySlug(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"archived_count": n})
}

// httpUnarchive restores a previously archived artifact. The creator
// may unarchive their own work; admins may unarchive anything.
// Hard-deleted artifacts cannot be restored (the row is gone).
func (s *Service) httpUnarchive(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller := auth.EmailFromContext(r.Context())
	admin, err := s.canManageArtifacts(r.Context(), caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	creator, err := s.CreatorOf(r.Context(), id)
	if err != nil {
		writeMaybeNotFound(w, err)
		return
	}
	if !admin && !strings.EqualFold(creator, caller) {
		writeError(w, http.StatusForbidden, "forbidden", "only the creator or an admin may unarchive")
		return
	}
	n, err := s.UnarchiveByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not-found", "no such archived artifact")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// httpMe returns the authenticated caller's email, admin flag, display
// name, and profile picture so the FE can render permission-gated
// controls and attribute comments to a real identity.
func (s *Service) httpMe(w http.ResponseWriter, r *http.Request) {
	email := auth.EmailFromContext(r.Context())
	name := auth.NameFromContext(r.Context())
	if name == "" {
		name = nameFromEmail(email)
	}
	// Effective permissions drive FE capability gating; is_admin is derived
	// from holding the ADMIN role (kept for back-compat with existing UI). Both
	// come from one rbac snapshot so they can't pair across cache generations.
	roles, permSet, err := s.store.CallerAccess(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	perms := make([]string, 0, len(permSet))
	for p := range permSet {
		perms = append(perms, p)
	}
	sort.Strings(perms)
	isAdmin := false
	for _, rn := range roles {
		if rn == rbac.RoleAdmin {
			isAdmin = true
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email":       email,
		"is_admin":    isAdmin,
		"name":        name,
		"picture":     auth.PictureFromContext(r.Context()),
		"permissions": perms,
	})
}

// nameFromEmail derives a display name from an email address.
// "thibaut@example.com" → "Thibaut", "john.doe@x.com" → "John Doe".
func nameFromEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return email
	}
	local := email[:at]
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// UpdateMetadataRequest carries the editable fields for UpdateMetadata. A nil
// pointer leaves a field unchanged; a non-nil pointer is an explicit set,
// including an empty slice to clear (e.g. AllowedAccess=[] → creator-only).
// Content and artifact_type are immutable — version via Create to change those.
type UpdateMetadataRequest struct {
	Title *string `json:"title"`
	// Description is editable for the same reason Labels is: together they are
	// the only two fields arti's browse/search actually surface, so a bad or
	// missing description is a findability defect. Making labels fixable in
	// place but not the description meant an agent that published a doc with a
	// useless description could only fix it by minting a content version — and
	// for a PACKAGE or ATTACHMENT that requires the original bytes, which the
	// session that wrote them has usually long since discarded.
	// An explicit "" clears it.
	Description   *string   `json:"description"`
	Scopes        *[]string `json:"scopes"`
	Labels        *[]string `json:"labels"`
	AllowedAccess *[]string `json:"allowed_access"`
	AllowedWrite  *[]string `json:"allowed_write"`
}

// cleanStringSlice trims each element, drops empties, and dedupes
// (case-sensitive), preserving first-seen order. Lets callers be sloppy with
// labels/scopes/allowed_access without persisting blanks or duplicates.
func cleanStringSlice(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		t := strings.TrimSpace(v)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// UpdateMetadata edits the mutable fields (title, description, scopes, labels,
// allowed_access) of ONE artifact version in place — content / type stay
// immutable (create a new version to change those). Only fields whose pointer
// is non-nil are touched; an explicit empty slice clears the field. The edit
// applies to the single version named by `id`: sibling versions under the same
// slug keep their prior metadata until patched individually.
//
// Enforces creator-or-MANAGE_ARTIFACTS, plus the kind:skill write-guard
// (editing — or relabeling into/out of — a skill artifact needs MANAGE_SKILLS,
// so the Create/Append guard can't be sidestepped via PATCH). Best-effort
// re-indexes to OpenSearch, then returns the refreshed DTO. Shared by the REST
// PATCH handler and the MCP update_artifact tool. Errors: pgstore.ErrNotFound
// (→404), forbidden (→403), badRequest (→400).
func (s *Service) UpdateMetadata(ctx context.Context, id uuid.UUID, req UpdateMetadataRequest, caller string) (ArtifactInfo, error) {
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return ArtifactInfo{}, err
	}
	existing, err := s.store.GetByID(ctx, id)
	if err != nil {
		return ArtifactInfo{}, err
	}
	if !admin && !strings.EqualFold(existing.Creator, caller) {
		return ArtifactInfo{}, errForbidden("only the creator or an admin may edit")
	}
	guardSets := [][]string{existing.Labels}
	if req.Labels != nil {
		guardSets = append(guardSets, *req.Labels)
	}
	if err := s.requireSkillWrite(ctx, caller, guardSets...); err != nil {
		return ArtifactInfo{}, err
	}
	if req.Title != nil {
		t := strings.TrimSpace(*req.Title)
		if t == "" {
			return ArtifactInfo{}, errBadRequest("title cannot be empty")
		}
		if _, err := s.store.UpdateTitle(ctx, id, t); err != nil {
			return ArtifactInfo{}, err
		}
	}
	if req.Description != nil {
		if _, err := s.store.UpdateDescription(ctx, id, strings.TrimSpace(*req.Description)); err != nil {
			return ArtifactInfo{}, err
		}
	}
	if req.Labels != nil {
		if _, err := s.store.UpdateLabels(ctx, id, cleanStringSlice(*req.Labels)); err != nil {
			return ArtifactInfo{}, err
		}
	}
	if req.Scopes != nil {
		if _, err := s.store.UpdateScopes(ctx, id, cleanStringSlice(*req.Scopes)); err != nil {
			return ArtifactInfo{}, err
		}
	}
	if req.AllowedAccess != nil || req.AllowedWrite != nil {
		// Compute the final (access, write) pair from the request overlaid on
		// the current row, then write both together. UpdateAccess enforces the
		// ⊆ invariant (unions write into access). Omitting one field leaves it
		// as-is; passing allowed_write:[] means creator-only writes.
		access := existing.AllowedAccess
		if req.AllowedAccess != nil {
			access = cleanStringSlice(*req.AllowedAccess)
		}
		write := existing.AllowedWrite
		if req.AllowedWrite != nil {
			write = cleanStringSlice(*req.AllowedWrite)
		}
		// Changing a slug's ACL requires the slug OWNER (or admin), not merely
		// the creator of the version being patched: a delegated writer can push
		// a content-only version to become its creator and pass the top-level
		// creator guard, so ACL authority must be pinned to the immutable owner
		// (same rule as Create/Append). Slugless artifacts have no versioning
		// and thus no owner/creator divergence — the top guard already covers
		// them.
		if existing.NamedSlug != nil {
			if err := s.requireAclChangeAuthority(ctx, caller, *existing.NamedSlug, existing, access, write); err != nil {
				return ArtifactInfo{}, err
			}
		}
		if _, err := s.store.UpdateAccess(ctx, id, access, write); err != nil {
			return ArtifactInfo{}, err
		}
	}
	info, err := s.Get(ctx, id, caller)
	if err != nil {
		return ArtifactInfo{}, err
	}
	if s.osIndexer.Enabled() {
		go func() {
			if row, gerr := s.store.GetByID(context.Background(), id); gerr == nil {
				s.osIndexer.IndexArtifact(context.Background(), row)
				if row.NamedSlug != nil && *row.NamedSlug != "" {
					s.osIndexer.UpdateLatestFlags(context.Background(), *row.NamedSlug)
				}
			}
		}()
	}
	return info, nil
}

// httpPatch updates editable fields on an artifact. `title`, `description`,
// `scopes`, `labels`, and `allowed_access` are mutable post-publish; content /
// type stay immutable (create a new version to change those). Creator or admin
// only.
func (s *Service) httpPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body UpdateMetadataRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "invalid JSON")
		return
	}
	caller := auth.EmailFromContext(r.Context())
	info, err := s.UpdateMetadata(r.Context(), id, body, caller)
	if err != nil {
		var br badRequest
		if errors.As(err, &br) {
			writeError(w, http.StatusBadRequest, "bad-request", br.msg)
			return
		}
		var fb forbidden
		if errors.As(err, &fb) {
			writeError(w, http.StatusForbidden, "forbidden", fb.msg)
			return
		}
		writeMaybeNotFound(w, err) // pgstore.ErrNotFound → 404, else 500
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Service) httpFilesByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller := auth.EmailFromContext(r.Context())
	row, err := s.store.GetByID(r.Context(), id)
	if writeMaybeNotFound(w, err) {
		return
	}
	if writeMaybeNotFound(w, s.checkAccess(r.Context(), row, caller)) {
		return
	}
	resp, err := s.ListPackageFiles(r.Context(), row)
	if err != nil {
		writeBadOrInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// filePathParam returns the package-relative file path from the trailing
// wildcard, percent-decoded. chi hands back the path as it appeared in the
// URL (e.g. spaces as %20, so "report (1).pdf" arrives as "report%20(1).pdf");
// the zip's entry names are literal, so we must unescape before lookup or
// any file with a space/special char 404s. Falls back to the raw value if
// it isn't valid percent-encoding.
func filePathParam(r *http.Request) string {
	raw := chi.URLParam(r, "*")
	if dec, err := url.PathUnescape(raw); err == nil {
		return dec
	}
	return raw
}

func (s *Service) httpFileByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	caller := auth.EmailFromContext(r.Context())
	path := filePathParam(r)
	row, err := s.store.GetByID(r.Context(), id)
	if writeMaybeNotFound(w, err) {
		return
	}
	if writeMaybeNotFound(w, s.checkAccess(r.Context(), row, caller)) {
		return
	}
	body, ct, err := s.ReadPackageFile(r.Context(), row, path)
	if err != nil {
		writeBadOrInternal(w, err)
		return
	}
	body = s.injectFilesBaseToken(body, ct, id.String(), caller, path)
	if row.ArtifactType == pgstore.TypeApp {
		// APP files get ONLY the app bridge, never the comments overlay — a
		// sandboxed page should carry one scoped token, not two.
		body = s.injectAppBridge(body, ct, id.String(), caller, nil)
		s.setContentSecurityApp(w, ct)
	} else {
		body = s.injectComments(body, ct, id.String(), caller, auth.NameFromContext(r.Context()), auth.PictureFromContext(r.Context()))
		body = injectFileNavReporter(body, ct, path)
		setContentSecurityMaybeFullPage(w, ct, fullPageContext(r))
	}
	_, _ = w.Write(body)
}

func (s *Service) httpFilesBySlug(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	ver := parseVersion(r)
	caller := auth.EmailFromContext(r.Context())
	row, err := s.resolveSlugForCaller(r.Context(), slug, ver, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	resp, err := s.ListPackageFiles(r.Context(), row)
	if err != nil {
		writeBadOrInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// httpApp serves an APP artifact full-page at its entry point. ident is a slug
// or UUID. Unlike the /files routes (the raw content API) and the FE viewer
// (/s/{slug}, which wraps a doc in chrome), this is the "open the app" URL: the
// app is the whole page, with the bridge + APP sandbox injected.
func (s *Service) httpApp(w http.ResponseWriter, r *http.Request) {
	ident := chi.URLParam(r, "ident")
	caller := auth.EmailFromContext(r.Context())
	// Version: the {version} path segment (on /app/{slug}/{version}) wins, else
	// the ?version= query; absent → latest the caller can read. A UUID ident
	// already pins one exact version, so version is ignored in that case.
	var ver *int32
	if vs := chi.URLParam(r, "version"); vs != "" {
		n, perr := strconv.Atoi(vs)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "bad-request", "invalid version: "+vs)
			return
		}
		x := int32(n)
		ver = &x
	} else {
		ver = parseVersion(r)
	}
	// Whether this URL pins a version: an explicit ?version=/{version}, or a
	// UUID ident (which addresses one immutable row). Everything else resolved
	// to the slug's newest readable version by construction.
	_, identErr := uuid.Parse(ident)
	pinned := ver != nil || identErr == nil
	row, err := s.resolveIdent(r.Context(), ident, ver, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	if row.ArtifactType != pgstore.TypeApp {
		writeError(w, http.StatusNotFound, "not-found", "not an APP artifact (use /a or /s for other types)")
		return
	}
	// The /s viewer shows a "newer version available" strip when the URL pins
	// an older version; /app is chrome-less, so the same notice has to ride
	// inside the app document. Only this top-level route gets it — an embed
	// surface pins deliberately and its host owns that chrome.
	//
	// The probe is not one query: resolving the caller-scoped latest costs a
	// MANAGE_ARTIFACTS check plus the latest-for-caller lookup (which resolves
	// the caller's groups), and on the slug path the permission check repeats
	// one resolveIdent just made. Cheap indexed single-row reads, but budget
	// 2–3 round-trips per pinned load. Hence the gate: a bare /app/{slug}
	// already resolved to the newest version this caller can read and so can
	// never be stale — the common (unpinned) launch stays at exactly the query
	// count it had before, and only a version-pinned open pays.
	var stale *staleNotice
	if pinned {
		stale = s.staleAppNotice(r.Context(), row, caller)
	}
	if err := s.serveAppRow(w, r, row, caller, "", "", "", nil, stale); err != nil {
		writeBadOrInternal(w, err)
	}
}

// staleNotice describes an APP page pinned to an older version than its slug's
// latest: the version being served, that latest version, and the unpinned URL
// pointing at it.
type staleNotice struct {
	Current   int32
	Latest    int32
	LatestURL string
}

// staleAppNotice returns a notice when row is NOT the newest version of its
// slug that caller can read, else nil. Deliberately best-effort and never
// fatal: a failed lookup just means no strip, never a failed app load.
//
// Slugless artifacts (reached only as /app/{uuid}) have no "latest" to point
// at, so they never produce one. The latest is resolved through the same
// caller-scoped path the read API uses, so a viewer who can't read a newer
// restricted version is not told it exists.
func (s *Service) staleAppNotice(ctx context.Context, row sqlc.Artifact, caller string) *staleNotice {
	// Guard before the lookup, not after: a slugless row has no "latest" to
	// resolve, so it must not cost a query (nor touch a nil store).
	if row.NamedSlug == nil || *row.NamedSlug == "" || row.Version == nil {
		return nil
	}
	latest, err := s.resolveSlugForCaller(ctx, *row.NamedSlug, nil, caller)
	if err != nil {
		return nil
	}
	return staleNoticeFrom(row, latest)
}

// staleNoticeFrom decides what to render given the served row and its slug's
// latest version: nil when row IS the latest (or newer, which a pinned read of
// a since-archived head can produce), else the notice. Pure — the store lookup
// stays in staleAppNotice — so the comparison itself is table-testable without
// a store.
func staleNoticeFrom(row, latest sqlc.Artifact) *staleNotice {
	if row.NamedSlug == nil || *row.NamedSlug == "" || row.Version == nil {
		return nil
	}
	if latest.Version == nil || *latest.Version <= *row.Version {
		return nil
	}
	return &staleNotice{
		Current:   *row.Version,
		Latest:    *latest.Version,
		LatestURL: "/app/" + url.PathEscape(*row.NamedSlug),
	}
}

// resolveIdent resolves an artifact ident (slug or UUID) for caller, applying
// the same access checks as the rest of the read API. A UUID pins one exact
// version; a slug resolves to the latest version caller can read (or ver, when
// set). It does NOT verify the artifact type. Shared by /app/{ident} (httpApp)
// and the embed surfaces (embed_serve.go).
func (s *Service) resolveIdent(ctx context.Context, ident string, ver *int32, caller string) (sqlc.Artifact, error) {
	if id, perr := uuid.Parse(ident); perr == nil {
		row, err := s.store.GetByID(ctx, id)
		if err == nil {
			err = s.checkAccess(ctx, row, caller)
		}
		return row, err
	}
	return s.resolveSlugForCaller(ctx, ident, ver, caller)
}

// serveAppRow renders a resolved APP artifact full-page at its entry point:
// entry resolution, <base> + bridge injection, and the APP sandbox +
// frame-ancestors CSP. caller is the identity the bridge's scoped token is
// minted for. The caller must have already verified row is an APP. Shared by
// /app/{ident} and the embed surfaces.
//
// Returns a non-nil error (writing NOTHING) when the entry content can't be
// read, so the caller decides the failure UX while preserving the error's
// class — httpApp passes it to writeBadOrInternal (400 vs 500), the embed
// ignores it and falls back to its placeholder. Returns nil once it has written
// the response.
//
// frameAncestors overrides the CSP frame-ancestors source list for this
// response ("" → the global ARTI_APP_FRAME_ANCESTORS default). filesBase
// overrides the <base href> for sibling assets ("" → the cookie-authed
// /api/artifacts/{id}/files/ path; the embed passes its own public files path).
//
// userSurface, when non-empty, names the user-mode embed surface this page is
// served for: the bridge is injected in its needs-user state (no token, a
// Connect handshake instead — see injectAppBridgeUser) and caller is expected
// to be "". userOrigins is that surface's embedder-origin allowlist (nil
// outside user mode), forwarded to the bridge for the token relay.
//
// stale, when non-nil, overlays the "a newer version is available" strip on
// the page (see injectStaleAppBanner). Only /app/{ident} passes one.
func (s *Service) serveAppRow(w http.ResponseWriter, r *http.Request, row sqlc.Artifact, caller, frameAncestors, filesBase, userSurface string, userOrigins []string, stale *staleNotice) error {
	// Launch page precedence: arti-app.json `entry` is the source of truth for
	// an APP (it's what the manifest documents), then the package entry_point,
	// then index.html.
	entry := ""
	// params: the declared URL-param contract (arti-app.json `params`), resolved
	// against this request's query and handed to the app as window.arti.params.
	// nil when the manifest declares none.
	var params map[string]any
	if mb, _, merr := s.ReadPackageFile(r.Context(), row, "arti-app.json"); merr == nil {
		var man struct {
			Entry  string         `json:"entry"`
			Params []appParamSpec `json:"params"`
		}
		if json.Unmarshal(mb, &man) == nil {
			entry = man.Entry
			params = resolveAppParams(man.Params, r.URL.Query())
		}
	}
	if entry == "" {
		if m, e := s.ListPackageFiles(r.Context(), row); e == nil && m.EntryPoint != "" {
			entry = m.EntryPoint
		}
	}
	if entry == "" {
		entry = "index.html"
	}
	body, ct, err := s.ReadPackageFile(r.Context(), row, entry)
	if err != nil {
		return err
	}
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()
	// The entry HTML is served at a /app/… document URL, but its sibling assets
	// (js/css/img) live in the package. Inject a <base> so relative refs resolve
	// to the package-files API instead of under /app/ (where a path segment
	// would be mis-parsed as a version). Root-absolute refs (the injected
	// bridge's /api/apps/mcp) are unaffected by <base>. filesBaseFor prefers a
	// token-scoped path over the cookie-gated one — this response's own
	// CSP: sandbox (below) gives it an opaque origin, so its own sibling
	// requests can't carry the SameSite=Lax arti_session cookie anyway.
	if filesBase == "" {
		filesBase = s.filesBaseFor(caller, aid, entry)
	}
	body = injectBaseHref(body, ct, filesBase)
	if userSurface != "" {
		body = s.injectAppBridgeUser(body, ct, aid, userSurface, userOrigins, params)
	} else {
		body = s.injectAppBridge(body, ct, aid, caller, params)
	}
	body = injectStaleAppBanner(body, ct, stale)
	s.setContentSecurityAppFA(w, ct, frameAncestors)
	_, _ = w.Write(body)
	return nil
}

func (s *Service) httpFileBySlug(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	ver := parseVersion(r)
	path := filePathParam(r)
	caller := auth.EmailFromContext(r.Context())
	row, err := s.resolveSlugForCaller(r.Context(), slug, ver, caller)
	if writeMaybeNotFound(w, err) {
		return
	}
	body, ct, err := s.ReadPackageFile(r.Context(), row, path)
	if err != nil {
		writeBadOrInternal(w, err)
		return
	}
	aid := pgstore.UUIDFromPG(row.ArtifactID).String()
	body = s.injectFilesBaseToken(body, ct, aid, caller, path)
	if row.ArtifactType == pgstore.TypeApp {
		// APP files get ONLY the app bridge, never the comments overlay — a
		// sandboxed page should carry one scoped token, not two.
		body = s.injectAppBridge(body, ct, aid, caller, nil)
		s.setContentSecurityApp(w, ct)
	} else {
		body = s.injectComments(body, ct, aid, caller, auth.NameFromContext(r.Context()), auth.PictureFromContext(r.Context()))
		body = injectFileNavReporter(body, ct, path)
		setContentSecurityMaybeFullPage(w, ct, fullPageContext(r))
	}
	_, _ = w.Write(body)
}

// ─── helpers ─────────────────────────────────────────────────────────

// setContentSecurity sets the Content-Type header and, when the body
// is HTML, attaches a Content-Security-Policy: sandbox directive. The
// header makes the response act sandbox-with-scripts even at the top
// level — so an uploaded `index.html` opened directly in a browser
// tab gets a unique opaque origin, cannot read arti_session, and
// cannot reach arti's main-origin DOM. Scripts inside the page still
// run; static asset loads (images/scripts/css) still work. The
// browser-rendering interactive reports we care about (data inlined)
// continue to function.
//
// Only HTML responses need this. Plain text, JSON, zip, etc. are not
// script-executing surfaces.
//
// That opaque origin has a sharp edge past what the comment above claims:
// per the Fetch spec, a document's "site for cookies" compares ITS OWN
// origin to the top-level document's origin, so an opaque-origin document's
// own subresource requests (its <link rel=stylesheet>, <script src>,
// fetch()) are schemefully cross-site. /api/artifacts/*/files/* sits on the
// PublicIngress (no oauth2-proxy), so the SameSite=Lax arti_session cookie
// is the only credential a browser can attach to those requests — and
// cross-site classification strips it. Net effect: every sibling asset a
// rendered page loads itself 401s in production (ARTI_AUTH_DISABLED=true
// local dev never hits the cookie check, which is why this doesn't show up
// there). filesBaseFor / injectFilesBaseToken route those loads through a
// token-scoped path instead so they don't depend on the cookie at all.
//
// path is the package-relative path of the file BEING SERVED (not the asset
// it references) — the <base> must point at that file's own directory, not
// the package root, or a nested page's document-relative refs (e.g.
// docs/page.html's "style.css") resolve to the wrong directory and 404.
func (s *Service) filesBaseFor(caller, artifactID, path string) string {
	dir := dirOf(path)
	cookiePath := "/api/artifacts/" + artifactID + "/files/" + dir
	if s.appToken == nil || s.appTokenVerify == nil || caller == "" {
		return cookiePath
	}
	tok, err := s.appToken(caller, artifactID)
	if err != nil || tok == "" {
		return cookiePath
	}
	return "/api/artifacts/" + artifactID + "/files-token/" + tok + "/" + dir
}

// dirOf returns the directory portion of a package-relative path, with a
// trailing slash, or "" for a root-level path — e.g. "docs/page.html" →
// "docs/", "index.html" → "".
func dirOf(path string) string {
	path = strings.TrimPrefix(path, "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i+1]
	}
	return ""
}

// injectFilesBaseToken sets a <base> on served HTML so its sibling asset
// loads resolve through filesBaseFor's path instead of defaulting to this
// request's own (cookie-gated) URL. path is the served file's own
// package-relative path (see filesBaseFor). No-op for non-HTML or an
// anonymous caller.
func (s *Service) injectFilesBaseToken(body []byte, ct, artifactID, caller, path string) []byte {
	if caller == "" || !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	return injectBaseHref(body, ct, s.filesBaseFor(caller, artifactID, path))
}

// injectComments inserts the comments overlay (a small config blob + the
// arti-served bundle) into served text/html package files, so a reader can
// comment on the prototype page itself. The overlay reaches the comments API
// with a scoped bearer token (the sandboxed page can't send the cookie).
// No-ops for non-HTML, unauthenticated requests, or when injection is off.
func (s *Service) injectComments(body []byte, ct, artifactID, email, name, picture string) []byte {
	if s.embedToken == nil || email == "" || !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	tok, err := s.embedToken(email, artifactID, name, picture)
	if err != nil {
		return body
	}
	if name == "" {
		name = nameFromEmail(email)
	}
	me := map[string]string{"email": email, "name": name}
	if picture != "" {
		me["picture"] = picture
	}
	meJSON, _ := json.Marshal(me)
	idJSON, _ := json.Marshal(artifactID)
	tokJSON, _ := json.Marshal(tok)
	snippet := []byte("<script>window.__ARTI_COMMENTS__={\"artifactId\":" + string(idJSON) +
		",\"token\":" + string(tokJSON) + ",\"me\":" + string(meJSON) +
		"};</script><script src=\"/comments-embed.js\" defer></script>")
	if i := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		return append(out, body[i:]...)
	}
	return append(body, snippet...)
}

// injectFileNavReporter appends a tiny script to served PACKAGE HTML that tells
// the parent frame which file this is, so the full-page viewer can keep the
// browser URL (?file=<path>) in sync as the reader follows in-content links
// from one file to another. The iframe is an opaque origin (CSP: sandbox, no
// allow-same-origin), so the parent CANNOT read the iframe's location — the
// served page must report it. The message carries ONLY the file path (no
// secret; it IS the iframe's own URL); trust is enforced on the receiving side
// by frame identity + a manifest lookup (FullPageHtmlFrame). targetOrigin "*"
// is used because the opaque-origin page's own origin is the string "null", so
// an origin-scoped post is impossible anyway — the same trust model as the APP
// OAuth bridge, which keys on e.source. No-op on non-HTML. `path` is the
// package-relative path being served (the parent resolves it to the canonical
// entry with the same algorithm the server uses).
func injectFileNavReporter(body []byte, ct, path string) []byte {
	if !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	pathJSON, _ := json.Marshal(path)
	snippet := []byte(`<script>try{window.parent!==window&&window.parent.postMessage({source:"arti-file-nav",path:` +
		string(pathJSON) + `},"*")}catch(e){}</script>`)
	if i := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		return append(out, body[i:]...)
	}
	return append(body, snippet...)
}

// appBridgeJS is the tiny client injected into a served APP page. It exposes
// window.arti.callTool(server, tool, args), which POSTs to the governed proxy
// with the injected scoped token, and transparently handles the
// authorization_required handshake (open the Runlayer consent popup, then
// retry). No template literals/backticks so it lives in a Go raw string.
//
// On a user-mode embed surface the page is served with NO token and a
// cfg.connect block instead: the bridge shows a "Connect as you" affordance
// (never an unprompted popup — browsers block those) that opens the
// /auth/embed/app-token handshake in a top-level popup; an expired token
// re-mints on the next tool click, silently once the popup's consent is
// cached. Two DIFFERENT 401s flow through callTool: the proxy's
// {error:"authorization_required"} means an OBO consent (existing popup),
// while a plain 401 ("invalid app token") means re-mint via connect().
//
// Zero-click relay (the embedding-host contract): so the viewer doesn't
// re-consent on every iframe load, a minted token may round-trip through the
// embedding host — the bridge announces it via postMessage
// ({type:"arti-app:token"}) pinned to the surface's configured origins, and
// adopts one handed back in the #arti_token URL fragment on boot. The consent
// click is untouched: every token still originates from the popup handshake;
// the relay only moves an already-minted token between the page and a host
// the surface already declared (frame-ancestors = the same origin set).
const appBridgeJS = `(function(){
  var cfg = window.__ARTI_APP__;
  if(!cfg || (!cfg.token && !cfg.connect)){ return; }
  // Zero-click relay, host->page leg: an embedding host that cached a
  // previously minted token (delivered via announceToken below) may hand it
  // back in the URL fragment (#arti_token=...). Fragments never reach the
  // server or its logs. Adopt it as this page's token and scrub the URL; a
  // stale or version-mismatched hand-in simply falls back to the connect
  // flow via the 401/403 handling in run().
  if(!cfg.token && cfg.connect){
    try{
      var mh = (location.hash || "").match(/[#&]arti_token=([^&]+)/);
      if(mh){
        cfg.token = decodeURIComponent(mh[1]);
        try{ history.replaceState(null, "", location.pathname + location.search); }catch(e){}
      }
    }catch(e){}
  }
  // Zero-click relay, page->host leg: hand a freshly minted token UP to the
  // embedding host so it can cache it (e.g. sessionStorage) and return it on
  // the next page load. targetOrigin is pinned to the surface's configured
  // embedder-origin list (cfg.connect.origins) - the same set frame-ancestors
  // admits - so the token can only be delivered to a declared embedder, never
  // to an arbitrary parent. exp is read client-side from the JWT so the host
  // can expire its cache without decoding tokens itself.
  function announceToken(tok){
    if(!cfg.connect || !cfg.connect.origins || !cfg.connect.origins.length){ return; }
    if(window.parent === window){ return; }
    var exp = null;
    try{ exp = JSON.parse(atob(tok.split(".")[1].replace(/-/g,"+").replace(/_/g,"/"))).exp || null; }catch(e){}
    for(var i = 0; i < cfg.connect.origins.length; i++){
      var o = cfg.connect.origins[i];
      if(typeof o !== "string" || o.indexOf("http") !== 0){ continue; } // skip 'self' & non-origin sources
      try{ window.parent.postMessage({ type: "arti-app:token", appId: cfg.appId, token: tok, exp: exp }, o); }catch(e){}
    }
  }
  function rpc(server, tool, args){
    return fetch(cfg.endpoint, {
      method: "POST",
      credentials: "omit",
      headers: { "Content-Type": "application/json", "Authorization": "Bearer " + cfg.token },
      body: JSON.stringify({ app_id: cfg.appId, server: server, tool: tool, arguments: args || {} })
    }).then(function(r){
      // Tolerate non-JSON bodies (e.g. a 502 HTML page or empty body) instead
      // of throwing a parse error — surface a controlled {detail}.
      return r.text().then(function(tx){
        var b; try { b = tx ? JSON.parse(tx) : {}; } catch(e){ b = { detail: "non-JSON response (HTTP " + r.status + ")" }; }
        return { status: r.status, body: b };
      });
    });
  }
  function authorize(url){
    return new Promise(function(resolve){
      var pop = window.open(url, "arti_oauth", "width=520,height=720");
      var done = false;
      function finish(){ if(done){ return; } done = true; window.removeEventListener("message", onmsg); resolve(); }
      // Accept "done" only from the popup WE opened (e.source === pop). An
      // origin check can't be used: the APP page is opaque-origin (sandbox), so
      // its window.location.origin is "null" and never equals the callback's
      // real origin. Binding to our own popup handle is unforgeable by any other
      // browsing context and works regardless of origin.
      function onmsg(e){ if(e.source === pop && e.data && e.data.arti_oauth === "done"){ finish(); } }
      window.addEventListener("message", onmsg);
      var t = setInterval(function(){ if(!pop || pop.closed){ clearInterval(t); finish(); } }, 600);
    });
  }
  function fail(res){ throw new Error((res.body && res.body.detail) || ("arti tool call failed: " + res.status)); }
  // ── user-mode connect handshake (cfg.connect present, no baked token) ──
  var pendingConnect = null, connectBtn = null;
  function connQS(){
    // The sandboxed page can read its own URL: forward the surface secret it
    // was served with and the EXACT version-pinned app UUID it carries.
    var qs = new URLSearchParams(location.search);
    return { secret: qs.get("auth_secret") || "" };
  }
  function mintURL(state){
    return cfg.connect.mint + "?surface=" + encodeURIComponent(cfg.connect.surface) +
      "&auth_secret=" + encodeURIComponent(connQS().secret) +
      "&app=" + encodeURIComponent(cfg.appId) +
      "&state=" + encodeURIComponent(state);
  }
  function pollURL(state){
    return cfg.connect.poll + "?surface=" + encodeURIComponent(cfg.connect.surface) +
      "&auth_secret=" + encodeURIComponent(connQS().secret) +
      "&state=" + encodeURIComponent(state);
  }
  function randState(){
    var a = new Uint8Array(16); crypto.getRandomValues(a);
    return Array.prototype.map.call(a, function(b){ return ("0" + b.toString(16)).slice(-2); }).join("");
  }
  // connect opens the consent popup and POLLS the server for the minted token.
  // We poll (rather than wait for a postMessage from the popup) because inside
  // a host like Front the popup is sandboxed: it can't reliably reach
  // window.opener, and its consent step is a plain top-level navigation. The
  // token is delivered server-side, keyed by OUR random state nonce, and taken
  // exactly once.
  function connect(){
    if(cfg.token){ return Promise.resolve(cfg.token); }
    if(pendingConnect){ return pendingConnect; }
    var state = randState();
    pendingConnect = new Promise(function(resolve, reject){
      var pop = window.open(mintURL(state), "arti_embed_connect", "width=520,height=720");
      if(!pop){
        // No user activation (e.g. a boot-time call discovered a stale relayed
        // token) or a popup blocker: make the Connect affordance visible so
        // the next attempt rides a real click.
        pendingConnect = null; showConnectButton();
        reject(new Error("popup blocked - use the Connect button"));
        return;
      }
      var done = false, deadline = Date.now() + 180000; // 3 min
      function finish(err, tok){
        if(done){ return; }
        done = true; pendingConnect = null;
        // Any failed (re-)mint — cancelled popup, timeout — must leave the
        // Connect affordance visible: with a relay-adopted token the button
        // was never shown at boot, so without this a stale token would
        // dead-end the page with no way to reconnect.
        if(err){ showConnectButton(); reject(err); } else { hideConnectButton(); resolve(tok); }
      }
      function tick(){
        if(done){ return; }
        if(Date.now() > deadline){ finish(new Error("connect timed out")); return; }
        fetch(pollURL(state), { credentials: "omit" })
          .then(function(r){ return r.ok ? r.json() : {}; })
          .then(function(b){
            if(b && b.ready && b.token){ cfg.token = b.token; announceToken(b.token); finish(null, b.token); return; }
            setTimeout(tick, 1200); // keep polling; user may still be consenting
          })
          .catch(function(){ setTimeout(tick, 1500); });
      }
      setTimeout(tick, 800); // give the popup a moment to render before first poll
    });
    return pendingConnect;
  }
  function showConnectButton(){
    if(connectBtn || cfg.token || !document.body){ return; }
    var b = document.createElement("button");
    b.textContent = "Connect as you";
    b.setAttribute("style", "position:fixed;right:14px;bottom:14px;z-index:2147483647;" +
      "font:13px/1 -apple-system,system-ui,sans-serif;padding:9px 14px;border-radius:8px;" +
      "border:1px solid #2563eb;background:#2563eb;color:#fff;cursor:pointer;" +
      "box-shadow:0 2px 8px rgba(0,0,0,.18)");
    b.addEventListener("click", function(){
      b.disabled = true;
      connect().catch(function(){}).then(function(){ if(connectBtn){ connectBtn.disabled = false; } });
    });
    connectBtn = b;
    document.body.appendChild(b);
  }
  function hideConnectButton(){ if(connectBtn){ connectBtn.remove(); connectBtn = null; } }
  if(cfg.connect && !cfg.token){
    if(document.readyState === "loading"){ document.addEventListener("DOMContentLoaded", showConnectButton); }
    else { showConnectButton(); }
  }
  function run(server, tool, args){
    return rpc(server, tool, args).then(function(res){
      if(res.status === 401 && res.body && res.body.error === "authorization_required"){
        return authorize(res.body.authorize_url).then(function(){
          return rpc(server, tool, args).then(function(res2){
            if(res2.status >= 400){ fail(res2); }
            return res2.body;
          });
        });
      }
      // Re-mint cases: a plain 401 (expired/invalid app token), OR the proxy's
      // app_id-mismatch 403 - a relayed token minted for a previous
      // version-pinned artifact UUID no longer matches the page that was
      // served after a republish. Both mean "this token is unusable here";
      // neither is fixable by anything but a fresh mint.
      var staleToken = res.status === 401 ||
        (res.status === 403 && res.body && typeof res.body.detail === "string" &&
         res.body.detail.indexOf("does not match app_id") >= 0);
      if(staleToken && cfg.connect){
        cfg.token = null;
        return connect().then(function(){
          return rpc(server, tool, args).then(function(res2){
            if(res2.status >= 400){ fail(res2); }
            return res2.body;
          });
        });
      }
      if(res.status >= 400){ fail(res); }
      return res.body;
    });
  }
  window.arti = {
    callTool: function(server, tool, args){
      if(!cfg.token && cfg.connect){
        // Never an unprompted popup: this path only works inside a user
        // gesture (a click's activation covers the window.open); outside one
        // it rejects and the fixed Connect button remains the affordance.
        return connect().then(function(){ return run(server, tool, args); });
      }
      return run(server, tool, args);
    }
  };
  // ── data helpers: read OTHER arti artifacts as the viewer. The governed
  // proxy runs arti's read tools in-process (same access as the viewer, no
  // consent popup), so a template can load its data from separate artifacts
  // pointed to by URL params. Requires the matching tool in arti-app.json's
  // allowlist (arti/read_artifact, arti/search_artifacts).
  function _firstText(r){ return (r && r.content && r.content[0] && r.content[0].text) || ""; }
  // loadArtifact(ident, opts?) -> {text, contentType, sizeBytes, sha256, truncated}.
  // ident is a slug (latest you can read) or UUID; opts may carry {version, max_bytes}.
  window.arti.loadArtifact = function(ident, opts){
    return window.arti.callTool("arti", "read_artifact", Object.assign({ident: ident}, opts || {}))
      .then(function(r){ return { text: _firstText(r), contentType: r && r.content_type, sizeBytes: r && r.size_bytes, sha256: r && r.sha256, truncated: !!(r && r.truncated) }; });
  };
  // loadJSON(ident, opts?) -> the artifact parsed as JSON (throws on bad JSON).
  window.arti.loadJSON = function(ident, opts){
    return window.arti.loadArtifact(ident, opts).then(function(a){ return JSON.parse(a.text); });
  };
  // search(args) -> parsed search_artifacts result {artifacts, total}. args:
  // {q, labels, scope, limit, offset, order_by, order_dir}. order_by is one of
  // created|title|type|slug|version|creator|scope (default created); order_dir
  // asc|desc (default desc) — e.g. {q:"sales", order_by:"created"} for the latest.
  window.arti.search = function(args){
    return window.arti.callTool("arti", "search_artifacts", args || {})
      .then(function(r){ var t = _firstText(r); return t ? JSON.parse(t) : {}; });
  };
  // Declared URL params, coerced server-side per arti-app.json's "params"
  // contract; {} when the app declares none. Undeclared query keys aren't here
  // but remain readable via new URLSearchParams(location.search).
  window.arti.params = (cfg && cfg.params) || {};
  // ── host-compat shims: let unmodified Cowork / Claude.ai artifacts use the
  // built-in llm.complete completion. They degrade gracefully (return ""/{text:""})
  // if the app's arti-app.json doesn't allowlist llm.complete (the proxy 403s).
  var _llmText = function(prompt, opts){
    return window.arti.callTool("llm","complete", Object.assign({prompt: prompt}, opts || {}))
      .then(function(r){ return (r && r.content && r.content[0] && r.content[0].text) || ""; });
  };
  if(!window.cowork){ window.cowork = { askClaude: function(prompt, history){
    var msgs = (history || []).concat([{role:"user", content: prompt}]);
    return window.arti.callTool("llm","complete",{messages: msgs})
      .then(function(r){ return {text: (r && r.content && r.content[0] && r.content[0].text) || ""}; })
      .catch(function(){ return {text: ""}; });
  } }; }
  if(!window.claude){ window.claude = { complete: function(prompt, opts){
    return _llmText(prompt, opts).catch(function(){ return ""; });
  } }; }
  if(!window.arti.askClaude){ window.arti.askClaude = function(prompt, opts){ return _llmText(prompt, opts).catch(function(){ return ""; }); }; }
})();`

// injectBaseHref inserts <base href="..."> as the first child of <head> so an
// APP's relative asset references resolve to the package-files API rather than
// under the /app/ document path. No-op for non-HTML or when a <base> already
// exists (author opted out). href must end with "/".
func injectBaseHref(body []byte, ct, href string) []byte {
	if !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	low := bytes.ToLower(body)
	if bytes.Contains(low, []byte("<base")) {
		return body // author set their own base; respect it
	}
	tag := []byte(`<base href="` + href + `">`)
	if at := headEnd(low); at >= 0 {
		out := make([]byte, 0, len(body)+len(tag))
		out = append(out, body[:at]...)
		out = append(out, tag...)
		return append(out, body[at:]...)
	}
	// no <head> — prepend so it still precedes any relative asset
	return append(tag, body...)
}

// headEnd returns the offset just past the opening <head …> tag in the
// lowercased body, or -1 if there's no head element. It accepts attributes
// (e.g. <head lang="en">) and won't mistake <header> for <head>.
func headEnd(low []byte) int {
	for from := 0; ; {
		i := bytes.Index(low[from:], []byte("<head"))
		if i < 0 {
			return -1
		}
		i += from
		end := i + len("<head")
		if end < len(low) {
			switch low[end] {
			case '>', ' ', '\t', '\n', '\r', '/': // a real <head>, <head …>, or <head/>
				if gt := bytes.IndexByte(low[i:], '>'); gt >= 0 {
					return i + gt + 1
				}
				return -1
			}
		}
		from = end // was <header…> or similar — keep looking
	}
}

// injectAppBridge inserts the window.__ARTI_APP__ config + the appBridgeJS
// shim into a served APP page (mirrors injectComments). The sandboxed page
// reaches the proxy with the injected Bearer token, never the session cookie.
// No-ops for non-HTML, unauthenticated requests, or when the app-token minter
// isn't wired.
func (s *Service) injectAppBridge(body []byte, ct, artifactID, email string, params map[string]any) []byte {
	if s.appToken == nil || email == "" || !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	tok, err := s.appToken(email, artifactID)
	if err != nil {
		return body
	}
	cfgM := map[string]any{"appId": artifactID, "token": tok, "endpoint": "/api/apps/mcp"}
	if params != nil {
		// Declared URL params, coerced per the manifest contract. json.Marshal
		// escapes <, >, & to < etc. by default, so an attacker-supplied value
		// like ?text=</script>… cannot break out of the <script> below.
		cfgM["params"] = params
	}
	cfg, _ := json.Marshal(cfgM)
	// Leading HTML comment so anyone who "view-source"s the served page understands
	// the token: it's injected per request (not part of the uploaded artifact), and
	// it's a per-viewer capability — see appTokenTTL in internal/apps for the ~12h.
	const appNote = "<!-- arti: window.__ARTI_APP__.token below is INJECTED per request — it is NOT part of " +
		"the uploaded artifact. It is a personal, per-viewer, app-scoped capability minted for the signed-in " +
		"viewer, valid ~12h, usable only for this app's allowlisted tools via /api/apps/mcp (it carries no " +
		"upstream secrets). Don't copy, share, screenshot, or commit it; reloading mints a fresh one. -->"
	snippet := []byte(appNote + "<script>window.__ARTI_APP__=" + string(cfg) + ";</script><script>" + appBridgeJS + "</script>")
	if i := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		return append(out, body[i:]...)
	}
	return append(body, snippet...)
}

// injectAppBridgeUser is injectAppBridge for a user-mode embed surface: there
// is no serve-time viewer, so NO token is injected — the config carries a
// `connect` block instead, and the bridge boots into its needs-user state (a
// visible Connect affordance that runs the /auth/embed/app-token popup
// handshake). appId is the version-pinned UUID the bridge forwards to the mint
// so the delivered token always matches this page's app_id.
//
// origins (the surface's embedder allowlist, from ARTI_EMBED_SURFACES) becomes
// connect.origins: the ONLY targets the bridge may postMessage a minted token
// to (the zero-click relay — see the bridge's announceToken). It is the same
// set frame-ancestors admits, so a token can never be announced to a window
// the surface didn't already declare as its embedder.
func (s *Service) injectAppBridgeUser(body []byte, ct, artifactID, surface string, origins []string, params map[string]any) []byte {
	if !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	connect := map[string]any{
		"surface": surface,
		"mint":    "/auth/embed/app-token",
		"poll":    "/embed/" + surface + "/token",
	}
	if len(origins) > 0 {
		connect["origins"] = origins
	}
	cfgM := map[string]any{
		"appId":    artifactID,
		"endpoint": "/api/apps/mcp",
		"connect":  connect,
	}
	if params != nil {
		cfgM["params"] = params
	}
	cfg, _ := json.Marshal(cfgM)
	const userNote = "<!-- arti: this page was served on a user-mode embed surface, so NO identity token is " +
		"baked in. Tool calls only work after the viewer clicks Connect, which mints a short-lived, " +
		"consent-gated, per-viewer token via /auth/embed/app-token. -->"
	snippet := []byte(userNote + "<script>window.__ARTI_APP__=" + string(cfg) + ";</script><script>" + appBridgeJS + "</script>")
	if i := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		return append(out, body[i:]...)
	}
	return append(body, snippet...)
}

// staleBannerJS builds the "you're on an older version" strip inside a served
// APP page. It mirrors StaleVersionBanner (the React strip the /s viewer
// shows): one amber line, a "View latest →" link, a × to dismiss.
//
// Two constraints shape it. (1) The page is an arbitrary uploaded app, so the
// strip cannot reflow it — it's a fixed overlay, built in JS from the DOM (no
// innerHTML, so nothing here can be an injection vector) and appended last, and
// it's dismissible for exactly that reason. (2) The document is served under
// CSP `sandbox` with an opaque origin: `allow-top-navigation-by-user-activation`
// is granted, so the click-driven link navigates fine, but storage may throw —
// dismissal is per page load, never persisted.
//
// It also publishes its height as --arti-top-strip on <html> (cleared on
// dismiss), matching the FE contract, so an app that wants to inset itself can
// read the var instead of being covered.
const staleBannerJS = `(function(){
  var cfg = window.__ARTI_STALE__;
  if(!cfg || !cfg.href){ return; }
  var ID = "arti-stale-strip", dismissed = false, queued = false, ro = null, mo = null, announced = false;
  function mount(){
    if(dismissed || !document.body || document.getElementById(ID)){ return; }
    var bar = document.createElement("div");
    bar.id = ID;
    // Live region on the FIRST mount only: a re-mount after a body clobber is
    // the same notice re-attached, and a chatty SPA would otherwise make a
    // screen reader re-announce it on every render.
    if(!announced){ bar.setAttribute("role", "status"); announced = true; }
    bar.setAttribute("style", "position:fixed;top:0;left:0;right:0;z-index:2147483646;" +
      "box-sizing:border-box;display:flex;align-items:center;gap:8px;padding:2px 12px;" +
      "font:12px/20px -apple-system,system-ui,BlinkMacSystemFont,'Segoe UI',sans-serif;" +
      "color:#78350f;background:rgba(255,251,235,.96);border-bottom:1px solid #fde68a;" +
      "-webkit-backdrop-filter:blur(4px);backdrop-filter:blur(4px)");
    var msg = document.createElement("span");
    msg.setAttribute("style", "min-width:0;overflow:hidden;white-space:nowrap;text-overflow:ellipsis");
    msg.textContent = "Viewing version v" + cfg.cur + " — a newer version v" + cfg.latest + " is available.";
    var link = document.createElement("a");
    link.href = cfg.href;
    link.textContent = "View latest →";
    link.setAttribute("style", "flex:none;color:#92400e;font-weight:600;text-decoration:underline");
    var close = document.createElement("button");
    close.type = "button";
    close.textContent = "×";
    close.setAttribute("aria-label", "dismiss newer version banner");
    // Longhands, not the "font" shorthand: the shorthand REQUIRES a family
    // term, so "font:16px/1 inherit" is invalid and the whole declaration —
    // size included — gets dropped.
    close.setAttribute("style", "margin-left:auto;flex:none;padding:0 3px;border:0;border-radius:3px;" +
      "background:transparent;color:#b45309;font-family:inherit;font-size:16px;line-height:1;cursor:pointer");
    close.addEventListener("click", function(){ dismissed = true; bar.remove(); stopRO(); stopMO(); clearVar(); });
    bar.appendChild(msg); bar.appendChild(link); bar.appendChild(close);
    document.body.appendChild(bar);
    publish(bar);
  }
  // Measured, not hardcoded, so a two-line wrap on a narrow viewport still
  // clears — same reasoning as the React strip. The observer is torn down on
  // dismiss and before each re-mount: left running, its height-0 callback fires
  // AFTER clearVar and rewrites the var it just removed (and a re-mount would
  // stack a fresh observer on every clobber).
  function publish(bar){
    var set = function(){
      document.documentElement.style.setProperty("--arti-top-strip",
        Math.round(bar.getBoundingClientRect().height) + "px");
    };
    set();
    stopRO();
    if(window.ResizeObserver){ ro = new ResizeObserver(set); ro.observe(bar); }
  }
  function stopRO(){ if(ro){ ro.disconnect(); ro = null; } }
  function clearVar(){ document.documentElement.style.removeProperty("--arti-top-strip"); }
  // An SPA that renders by clobbering document.body (React/Vue roots that mount
  // over it, or an app that rewrites body's markup wholesale) would silently
  // take the strip with it. Re-mount when that happens — coalesced to one check
  // per frame so a busy app's mutations stay cheap, never after a dismiss.
  function watch(){
    if(!window.MutationObserver || !document.body){ return; }
    mo = new MutationObserver(function(){
      if(dismissed || queued || document.getElementById(ID)){ return; }
      queued = true;
      requestAnimationFrame(function(){ queued = false; mount(); });
    });
    mo.observe(document.documentElement, { childList: true, subtree: true });
  }
  // Disconnected on dismiss: nothing can re-mount after that, so leaving it
  // subscribed would fire (and early-return) on every mutation of a busy SPA
  // for the rest of the page's life.
  function stopMO(){ if(mo){ mo.disconnect(); mo = null; } }
  function boot(){ mount(); watch(); }
  if(document.readyState === "loading"){ document.addEventListener("DOMContentLoaded", boot); }
  else { boot(); }
})();`

// injectStaleAppBanner appends the stale-version strip to a served APP page.
// No-ops for non-HTML and when there is nothing to warn about (stale == nil),
// so a page on the newest version is byte-identical to before.
func injectStaleAppBanner(body []byte, ct string, stale *staleNotice) []byte {
	if stale == nil || !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	// json.Marshal escapes <, >, & — a slug can't break out of the <script>.
	cfg, err := json.Marshal(map[string]any{
		"cur":    stale.Current,
		"latest": stale.Latest,
		"href":   stale.LatestURL,
	})
	if err != nil {
		return body
	}
	snippet := []byte("<script>window.__ARTI_STALE__=" + string(cfg) + ";</script><script>" + staleBannerJS + "</script>")
	if i := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		return append(out, body[i:]...)
	}
	return append(body, snippet...)
}

// setContentSecurityApp is setContentSecurity for APP artifacts: the same
// opaque sandbox (still NO allow-same-origin, so the page can't read
// arti_session or reach arti's other APIs — the governance guarantee), plus
// allow-popups + allow-popups-to-escape-sandbox so the app bridge can open the
// Runlayer OAuth consent in a popup.
//
// It also governs embedding: a `frame-ancestors` directive names the origins
// allowed to iframe the app (the embedding use case), and the global
// X-Frame-Options: SAMEORIGIN — which can't express an allowlist and would
// otherwise still block cross-origin framing — is dropped so frame-ancestors
// is authoritative. Empty allowlist ⇒ "'self'" (same-origin only).
func (s *Service) setContentSecurityApp(w http.ResponseWriter, ct string) {
	s.setContentSecurityAppFA(w, ct, "")
}

// setContentSecurityAppFA is setContentSecurityApp with an explicit
// frame-ancestors source list. fa == "" falls back to the global
// ARTI_APP_FRAME_ANCESTORS (s.appFrameAncestors), then to 'self'. The embed
// surfaces pass a per-surface list ("'self' https://app.frontapp.com") so a
// given surface relaxes framing only to its own origin, independent of the
// global app default.
func (s *Service) setContentSecurityAppFA(w http.ResponseWriter, ct, fa string) {
	w.Header().Set("Content-Type", ct)
	if strings.HasPrefix(strings.ToLower(ct), "text/html") {
		if fa == "" {
			fa = s.appFrameAncestors
		}
		if fa == "" {
			fa = "'self'"
		}
		w.Header().Set("Content-Security-Policy",
			"sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-downloads; frame-ancestors "+fa)
		w.Header().Del("X-Frame-Options")
	}
}

// writeServedContent writes an artifact body to the response, injecting the
// comments overlay for text/html (so a standalone HTML artifact served via an
// iframe `src` gets the same in-page commenting as a PACKAGE file does). For
// non-HTML — including the multi-MB zip of a PACKAGE download — it streams
// straight through without buffering. injectComments no-ops on non-HTML, but we
// gate on the content type here too so we never read a large zip into memory.
func (s *Service) writeServedContent(w http.ResponseWriter, rc io.Reader, ct, artifactID, artifactType, caller, callerName, callerPicture string, sizeBytes *int64, fullPage bool) {
	if strings.HasPrefix(strings.ToLower(ct), "text/html") {
		body, err := io.ReadAll(rc)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		setContentSecurityMaybeFullPage(w, ct, fullPage)
		// ATTACHMENT uploads never get the comments overlay — you can't
		// comment on an attachment. Everything else (standalone HTML, package
		// files) keeps the in-page injected overlay. Both paths do a single
		// buffered Write, so net/http sets Content-Length itself — and comment
		// injection changes the length, so we must NOT pre-set the stored size.
		if artifactType == pgstore.TypeAttachment {
			_, _ = w.Write(body)
		} else {
			_, _ = w.Write(s.injectComments(body, ct, artifactID, caller, callerName, callerPicture))
		}
		return
	}
	setContentSecurity(w, ct)
	// Advertise the body length up front so HTTP clients (and agents) can size
	// the artifact before consuming the stream. Safe here: the non-HTML path
	// streams the stored bytes verbatim (no injection), so size_bytes is exact.
	if sizeBytes != nil && *sizeBytes >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(*sizeBytes, 10))
	}
	_, _ = io.Copy(w, rc)
}

func setContentSecurity(w http.ResponseWriter, ct string) {
	w.Header().Set("Content-Type", ct)
	switch {
	case strings.HasPrefix(strings.ToLower(ct), "text/html"):
		w.Header().Set("Content-Security-Policy",
			"sandbox allow-scripts allow-top-navigation-by-user-activation allow-downloads")
	case !isScriptSafeMedia(ct):
		// Non-HTML content the browser may execute as an active document
		// (image/svg+xml, application/xml, ...) is served under a script-less
		// `sandbox` so an uploaded SVG/XML cannot run JS on the arti origin
		// (stored XSS). Harmless for the non-scriptable types it also covers
		// (json, text, octet-stream). Raster images / PDF / audio / video skip
		// it — they don't execute page script, and we avoid any inline-render
		// regression.
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
}

// fullPageContext reports whether this request is the chrome-less full-page
// viewer load. FullPageView appends ?ctx=fullpage to the entry document's URL
// so the server can widen the sandbox for that context ONLY (see
// setContentSecurityMaybeFullPage). It is NOT set on in-viewer previews,
// top-level raw navigations, or the by-slug raw route.
func fullPageContext(r *http.Request) bool {
	return r.URL.Query().Get("ctx") == "fullpage"
}

// setContentSecurityMaybeFullPage is setContentSecurity, but in the full-page
// viewer context it grants popups for HTML so a report's target="_blank" /
// window.open links open on a NORMAL click (otherwise they need cmd-click,
// because CSP `sandbox` without allow-popups blocks new browsing contexts).
// The granted set matches the APP/embed sandbox — but still WITHOUT
// allow-same-origin, so the page cannot read arti_session, the arti DOM, or
// call arti's APIs as the viewer. HTML-ONLY: every other content type falls
// through to setContentSecurity, so a forged ?ctx=fullpage can never turn an
// uploaded SVG/XML into a script-enabled document.
func setContentSecurityMaybeFullPage(w http.ResponseWriter, ct string, fullPage bool) {
	if fullPage && strings.HasPrefix(strings.ToLower(ct), "text/html") {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Security-Policy",
			"sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-downloads")
		return
	}
	setContentSecurity(w, ct)
}

// isScriptSafeMedia reports whether ct is a media type the browser renders
// inline WITHOUT executing in-page script: raster images, PDF, audio, video.
// image/svg+xml is deliberately EXCLUDED — an SVG is an active, scriptable
// document despite its image/* type, so it must be sandboxed.
func isScriptSafeMedia(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp",
		"image/avif", "image/bmp", "image/x-icon", "image/vnd.microsoft.icon",
		"application/pdf":
		return true
	}
	return strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/")
}

// setContentDisposition sets a download-friendly Content-Disposition
// based on the artifact's title + MIME-derived extension. Disposition
// is `inline` for content the browser can render in-page (text, html,
// image, pdf, audio, video) so /a/{id} links — and the viewer's
// <iframe>/<img> previews — keep working; `attachment` for everything
// else (binary blobs, archives, PACKAGE zips) so the browser saves them
// with a sensible filename instead of "{uuid}". Either way the filename
// gets the right extension so curl > out and "Save As" both produce a
// usable file.
//
// ATTACHMENT is NOT force-downloaded: a previewable attachment (a PDF or
// image) renders inline in the viewer like any other artifact; only
// non-renderable bytes fall through to `attachment` via isInlineRenderable.
// Attachments are creator-only, so inline serving can only ever be
// triggered by the uploader viewing their own file (no cross-user surface).
//
// A ?download=1 query param forces `attachment` regardless — useful
// for "right-click → save" affordances (the viewer's Download button).
func setContentDisposition(w http.ResponseWriter, r *http.Request, title, ct, artifactType string) {
	filename := deriveDownloadFilename(title, ct)
	if filename == "" {
		return
	}
	disp := "inline"
	if r.URL.Query().Get("download") == "1" ||
		pgstore.IsPackageLike(artifactType) || // PACKAGE + APP are zips
		!isInlineRenderable(ct) {
		disp = "attachment"
	}
	// RFC 5987 encoded filename* covers non-ASCII titles; plain filename=
	// stays for legacy clients. Quote-escape the ASCII fallback so titles
	// containing quotes / backslashes don't break the header.
	asciiFallback := sanitizeASCIIFilename(filename)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`,
			disp, asciiFallback, urlEscapeRFC5987(filename)))
}

// deriveDownloadFilename combines a sanitized stem (from title) with
// an extension chosen from ct. Returns "" if title is empty.
func deriveDownloadFilename(title, ct string) string {
	stem := strings.TrimSpace(title)
	if stem == "" {
		return ""
	}
	// Drop path separators; keep titles like "fund-launch v2".
	stem = strings.ReplaceAll(stem, "/", "_")
	stem = strings.ReplaceAll(stem, "\\", "_")

	want := extForContentType(ct)
	if want == "" {
		return stem
	}
	// Don't double-append: titles already ending in the right extension
	// (case-insensitive) keep what the author wrote.
	if strings.HasSuffix(strings.ToLower(stem), want) {
		return stem
	}
	return stem + want
}

// extForContentType picks the canonical extension for a MIME, falling
// back to a hand-curated map for the common cases mime.ExtensionsByType
// returns ambiguously (e.g. image/jpeg → ".jfif" instead of ".jpg" on
// some platforms).
func extForContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "text/plain":
		return ".txt"
	case "text/markdown", "text/x-markdown":
		return ".md"
	case "text/html":
		return ".html"
	case "text/css":
		return ".css"
	case "text/csv":
		return ".csv"
	case "text/xml", "application/xml":
		return ".xml"
	case "application/json", "application/ld+json":
		return ".json"
	case "application/yaml", "application/x-yaml":
		return ".yaml"
	case "application/javascript", "application/ecmascript":
		return ".js"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "application/gzip", "application/x-gzip":
		return ".gz"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	}
	// Structured-suffix types carry their base format in the suffix, so a
	// vendor type like application/vnd.arti.diagram+json downloads as .json
	// rather than extensionless.
	if strings.HasSuffix(ct, "+json") {
		return ".json"
	}
	// Last-resort lookup; mime.ExtensionsByType returns slices ordered
	// by MIME registry which isn't always the friendliest (e.g. ".jfif"
	// for jpeg) so we prefer the curated map above.
	if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// isInlineRenderable reports whether browsers can display ct in-page
// without forcing a download. Used to pick `inline` vs `attachment` in
// Content-Disposition. Conservative — anything we're unsure about gets
// `attachment` so the browser saves it instead of trying to render it.
func isInlineRenderable(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") ||
		strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "audio/") ||
		strings.HasPrefix(ct, "video/") {
		return true
	}
	// +json types are JSON text — browsers show them in-page like any other
	// JSON, so forcing a download would be gratuitous.
	if strings.HasSuffix(ct, "+json") {
		return true
	}
	switch ct {
	case "application/pdf",
		"application/json",
		"application/ld+json",
		"application/xml",
		"application/javascript":
		return true
	}
	return false
}

// sanitizeASCIIFilename strips non-ASCII + control chars and escapes
// quote/backslash for the plain `filename="..."` parameter. Clients
// that grok RFC 5987 will use the `filename*` instead and see the full
// UTF-8 title.
func sanitizeASCIIFilename(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		case r == '"' || r == '\\':
			b.WriteByte('_')
		case r > 0x7e:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "download"
	}
	return out
}

// urlEscapeRFC5987 percent-encodes per RFC 5987 attr-char. Used for
// the `filename*=UTF-8”<value>` parameter so non-ASCII titles round-
// trip in browsers that support it.
func urlEscapeRFC5987(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '!' || r == '#' || r == '$' || r == '&' || r == '+' ||
			r == '-' || r == '.' || r == '^' || r == '_' || r == '`' ||
			r == '|' || r == '~':
			b.WriteRune(r)
		default:
			// percent-encode each UTF-8 byte
			for _, by := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", by)
			}
		}
	}
	return b.String()
}

// isValidContentType accepts standard MIME types of the form
// `type/subtype` with an optional `; charset=…` parameter. Rejects
// uploads where someone tries to smuggle scripts or extra params
// through the content_type field.
//
// Accepts: text/markdown, text/html, text/html; charset=utf-8,
// application/json, image/png, application/vnd.foo.bar+xml.
// Rejects:  text/html;<script>, text/html\nX-Foo:bar, "", */*.
var contentTypeRe = regexp.MustCompile(
	`^[a-zA-Z0-9!#$&^_.+\-]+/[a-zA-Z0-9!#$&^_.+\-]+(\s*;\s*charset=[a-zA-Z0-9._\-]+)?$`,
)

func isValidContentType(ct string) bool {
	return contentTypeRe.MatchString(strings.TrimSpace(ct))
}

// isTextualContentType reports whether a content_type holds text the TEXT
// artifact type + viewer can render as text. Everything else (pdf, images,
// zips, binaries) must be an ATTACHMENT or live inside a PACKAGE — stored as
// TEXT it would render as garbage. Charset/params are ignored.
func isTextualContentType(ct string) bool {
	base := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	if strings.HasPrefix(base, "text/") {
		return true
	}
	// Structured-suffix JSON types (RFC 6839) — application/vnd.foo+json — are
	// JSON text and render fine in the viewer. This is what lets the web UI's
	// diagram format (application/vnd.arti.diagram+json) be a TEXT artifact
	// instead of needing a new artifact_type.
	if strings.HasSuffix(base, "+json") {
		return true
	}
	switch base {
	case "application/json", "application/yaml", "application/javascript":
		return true
	}
	return false
}

type badRequest struct{ msg string }

func (b badRequest) Error() string   { return b.msg }
func errBadRequest(msg string) error { return badRequest{msg: msg} }

type forbidden struct{ msg string }

func (f forbidden) Error() string   { return f.msg }
func errForbidden(msg string) error { return forbidden{msg: msg} }

// skillLabel marks an artifact as a managed skill (couch's skill catalog).
const skillLabel = "kind:skill"

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// requireSkillWrite gates writes to `kind:skill` artifacts: only callers with
// MANAGE_SKILLS (or the broader MANAGE_ARTIFACTS) may create/version/relabel
// them. Pass EVERY relevant label set — the incoming labels AND the existing
// artifact's labels — and the guard fires if ANY of them carries the skill
// tag. That way a caller can neither sneak the tag onto a new/non-skill
// artifact nor strip it off (or otherwise edit) a protected skill without the
// permission. No-op when no set carries the tag.
func (s *Service) requireSkillWrite(ctx context.Context, caller string, labelSets ...[]string) error {
	isSkill := false
	for _, ls := range labelSets {
		if containsLabel(ls, skillLabel) {
			isSkill = true
			break
		}
	}
	if !isSkill {
		return nil
	}
	for _, p := range []rbac.Permission{rbac.ManageSkills, rbac.ManageArtifacts} {
		ok, err := s.store.HasPermission(ctx, caller, p)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errForbidden("writing skill artifacts (kind:skill) requires the MANAGE_SKILLS permission")
}

// DiagramContentType is the body type of a diagram artifact. A diagram is an
// ordinary TEXT artifact — this is the only thing that distinguishes it — which
// is how it inherits slugs, versioning, compare, comments and access control.
const DiagramContentType = "application/vnd.arti.diagram+json"

// typePseudo maps the sidebar's pseudo-types onto content_type globs. They don't
// name a real artifact_type (those are TEXT / PACKAGE / APP / ATTACHMENT) but a
// family of bodies, so the rail can offer one-click filtering for the shapes
// people actually look for without anyone typing content_type syntax.
//
// Keys are compared case-insensitively so the `type:markdown` search token works
// as well as the rail's uppercase value.
var typePseudo = map[string]string{
	"MARKDOWN": "text/markdown*",
	"HTML":     "text/html*",
	"DIAGRAM":  DiagramContentType + "*",
	"JSON":     "application/json*",
	"PDF":      "application/pdf*",
	"IMAGE":    "image/*",
}

// ApplyTypeFilter resolves one `type` filter value onto a ListInput: a
// pseudo-type becomes a content_type glob, anything else an artifact_type.
//
// Deliberately shared by all three entry points that accept a type filter — the
// ?type= query param, the `type:` search token, and the MCP/CLI list tool. It
// used to live inline in the query-param path only, so `type=MARKDOWN` returned
// results in the web UI but zero rows via MCP and via the search box (both fell
// through to artifact_type, which matches nothing).
func ApplyTypeFilter(in *pgstore.ListInput, v string) {
	if v == "" {
		return
	}
	if ct, ok := typePseudo[strings.ToUpper(v)]; ok {
		in.ContentType = &ct
		return
	}
	// Not a pseudo-type: pass through verbatim, preserving the caller's casing.
	in.ArtifactType = &v
}

// ApplyNotTypeFilter is ApplyTypeFilter for a negated `-type:` token: the same
// resolution, appended to the exclusion lists instead of the scalar filters.
//
// It exists because negation reintroduced the very bug above: `-type:markdown`
// was appended to NotArtifactType verbatim, and no artifact_type is ever named
// "markdown", so the exclusion quietly matched nothing while the positive
// filter worked. Both directions now share one resolution.
func ApplyNotTypeFilter(in *pgstore.ListInput, v string) {
	if v == "" {
		return
	}
	if ct, ok := typePseudo[strings.ToUpper(v)]; ok {
		// addNotScalar globs on `*`, so the exclusion covers the whole family.
		in.NotContentType = append(in.NotContentType, ct)
		return
	}
	in.NotArtifactType = append(in.NotArtifactType, v)
}

// slugConflict is returned from Create when the caller asked for
// EnsureNew=true and the slug already has a non-deleted version. HTTP
// layer maps it to 409; CLI / MCP surface the message verbatim.
type slugConflict struct {
	slug            string
	existingVersion int32
	creator         string
}

func (e slugConflict) Error() string {
	return fmt.Sprintf("slug %q already exists (v%d by %s); drop --ensure-new or pick a different slug",
		e.slug, e.existingVersion, e.creator)
}

// fieldSyntax recognizes search tokens like `slug:smoke-test` or
// `scope:user:tian@al.com`. The first colon splits key from value; the
// value is taken verbatim so it may itself contain colons.
var fieldKeys = map[string]bool{
	"slug": true, "creator": true, "scope": true, "type": true, "label": true,
	"content_type": true,
}

// negatableKeys are the fields a leading `-` may exclude (e.g. `-label:foo`).
// slug is deliberately absent: a `-slug:` token falls through to free text
// rather than acting as a filter.
var negatableKeys = map[string]bool{
	"creator": true, "scope": true, "type": true, "label": true, "content_type": true,
}

// parseQuery extracts field:value tokens from a search query. A leading `-`
// on a negatable field routes the token into `negated` instead of `filters`;
// every other token (unknown key, empty value, bare word, or `-slug:…`) is
// returned in remainder unchanged. Multiple occurrences of the same key with
// non-list semantics overwrite; `label` accumulates. Negated tokens always
// accumulate (each is an independent exclusion).
func parseQuery(q string) (filters, negated map[string][]string, remainder string) {
	filters = map[string][]string{}
	negated = map[string][]string{}
	var rem []string
	for _, tok := range strings.Fields(q) {
		body, neg := tok, false
		if strings.HasPrefix(tok, "-") {
			body, neg = tok[1:], true
		}
		i := strings.Index(body, ":")
		if i <= 0 {
			rem = append(rem, tok)
			continue
		}
		k, v := body[:i], body[i+1:]
		if v == "" {
			rem = append(rem, tok)
			continue
		}
		if neg {
			if !negatableKeys[k] {
				rem = append(rem, tok)
				continue
			}
			negated[k] = append(negated[k], v)
			continue
		}
		if !fieldKeys[k] {
			rem = append(rem, tok)
			continue
		}
		filters[k] = append(filters[k], v)
	}
	return filters, negated, strings.Join(rem, " ")
}

// mergeFilters folds parsed field:value tokens into a ListInput. Existing
// values on `in` win (so explicit query params still take precedence over
// the syntax sugar in the q box).
func mergeFilters(in *pgstore.ListInput, filters, negated map[string][]string) {
	first := func(k string) *string {
		v := filters[k]
		if len(v) == 0 {
			return nil
		}
		return &v[0]
	}
	if in.ArtifactType == nil && in.ContentType == nil {
		if v := first("type"); v != nil {
			// Same resolution as ?type=, so `type:markdown` in the search
			// box means what the rail's markdown chip means.
			ApplyTypeFilter(in, *v)
		}
	}
	if in.Creator == nil {
		if v := first("creator"); v != nil {
			in.Creator = v
		}
	}
	if in.Scope == nil {
		if v := first("scope"); v != nil {
			in.Scope = v
		}
	}
	if in.Slug == nil {
		if v := first("slug"); v != nil {
			in.Slug = v
		}
	}
	if in.ContentType == nil {
		if v := first("content_type"); v != nil {
			in.ContentType = v
		}
	}
	if labels := filters["label"]; len(labels) > 0 {
		in.Labels = append(in.Labels, labels...)
	}
	in.NotLabels = append(in.NotLabels, negated["label"]...)
	in.NotScope = append(in.NotScope, negated["scope"]...)
	in.NotCreator = append(in.NotCreator, negated["creator"]...)
	for _, v := range negated["type"] {
		ApplyNotTypeFilter(in, v)
	}
	in.NotContentType = append(in.NotContentType, negated["content_type"]...)
}

func parseList(r *http.Request) pgstore.ListInput {
	q := r.URL.Query()
	in := pgstore.ListInput{Limit: 50}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			in.Limit = int32(n)
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			in.Offset = int32(n)
		}
	}
	if v := q.Get("type"); v != "" {
		ApplyTypeFilter(&in, v)
	}
	if v := q.Get("creator"); v != "" {
		in.Creator = &v
	}
	if v := q.Get("scope"); v != "" {
		in.Scope = &v
	}
	if v := q.Get("slug"); v != "" {
		in.Slug = &v
	}
	if vs := q["label"]; len(vs) > 0 {
		in.Labels = vs
	}
	switch q.Get("archived") {
	case "only":
		in.OnlyArchived = true
	case "include":
		in.IncludeArchived = true
	}
	// Back-compat with the original parameter name.
	if q.Get("include_archived") == "true" {
		in.IncludeArchived = true
	}
	if v := q.Get("order_by"); v != "" && pgstore.IsSortKey(v) {
		in.OrderBy = v
	}
	if v := q.Get("order_dir"); v != "" {
		in.OrderDir = v
	}
	return in
}

func parseVersion(r *http.Request) *int32 {
	v := r.URL.Query().Get("version")
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	x := int32(n)
	return &x
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	v := chi.URLParam(r, name)
	id, err := uuid.Parse(v)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "invalid UUID: "+v)
		return uuid.Nil, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, Error{Detail: detail, Code: code})
}

func writeMaybeNotFound(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not-found", "not found")
		return true
	}
	writeError(w, http.StatusInternalServerError, "internal", err.Error())
	return true
}

func writeBadOrInternal(w http.ResponseWriter, err error) {
	var br badRequest
	if errors.As(err, &br) {
		writeError(w, http.StatusBadRequest, "bad-request", br.msg)
		return
	}
	if errors.Is(err, pgstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not-found", "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", err.Error())
}

func mergeMeta(existing json.RawMessage, add map[string]any) json.RawMessage {
	m := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &m)
	}
	for k, v := range add {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return raw
}
