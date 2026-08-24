package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// External timed share links. A link is a CAPABILITY: it names one document
// and grants read of it to whoever holds the URL, until it expires or is
// revoked. It never resolves to an identity, which is what keeps it from
// inheriting the admin short-circuit at the top of checkAccess.
//
// See DD-0069 for the threat model. The parts that are load-bearing rather
// than incidental are marked in the code that implements them.

// ShareConfig carries the share-link knobs. The zero value disables the
// feature, which is also the config default: ARTI_SHARE_ENABLED is the
// backout, so nothing is exposed until it is set.
type ShareConfig struct {
	Enabled bool
	MaxTTL  time.Duration
}

// SetShareConfig wires the share-link settings. Follows the setter convention
// the rest of this service's optional wiring uses (SetAppTokenFn and friends)
// rather than widening NewService.
func (s *Service) SetShareConfig(c ShareConfig) { s.share = c }

// ShareEnabled reports whether share links are switched on, for the /api/me
// capability bit and the route mount.
func (s *Service) ShareEnabled() bool { return s.share.Enabled }

// IsForbidden reports whether err is a 403-class refusal, as opposed to
// ErrNotFound, which the access layer returns instead of 403 so a caller who
// cannot see a document is not told that it exists. Exported because the
// distinction is a security property that tests outside this package need to
// assert, and the error type itself is unexported.
func IsForbidden(err error) bool {
	var f forbidden
	return errors.As(err, &f)
}

// shareTTLs is the closed set a caller may ask for. A free-form duration would
// let a caller mint something indistinguishable from permanent, and an
// unbounded external link is the property that made bearer links unacceptable
// the last time they were proposed.
var shareTTLs = map[string]time.Duration{
	"1h":  time.Hour,
	"8h":  8 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// NewShareToken mints a share-link token: 32 bytes of crypto/rand, base64url
// without padding. Returns the plaintext (shown to the minter exactly once and
// never recoverable afterwards), sha256(plaintext) for storage, and the first
// 8 characters as a display prefix. Mirrors apikeys.NewKey — arti stores only
// the digest, so a database dump yields no usable links.
func NewShareToken() (string, []byte, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(tok))
	return tok, sum[:], tok[:8], nil
}

// HashShareToken returns sha256(token) — the share_links lookup key. Because
// the digest is the key, the serve path performs no secret-dependent
// comparison at all, which is a better position than a constant-time compare.
func HashShareToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// MintShareRequest is one mint. Scope is "version" (pinned to the version that
// is current now) or "slug" (tracking the slug's newest version).
type MintShareRequest struct {
	Scope string `json:"scope"`
	TTL   string `json:"ttl"`
	Note  string `json:"note"`
}

// MintShareResult carries the one and only sighting of the URL.
type MintShareResult struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	TokenPrefix string    `json:"token_prefix"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// ShareLinkDTO is a link as the owner sees it in the Share dialog. It has no
// token field and must never gain one: the prefix identifies a row, it does
// not open one.
type ShareLinkDTO struct {
	ID           string     `json:"id"`
	TokenPrefix  string     `json:"token_prefix"`
	Scope        string     `json:"scope"`
	Note         string     `json:"note"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	OpenCount    int64      `json:"open_count"`
	LastOpenedAt *time.Time `json:"last_opened_at,omitempty"`
}

// ShareOpenDTO is one recorded open.
type ShareOpenDTO struct {
	At        time.Time `json:"at"`
	IP        string    `json:"ip"`
	PeerAddr  string    `json:"peer_addr"`
	UserAgent string    `json:"user_agent"`
}

// shareAuthority confirms the caller may act on `row`'s share links.
//
// The owner check runs FIRST, and deliberately so. checkAccess hides an
// ARCHIVED document from everyone except the row's own Creator
// (server.go:638), and versioning reassigns Creator to whoever pushed the
// latest version. Checking access first therefore locked the slug's owner out
// of revoking links on an archived document whose tip a delegated writer had
// published — and since archiving does not revoke anything, unarchiving would
// bring those still-live links back. Archiving must never strand a link with
// nobody able to kill it.
//
// For everyone else the order still hides existence: a caller who cannot see
// the document gets ErrNotFound (404) from checkAccess, and only a caller who
// can see it but does not own it gets forbidden (403).
// shareLineageAmbiguous reports whether a slug's immutable owner and its live
// lineage disagree, which happens in two ways:
//
//   - REUSE. An all-archived slug is free for anyone to claim
//     (pgstore/store.go:355). SlugCreator still names whoever walked away, so
//     without this check the former owner could mint external links on the new
//     occupant's private document.
//   - A SHIFTED LINEAGE. The owner archives their own early versions while a
//     delegated writer's later version stays live, so the earliest live
//     creator is the writer.
//
// Both are refused rather than resolved. Deciding who "really" owns the slug
// needs a notion of lineage identity that arti does not have — see
// bl-arti-isdocowner-slug-reuse — and guessing in either direction hands
// someone the ability to publish a document to the internet. Refusing is
// recoverable (unarchive, or ask an admin); guessing wrong is not.
//
// Returns false when the slug has no live version at all: nothing can be
// ambiguous about a lineage nobody currently occupies, and minting is refused
// on an archived document anyway.
func (s *Service) liveLineageOwner(ctx context.Context, row sqlc.Artifact) (string, error) {
	if row.NamedSlug == nil || *row.NamedSlug == "" {
		return "", nil
	}
	owner, err := s.store.SlugLiveCreator(ctx, *row.NamedSlug)
	if errors.Is(err, pgstore.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return owner, nil
}

func (s *Service) shareAuthority(ctx context.Context, row sqlc.Artifact, caller string) error {
	err := s.isDocOwner(ctx, row, caller)
	if err == nil {
		return nil
	}
	// Only a forbidden result means "not the owner". isDocOwner also returns
	// real failures — an RBAC lookup error from canManageArtifacts, a
	// SlugCreator query error — and swallowing those would turn a transient
	// infrastructure fault into an authorization answer: the true owner would
	// get 403 (they still pass checkAccess) or 404 on an archived tip they did
	// not create, instead of the 500 that says "ask again". That would also
	// re-strand the archived-document revoke path this ordering exists to fix.
	if !IsForbidden(err) {
		return err
	}
	if aerr := s.checkAccess(ctx, row, caller); aerr != nil {
		return aerr
	}
	return errForbidden("only the document's owner or an admin may manage its share links")
}

// shareMintAuthority gates CREATING an external link, which is a larger act
// than managing one that already exists: it publishes a document to the
// internet. On top of shareAuthority it refuses whenever the slug's ownership
// is ambiguous (see shareLineageAmbiguous), so a disputed lineage can never be
// the basis for new exposure. Admins are exempt, and are the recovery path.
// It returns the live-lineage owner it validated. That value — not a second
// read — is what a slug-scoped link records as its anchor: taking the anchor
// from a separate query would leave a window where the check passes against
// one lineage and the anchor records another, which is enough for a mint to
// be authorized against the caller's own slug while anchoring to whoever
// claimed it in between.
func (s *Service) shareMintAuthority(ctx context.Context, row sqlc.Artifact, caller string) (string, error) {
	if err := s.shareAuthority(ctx, row, caller); err != nil {
		return "", err
	}
	liveOwner, err := s.liveLineageOwner(ctx, row)
	if err != nil {
		return "", err
	}
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return "", err
	}
	if admin {
		return liveOwner, nil
	}
	if liveOwner != "" && !strings.EqualFold(liveOwner, caller) {
		return "", errForbidden("this slug's ownership is ambiguous — its earliest live version was created by someone else, so external sharing is refused until that is resolved or an admin acts")
	}
	return liveOwner, nil
}

// shareDocAuthority loads the artifact behind an id and applies shareAuthority.
func (s *Service) shareDocAuthority(ctx context.Context, artifactID, caller string) (sqlc.Artifact, error) {
	id, err := uuid.Parse(artifactID)
	if err != nil {
		return sqlc.Artifact{}, errBadRequest("bad artifact id")
	}
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return sqlc.Artifact{}, err
	}
	if err := s.shareAuthority(ctx, row, caller); err != nil {
		return sqlc.Artifact{}, err
	}
	return row, nil
}

// MintShare creates an external link for one document. Authority is
// isDocOwner: minting publishes a document to the internet, which is a larger
// act than any ACL widening, so it takes at least the authority an ACL change
// takes.
func (s *Service) MintShare(ctx context.Context, artifactID, caller string, req MintShareRequest) (MintShareResult, error) {
	if !s.share.Enabled {
		return MintShareResult{}, pgstore.ErrNotFound
	}
	id, perr := uuid.Parse(artifactID)
	if perr != nil {
		return MintShareResult{}, errBadRequest("bad artifact id")
	}
	row, err := s.store.GetByID(ctx, id)
	if err != nil {
		return MintShareResult{}, err
	}
	anchorOwner, err := s.shareMintAuthority(ctx, row, caller)
	if err != nil {
		return MintShareResult{}, err
	}

	// An APP executes JavaScript that reaches arti's governed tool proxy and
	// the LLM. Handing that to an anonymous visitor is a different feature
	// with a different threat model, so it is refused here — and again at
	// serve time, because a tracking link resolves whatever the latest version
	// is and a slug's type is not pinned across versions.
	if row.ArtifactType == pgstore.TypeApp {
		return MintShareResult{}, errBadRequest("APP artifacts cannot be shared externally")
	}
	if row.DeletedAt.Valid {
		return MintShareResult{}, errBadRequest("cannot share an archived artifact")
	}

	ttl, ok := shareTTLs[req.TTL]
	if !ok {
		return MintShareResult{}, errBadRequest(fmt.Sprintf("ttl %q is not one of 1h, 8h, 24h, 7d, 30d", req.TTL))
	}
	if s.share.MaxTTL > 0 && ttl > s.share.MaxTTL {
		return MintShareResult{}, errBadRequest(fmt.Sprintf("ttl %s exceeds the configured maximum of %s", ttl, s.share.MaxTTL))
	}

	in := pgstore.ShareLinkInput{
		CreatedBy: caller,
		Note:      req.Note,
		ExpiresAt: time.Now().Add(ttl),
	}
	switch req.Scope {
	case "version":
		id := pgstore.UUIDFromPG(row.ArtifactID)
		in.ArtifactID = &id
	case "slug":
		if row.NamedSlug == nil || *row.NamedSlug == "" {
			return MintShareResult{}, errBadRequest("this artifact has no slug, so it can only be shared as a pinned version")
		}
		in.Slug = row.NamedSlug
		// The anchor is the lineage owner shareMintAuthority just validated,
		// not a fresh read — see its doc comment for the window that would
		// otherwise open between the two.
		if anchorOwner == "" {
			return MintShareResult{}, errBadRequest("this slug has no live version to anchor a tracking link to")
		}
		in.AnchorOwner = anchorOwner
	default:
		return MintShareResult{}, errBadRequest(`scope must be "version" or "slug"`)
	}

	tok, hash, prefix, err := NewShareToken()
	if err != nil {
		return MintShareResult{}, err
	}
	in.TokenHash, in.TokenPrefix = hash, prefix

	link, err := s.store.CreateShareLink(ctx, in)
	if err != nil {
		return MintShareResult{}, err
	}
	return MintShareResult{
		ID:          pgstore.UUIDFromPG(link.ID).String(),
		URL:         s.baseURL + "/share/" + tok,
		TokenPrefix: prefix,
		ExpiresAt:   link.ExpiresAt.Time,
	}, nil
}

// ListShares returns every link for the document, across the slug's version
// lineage. Gated on isDocOwner: that a document is externally shared is itself
// information the owner controls.
func (s *Service) ListShares(ctx context.Context, artifactID, caller string) ([]ShareLinkDTO, error) {
	row, err := s.shareDocAuthority(ctx, artifactID, caller)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListShareLinksForDoc(ctx, pgstore.UUIDFromPG(row.ArtifactID), row.NamedSlug)
	if err != nil {
		return nil, err
	}
	out := make([]ShareLinkDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, shareLinkDTO(r))
	}
	return out, nil
}

// shareLinkAuthority loads a link and confirms the caller owns the document it
// points at. Authority follows the DOCUMENT, not created_by: an owner must be
// able to kill a link somebody else minted, and whoever minted it must lose
// that power along with ownership.
func (s *Service) shareLinkAuthority(ctx context.Context, shareID, caller string) (sqlc.ShareLink, error) {
	id, err := uuid.Parse(shareID)
	if err != nil {
		return sqlc.ShareLink{}, errBadRequest("bad share id")
	}
	link, err := s.store.GetShareLink(ctx, pgUUIDOf(id))
	if err != nil {
		return sqlc.ShareLink{}, err
	}
	row, err := s.shareTarget(ctx, link)
	if err != nil {
		return sqlc.ShareLink{}, err
	}
	if err := s.shareAuthority(ctx, row, caller); err != nil {
		return sqlc.ShareLink{}, err
	}
	return link, nil
}

func (s *Service) RevokeShare(ctx context.Context, shareID, caller string) error {
	link, err := s.shareLinkAuthority(ctx, shareID, caller)
	if err != nil {
		return err
	}
	return s.store.RevokeShareLink(ctx, link.ID, caller)
}

func (s *Service) ShareOpens(ctx context.Context, shareID, caller string) ([]ShareOpenDTO, error) {
	link, err := s.shareLinkAuthority(ctx, shareID, caller)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListShareLinkOpens(ctx, link.ID, 200)
	if err != nil {
		return nil, err
	}
	out := make([]ShareOpenDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, ShareOpenDTO{At: r.At.Time, IP: r.Ip, PeerAddr: r.PeerAddr, UserAgent: r.UserAgent})
	}
	return out, nil
}

// shareTarget resolves the artifact a link points at, WITHOUT the liveness
// checks ResolveShare applies. Used by the authority helpers, which need the
// document even when the link is revoked or expired — an owner must still be
// able to list and inspect a dead link.
func (s *Service) shareTarget(ctx context.Context, link sqlc.ShareLink) (sqlc.Artifact, error) {
	if link.ArtifactID.Valid {
		return s.store.GetByID(ctx, pgstore.UUIDFromPG(link.ArtifactID))
	}
	return s.store.GetLatestBySlugAnyState(ctx, link.Slug)
}

// ResolveShare turns a share token into the artifact it may serve.
//
// Every refusal returns pgstore.ErrNotFound and nothing else, so the caller
// emits one identical 404 for all of them and a holder of a revoked link
// cannot tell it from a random string.
//
// BRANCH ORDER IS A SECURITY PROPERTY — DO NOT REORDER.
// The revoked and expired checks come BEFORE the artifact lookup so that the
// cheap branches (one indexed query on token_hash) are the ones a stranger
// reaches, and the two-query branches are reachable only with a token that
// already passed the live checks. Resolving the artifact first would let a
// revoked-link holder time the difference and learn whether the document
// still exists.
func (s *Service) ResolveShare(ctx context.Context, token string) (sqlc.Artifact, pgtype.UUID, error) {
	var (
		zero   sqlc.Artifact
		noLink pgtype.UUID
	)
	link, err := s.store.GetShareLinkByHash(ctx, HashShareToken(token))
	if err != nil {
		return zero, noLink, pgstore.ErrNotFound // unknown token
	}
	if link.RevokedAt.Valid {
		return zero, noLink, pgstore.ErrNotFound // revoked
	}
	if !link.ExpiresAt.Time.After(time.Now()) {
		return zero, noLink, pgstore.ErrNotFound // expired
	}

	// For a tracking link this deliberately does NOT use GetBySlug(slug, nil):
	// that filters deleted_at and walks back to an older version, so archiving
	// the latest version would leave the link quietly serving stale content
	// instead of dying. Take the newest row whatever its state and refuse it
	// below.
	row, err := s.shareTarget(ctx, link)
	if err != nil {
		return zero, noLink, pgstore.ErrNotFound // target gone
	}
	if row.DeletedAt.Valid {
		return zero, noLink, pgstore.ErrNotFound // archived
	}
	// Mint refuses APP, so this looks redundant. It is not. A tracking link
	// resolves the LATEST version, and a slug's type is not pinned across
	// versions — Create takes the type from the request and never compares it
	// to the previous version. Publishing a version needs write access, not
	// ownership, so without this check any delegated writer could turn an
	// owner's live tracking link into anonymous APP execution.
	if row.ArtifactType == pgstore.TypeApp {
		return zero, noLink, pgstore.ErrNotFound
	}
	// A slug-scoped link must still be pointing at the lineage it was minted
	// against. An all-archived slug is free for anyone to reuse, so without
	// this the link would follow the NAME to whatever document now occupies
	// it — serving a stranger's private document to the internet.
	if link.Slug != nil {
		// No anchor means the row predates 0025. Refuse rather than skip: an
		// unanchored slug link is exactly the one that can follow a reclaimed
		// slug to whoever occupies it next, which is the bypass the anchor
		// exists to close. Failing closed retires those links; they are
		// re-mintable. Pinned links never reach here — they carry a NULL slug.
		if link.AnchorOwner == "" {
			return zero, noLink, pgstore.ErrNotFound
		}
		owner, oerr := s.store.SlugLiveCreator(ctx, *link.Slug)
		if oerr != nil || !strings.EqualFold(owner, link.AnchorOwner) {
			return zero, noLink, pgstore.ErrNotFound
		}
	}
	return row, link.ID, nil
}

// RecordShareOpen appends one open. Best-effort by contract: the caller
// ignores the error, because a document that cannot be read on account of a
// failed audit write is a worse outcome than a missing audit row.
func (s *Service) RecordShareOpen(ctx context.Context, linkID pgtype.UUID, ip, peerAddr, ua string) error {
	return s.store.RecordShareLinkOpen(ctx, linkID, ip, peerAddr, ua)
}

func shareLinkDTO(r sqlc.ShareLink) ShareLinkDTO {
	d := ShareLinkDTO{
		ID:          pgstore.UUIDFromPG(r.ID).String(),
		TokenPrefix: r.TokenPrefix,
		Scope:       "version",
		Note:        r.Note,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt.Time,
		ExpiresAt:   r.ExpiresAt.Time,
		OpenCount:   r.OpenCount,
	}
	if r.Slug != nil {
		d.Scope = "slug"
	}
	if r.RevokedAt.Valid {
		t := r.RevokedAt.Time
		d.RevokedAt = &t
	}
	if r.LastOpenedAt.Valid {
		t := r.LastOpenedAt.Time
		d.LastOpenedAt = &t
	}
	return d
}

func pgUUIDOf(id uuid.UUID) pgtype.UUID {
	var p pgtype.UUID
	copy(p.Bytes[:], id[:])
	p.Valid = true
	return p
}
