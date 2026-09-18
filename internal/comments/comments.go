// Package comments implements per-artifact-version commenting: threads
// (doc-level, text-quote, or pin anchors) with open/resolved status, and
// comments hanging off them. It reuses the artifact's read-access rules —
// anyone who can read an artifact can read and add comments on it.
package comments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/mdtext"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/slacknotify"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

type Service struct {
	pool   *pgxpool.Pool
	q      *sqlc.Queries
	art    *pgstore.Store  // for artifact read-access checks
	signer *auth.JWTSigner // mints/verifies scoped embed tokens

	notifier *slacknotify.Notifier // nil → Slack notifications disabled
	baseURL  string                // for building artifact permalinks in notifications

	rateMu          sync.Mutex
	rateGlobalRPM   int
	rateArtifactRPM int
	rateGlobal      map[string][]time.Time
	rateArtifact    map[string][]time.Time
}

func NewService(pool *pgxpool.Pool, art *pgstore.Store, signer *auth.JWTSigner) *Service {
	return &Service{
		pool: pool, q: sqlc.New(pool), art: art, signer: signer,
		rateGlobalRPM: 30, rateArtifactRPM: 10,
		rateGlobal: map[string][]time.Time{}, rateArtifact: map[string][]time.Time{},
	}
}

func (s *Service) SetRateLimit(globalRPM, artifactRPM int) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	s.rateGlobalRPM = globalRPM
	s.rateArtifactRPM = artifactRPM
}

// SetNotifier enables Slack DM notifications for comment events. A nil
// notifier (e.g. no bot token configured) leaves notifications off — every
// event hook becomes a no-op. baseURL is used to build the artifact permalink
// included in each notification.
func (s *Service) SetNotifier(n *slacknotify.Notifier, baseURL string) {
	s.notifier = n
	s.baseURL = strings.TrimRight(baseURL, "/")
}

// ─── embed token: scoped to {email, one artifact, commenting} ────────
// Lets the overlay injected into a SANDBOXED served HTML page reach the
// comments API without the session cookie (which a sandboxed page can't
// send). The token is short-lived and authorizes commenting on exactly
// one artifact, nothing else — so even a malicious uploaded page can at
// most comment on its own artifact.

const embedScopePrefix = auth.EmbedScopePrefix
const embedTokenTTL = 12 * time.Hour

type embedArtifactKey struct{}

// SignEmbedToken mints a token for `email` scoped to commenting on `artifactID`.
// Name and picture are carried so the embed-auth middleware can propagate them
// into the request context for comment creation.
func (s *Service) SignEmbedToken(email, artifactID, name, picture string) (string, error) {
	return s.signer.Sign(auth.Claims{Email: email, Name: name, Picture: picture, Scopes: []string{embedScopePrefix + artifactID}, TTL: embedTokenTTL})
}

func embedArtifactOf(c auth.Claims) string {
	for _, sc := range c.Scopes {
		if strings.HasPrefix(sc, embedScopePrefix) {
			return strings.TrimPrefix(sc, embedScopePrefix)
		}
	}
	return ""
}

// ─── DTOs ────────────────────────────────────────────────────────────

type Comment struct {
	ID            string     `json:"id"`
	Author        string     `json:"author"`
	AuthorName    string     `json:"author_name"`
	AuthorPicture string     `json:"author_picture,omitempty"`
	Body          string     `json:"body"`
	Source        string     `json:"source,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	EditedAt      *time.Time `json:"edited_at,omitempty"`
}

type Thread struct {
	ID         string          `json:"id"`
	Anchor     json.RawMessage `json:"anchor"`
	Status     string          `json:"status"`
	CreatedBy  string          `json:"created_by"`
	CreatedAt  time.Time       `json:"created_at"`
	ResolvedBy *string         `json:"resolved_by,omitempty"`
	Comments   []Comment       `json:"comments"`
}

type ListResponse struct {
	ArtifactID string   `json:"artifact_id,omitempty"`
	Slug       string   `json:"slug,omitempty"`
	Version    *int32   `json:"version,omitempty"`
	Threads    []Thread `json:"threads"`
}

type WriteResponse struct {
	ArtifactID          string   `json:"artifact_id"`
	Slug                string   `json:"slug,omitempty"`
	Version             *int32   `json:"version,omitempty"`
	Thread              Thread   `json:"thread"`
	MentionsNotified    []string `json:"mentions_notified"`
	MentionsUnreachable []string `json:"mentions_unreachable"`
}

type AddInput struct {
	Anchor json.RawMessage `json:"anchor"`
	Quote  string          `json:"quote"`
	Body   string          `json:"body"`
	Source string          `json:"source"`
}

type ReplyInput struct {
	Body   string `json:"body"`
	Source string `json:"source"`
}

type createReq struct {
	Anchor json.RawMessage `json:"anchor"`
	Quote  string          `json:"quote"`
	Body   string          `json:"body"`
}

type replyReq struct {
	Body string `json:"body"`
}

type editReq struct {
	Body string `json:"body"`
}

// ─── routes ──────────────────────────────────────────────────────────

// Mount attaches comment routes. Auth middleware is applied at a higher level.
func (s *Service) Mount(r chi.Router) {
	r.Get("/api/artifacts/{id}/comments", s.list)
	r.Get("/api/artifacts/by-slug/{slug}/comments", s.listBySlug)
	r.Get("/api/artifacts/{id}/comments/people", s.mentionPeople)
	r.Post("/api/artifacts/{id}/comments", s.create)
	r.Post("/api/artifacts/by-slug/{slug}/comments", s.createBySlug)
	r.Get("/api/comments/{threadID}", s.getThread)
	r.Post("/api/comments/{threadID}/replies", s.reply)
	r.Post("/api/comments/{threadID}/resolve", s.resolve)
	r.Post("/api/comments/{threadID}/reopen", s.reopen)
	r.Put("/api/comments/{threadID}/comments/{commentID}", s.editComment)
	r.Delete("/api/comments/{threadID}/comments/{commentID}", s.deleteComment)
}

// MountEmbed registers the same comment endpoints under /api/embed/*, but
// the embed-token artifact pin stays in gateArtifact/gateThread. Do not wire
// embed routes directly to service methods, or the token widens past one artifact.
// Authed by a scoped embed token (Bearer) instead of the session cookie,
// with CORS so the sandboxed served-HTML overlay can call them. Mount this
// OUTSIDE the cookie-auth group.
func (s *Service) MountEmbed(r chi.Router) {
	r.Options("/api/embed/*", func(w http.ResponseWriter, _ *http.Request) { setCORS(w); w.WriteHeader(http.StatusNoContent) })
	r.Group(func(g chi.Router) {
		g.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { setCORS(w); next.ServeHTTP(w, req) })
		})
		g.Use(s.embedAuth)
		g.Get("/api/embed/artifacts/{id}/comments", s.list)
		g.Get("/api/embed/artifacts/{id}/comments/people", s.mentionPeople)
		g.Post("/api/embed/artifacts/{id}/comments", s.create)
		g.Post("/api/embed/comments/{threadID}/replies", s.reply)
		g.Post("/api/embed/comments/{threadID}/resolve", s.resolve)
		g.Post("/api/embed/comments/{threadID}/reopen", s.reopen)
		g.Put("/api/embed/comments/{threadID}/comments/{commentID}", s.editComment)
		g.Delete("/api/embed/comments/{threadID}/comments/{commentID}", s.deleteComment)
	})
}

func setCORS(w http.ResponseWriter) {
	// Bearer-token auth (no cookie) → `*` is safe; the token is the credential.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")
}

// embedAuth verifies the Bearer embed token, pins the identity + the one
// artifact it's scoped to into the context.
func (s *Service) embedAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		c, err := s.signer.Verify(tok)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid embed token")
			return
		}
		aid := embedArtifactOf(c)
		if aid == "" || c.Email == "" {
			writeErr(w, http.StatusUnauthorized, "embed token missing scope")
			return
		}
		ctx := auth.WithProfile(auth.WithIdentity(r.Context(), c.Email), c.Name, c.Picture)
		ctx = context.WithValue(ctx, embedArtifactKey{}, aid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ─── access ──────────────────────────────────────────────────────────

// canRead mirrors the artifact read rule: admins always; otherwise the
// caller must pass the artifact's access patterns (and archived versions
// are visible only to their creator). Returns the row too, so callers can
// consult the per-doc comment switch without a second fetch.
func (s *Service) canRead(ctx context.Context, artifactID uuid.UUID, caller string) (sqlc.Artifact, bool, error) {
	row, err := s.art.GetByID(ctx, artifactID)
	if err != nil {
		return sqlc.Artifact{}, false, err
	}
	ok, err := s.canReadRow(ctx, row, caller)
	if err != nil {
		return sqlc.Artifact{}, false, err
	}
	return row, ok, nil
}

// canReadRow is the read rule itself, for a row already in hand. It is split
// out because the mention paths ask it about somebody OTHER than the caller —
// "may this person be told what this comment says?" — and that question must be
// answered by the same rule that guards the read, not by a second one that can
// drift from it.
func (s *Service) canReadRow(ctx context.Context, row sqlc.Artifact, email string) (bool, error) {
	if ok, err := s.art.HasPermission(ctx, email, rbac.ManageArtifacts); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	if row.DeletedAt.Valid && !strings.EqualFold(row.Creator, email) {
		return false, nil
	}
	groups, err := s.art.CallerGroups(ctx, email)
	if err != nil {
		return false, err
	}
	return pgstore.CanAccess(row, email, groups), nil
}

// ─── handlers ────────────────────────────────────────────────────────

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, row, _, ok := s.gateArtifact(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	// Comments off → report none. The threads are still in the table (turning
	// the switch back on restores them), they are simply not part of the
	// document while it's closed — so nothing renders and no reply affordance
	// appears anywhere, including in already-loaded pages that re-poll.
	if !row.CommentsEnabled {
		writeJSON(w, http.StatusOK, ListResponse{Threads: []Thread{}})
		return
	}
	out, err := s.fetchThreads(r.Context(), id, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.withArtifact(row)
	writeJSON(w, http.StatusOK, out)
}

// fetchThreads loads an artifact's threads (with their comments). It does NOT
// enforce access — callers must gate first (gateArtifact / ListForCaller).
// When includeResolved is false, resolved threads are dropped.
func (s *Service) fetchThreads(ctx context.Context, artifactID uuid.UUID, includeResolved bool) (ListResponse, error) {
	threads, err := s.q.ListThreadsByArtifact(ctx, pgUUID(artifactID))
	if err != nil {
		return ListResponse{}, err
	}
	cmts, err := s.q.ListCommentsByArtifact(ctx, pgUUID(artifactID))
	if err != nil {
		return ListResponse{}, err
	}
	byThread := map[string][]Comment{}
	for _, c := range cmts {
		tid := uuidStr(c.ThreadID)
		byThread[tid] = append(byThread[tid], toComment(c))
	}
	out := ListResponse{Threads: make([]Thread, 0, len(threads))}
	for _, t := range threads {
		th := toThread(t, byThread[uuidStr(t.ThreadID)])
		if !includeResolved && th.Status == "resolved" {
			continue
		}
		out.Threads = append(out.Threads, th)
	}
	return out, nil
}

// ListForCaller returns an artifact's comment threads, enforcing the same read
// access as the artifact itself (admins always; otherwise the artifact's
// allowed_access, with archived versions visible only to their creator). When
// includeResolved is false, resolved threads are omitted. Returns
// pgstore.ErrNotFound when the caller can't read the artifact. Read-only — used
// by the MCP surface.
func (s *Service) ListForCaller(ctx context.Context, artifactID uuid.UUID, caller string, includeResolved bool) (ListResponse, error) {
	row, ok, err := s.canRead(ctx, artifactID, caller)
	if err != nil {
		return ListResponse{}, err
	}
	if !ok {
		return ListResponse{}, pgstore.ErrNotFound
	}
	// Same rule the HTTP list path applies: a document with commenting turned
	// off has no threads, so the MCP surface can't read around the switch.
	if !row.CommentsEnabled {
		return ListResponse{Threads: []Thread{}}, nil
	}
	return s.fetchThreads(ctx, artifactID, includeResolved)
}

func (s *Service) listBySlug(w http.ResponseWriter, r *http.Request) {
	row, caller, ok := s.gateSlug(w, r)
	if !ok {
		return
	}
	if !row.CommentsEnabled {
		out := ListResponse{Threads: []Thread{}}
		out.withArtifact(row)
		writeJSON(w, http.StatusOK, out)
		return
	}
	out, err := s.fetchThreads(r.Context(), uuidFrom(row.ArtifactID), r.URL.Query().Get("exclude_resolved") != "true")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = caller
	out.withArtifact(row)
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) getThread(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(chi.URLParam(r, "threadID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad thread id")
		return
	}
	th, err := s.GetThreadForCaller(r.Context(), tid, auth.EmailFromContext(r.Context()))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, th)
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, _, caller, ok := s.gateArtifact(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	out, err := s.AddForCaller(r.Context(), id, caller, AddInput{Anchor: req.Anchor, Quote: req.Quote, Body: req.Body, Source: sourceFromRequest(r)})
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) createBySlug(w http.ResponseWriter, r *http.Request) {
	row, caller, ok := s.gateSlug(w, r)
	if !ok {
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	out, err := s.AddForCaller(r.Context(), uuidFrom(row.ArtifactID), caller, AddInput{Anchor: req.Anchor, Quote: req.Quote, Body: req.Body, Source: sourceFromRequest(r)})
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) reply(w http.ResponseWriter, r *http.Request) {
	t, caller, ok := s.gateThread(w, r)
	if !ok {
		return
	}
	var req replyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body required")
		return
	}
	c, err := s.ReplyForCaller(r.Context(), uuidFrom(t.ThreadID), caller, ReplyInput{Body: req.Body, Source: sourceFromRequest(r)})
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Service) resolve(w http.ResponseWriter, r *http.Request) { s.setStatus(w, r, "resolved") }
func (s *Service) reopen(w http.ResponseWriter, r *http.Request)  { s.setStatus(w, r, "open") }

func (s *Service) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	t, caller, ok := s.gateThread(w, r)
	if !ok {
		return
	}
	th, err := s.SetStatusForCaller(r.Context(), uuidFrom(t.ThreadID), caller, status, sourceFromRequest(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	if status == "open" {
		writeJSON(w, http.StatusOK, th)
		return
	}
	writeJSON(w, http.StatusOK, th)
}

// ─── transport-neutral write surface ─────────────────────────────────

var (
	ErrEmptyBody        = errors.New("body required")
	ErrQuoteNotFound    = errors.New("quote not found in rendered artifact text")
	ErrCommentsDisabled = errors.New("commenting is turned off for this document")
	ErrQuoteUnsupported = errors.New("quote anchoring is not supported for a package artifact; omit quote to comment on the document")
	ErrQuoteIsSource    = errors.New("quote carries markdown markup; send the text as the page renders it")
	ErrRateLimited      = errors.New("comment write rate limit exceeded")
)

func (s *Service) AddForCaller(ctx context.Context, artifactID uuid.UUID, caller string, in AddInput) (WriteResponse, error) {
	if strings.TrimSpace(in.Body) == "" {
		return WriteResponse{}, ErrEmptyBody
	}
	row, ok, err := s.canRead(ctx, artifactID, caller)
	if err != nil {
		return WriteResponse{}, err
	}
	if !ok {
		return WriteResponse{}, pgstore.ErrNotFound
	}
	if !row.CommentsEnabled {
		return WriteResponse{}, ErrCommentsDisabled
	}
	if !s.allowWrite(caller, artifactID) {
		return WriteResponse{}, ErrRateLimited
	}
	anchor := in.Anchor
	if len(anchor) == 0 && strings.TrimSpace(in.Quote) != "" {
		if err := s.validateQuote(ctx, row, in.Quote); err != nil {
			return WriteResponse{}, err
		}
		b, _ := json.Marshal(map[string]string{"type": "text", "quote": strings.TrimSpace(in.Quote)})
		anchor = b
	}
	if len(anchor) == 0 {
		anchor = json.RawMessage(`{"type":"doc"}`)
	}
	if in.Source == "" {
		in.Source = "api"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteResponse{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	var thread sqlc.CommentThread
	var c sqlc.Comment
	if isDocAnchor(anchor) {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1)::bigint)", artifactID.String()+":doc-thread"); err != nil {
			return WriteResponse{}, err
		}
		threads, err := qtx.ListThreadsByArtifact(ctx, pgUUID(artifactID))
		if err != nil {
			return WriteResponse{}, err
		}
		for _, t := range threads {
			if isDocAnchor(t.Anchor) {
				thread = t
				break
			}
		}
	}
	if !thread.ThreadID.Valid {
		thread, err = qtx.CreateThread(ctx, sqlc.CreateThreadParams{
			ThreadID: pgUUID(uuid.New()), ArtifactID: pgUUID(artifactID), Anchor: anchor, CreatedBy: caller,
		})
		if err != nil {
			return WriteResponse{}, err
		}
	}
	c, err = qtx.AddComment(ctx, sqlc.AddCommentParams{
		CommentID: pgUUID(uuid.New()), ThreadID: thread.ThreadID, Author: caller, Body: in.Body,
		AuthorName: strPtr(auth.NameFromContext(ctx)), AuthorPicture: strPtr(auth.PictureFromContext(ctx)), Source: in.Source,
	})
	if err != nil {
		return WriteResponse{}, err
	}
	mentioned, unreachable, err := s.splitMentions(ctx, row, caller, in.Body)
	if err != nil {
		return WriteResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WriteResponse{}, err
	}
	s.fireNotify(slacknotify.ActionNewComment, artifactID, uuidFrom(thread.ThreadID), uuidFrom(c.CommentID), caller, auth.NameFromContext(ctx), in.Body)
	return writeResponse(row, toThread(thread, []Comment{toComment(c)}), mentioned, unreachable), nil
}

func (s *Service) ReplyForCaller(ctx context.Context, threadID uuid.UUID, caller string, in ReplyInput) (Comment, error) {
	if strings.TrimSpace(in.Body) == "" {
		return Comment{}, ErrEmptyBody
	}
	t, row, err := s.threadForCaller(ctx, threadID, caller)
	if err != nil {
		return Comment{}, err
	}
	if !row.CommentsEnabled {
		return Comment{}, ErrCommentsDisabled
	}
	if !s.allowWrite(caller, uuidFrom(t.ArtifactID)) {
		return Comment{}, ErrRateLimited
	}
	if in.Source == "" {
		in.Source = "api"
	}
	c, err := s.q.AddComment(ctx, sqlc.AddCommentParams{
		CommentID: pgUUID(uuid.New()), ThreadID: t.ThreadID, Author: caller, Body: in.Body,
		AuthorName: strPtr(auth.NameFromContext(ctx)), AuthorPicture: strPtr(auth.PictureFromContext(ctx)), Source: in.Source,
	})
	if err != nil {
		return Comment{}, err
	}
	s.fireNotify(slacknotify.ActionReply, uuidFrom(t.ArtifactID), uuidFrom(t.ThreadID), uuidFrom(c.CommentID), caller, auth.NameFromContext(ctx), in.Body)
	return toComment(c), nil
}

func (s *Service) SetStatusForCaller(ctx context.Context, threadID uuid.UUID, caller string, status, source string) (Thread, error) {
	t, row, err := s.threadForCaller(ctx, threadID, caller)
	if err != nil {
		return Thread{}, err
	}
	if !row.CommentsEnabled {
		return Thread{}, ErrCommentsDisabled
	}
	if t.Status == status {
		cmts, err := s.q.ListCommentsByThread(ctx, t.ThreadID)
		if err != nil {
			return Thread{}, err
		}
		return toThread(t, commentsToDTO(cmts)), nil
	}
	if !s.allowWrite(caller, uuidFrom(t.ArtifactID)) {
		return Thread{}, ErrRateLimited
	}
	var by *string
	var at pgtype.Timestamptz
	if status == "resolved" {
		by = &caller
		at = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	if _, err := s.q.SetThreadStatus(ctx, sqlc.SetThreadStatusParams{
		ThreadID: t.ThreadID, Status: status, ResolvedBy: by, ResolvedAt: at,
	}); err != nil {
		return Thread{}, err
	}
	t.Status = status
	t.ResolvedBy = by
	t.ResolvedAt = at
	action := slacknotify.ActionResolve
	if status == "open" {
		action = slacknotify.ActionReopen
	}
	s.fireNotify(action, uuidFrom(t.ArtifactID), uuidFrom(t.ThreadID), uuid.Nil, caller, auth.NameFromContext(ctx), "")
	cmts, err := s.q.ListCommentsByThread(ctx, t.ThreadID)
	if err != nil {
		return Thread{}, err
	}
	return toThread(t, commentsToDTO(cmts)), nil
}

func (s *Service) GetThreadForCaller(ctx context.Context, threadID uuid.UUID, caller string) (Thread, error) {
	t, row, err := s.threadForCaller(ctx, threadID, caller)
	if err != nil {
		return Thread{}, err
	}
	// A document with commenting off has no threads, which is what the list
	// paths report. Answering here with a shell would tell a caller holding the
	// id that the thread is still there.
	if !row.CommentsEnabled {
		return Thread{}, pgstore.ErrNotFound
	}
	cmts, err := s.q.ListCommentsByThread(ctx, t.ThreadID)
	if err != nil {
		return Thread{}, err
	}
	return toThread(t, commentsToDTO(cmts)), nil
}

func (s *Service) threadForCaller(ctx context.Context, threadID uuid.UUID, caller string) (sqlc.CommentThread, sqlc.Artifact, error) {
	t, err := s.q.GetThread(ctx, pgUUID(threadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.CommentThread{}, sqlc.Artifact{}, pgstore.ErrNotFound
	}
	if err != nil {
		return sqlc.CommentThread{}, sqlc.Artifact{}, err
	}
	row, ok, err := s.canRead(ctx, uuidFrom(t.ArtifactID), caller)
	if err != nil || !ok {
		return sqlc.CommentThread{}, sqlc.Artifact{}, pgstore.ErrNotFound
	}
	return t, row, nil
}

func (s *Service) allowWrite(caller string, artifactID uuid.UUID) bool {
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rateGlobal == nil {
		s.rateGlobal = map[string][]time.Time{}
	}
	if s.rateArtifact == nil {
		s.rateArtifact = map[string][]time.Time{}
	}
	globalKey := strings.ToLower(caller)
	artifactKey := globalKey + ":" + artifactID.String()
	if !allowRateKey(s.rateGlobal, globalKey, s.rateGlobalRPM, cutoff, now) {
		return false
	}
	if !allowRateKey(s.rateArtifact, artifactKey, s.rateArtifactRPM, cutoff, now) {
		if s.rateGlobalRPM > 0 {
			s.rateGlobal[globalKey] = s.rateGlobal[globalKey][:len(s.rateGlobal[globalKey])-1]
		}
		return false
	}
	return true
}

func allowRateKey(buckets map[string][]time.Time, key string, limit int, cutoff, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	kept := buckets[key][:0]
	for _, t := range buckets[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		buckets[key] = kept
		return false
	}
	buckets[key] = append(kept, now)
	return true
}

func (s *Service) validateQuote(ctx context.Context, row sqlc.Artifact, quote string) error {
	q := normalizePlainText(quote)
	if q == "" {
		return ErrQuoteNotFound
	}
	// The stored bytes of a PACKAGE or APP are the zip, not the served page,
	// so a text search over them can only ever say "not found".
	if pgstore.IsPackageLike(row.ArtifactType) {
		return ErrQuoteUnsupported
	}
	// A MAP version is NDJSON run through the code renderer, so the page text
	// and the stored bytes are not the same string and a quote match here
	// would be luck. Say unsupported rather than "not found", which would
	// send the commenter looking for a typo that isn't there.
	if row.ArtifactType == pgstore.TypeMap {
		return ErrQuoteUnsupported
	}
	rc, err := s.art.Content(ctx, row)
	if err != nil {
		return err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return err
	}
	body := mdtext.PageText(string(b))
	if strings.Contains(body, q) {
		return nil
	}
	// The quote does name text in the document, but only in its source form:
	// the markers are not on the page, so the viewer could never seat a
	// highlight for it. Say which of the two failures this is.
	if strings.Contains(body, mdtext.PageText(quote)) {
		return ErrQuoteIsSource
	}
	return ErrQuoteNotFound
}

func normalizePlainText(s string) string { return mdtext.Normalize(s) }

func commentsToDTO(cmts []sqlc.Comment) []Comment {
	out := make([]Comment, 0, len(cmts))
	for _, c := range cmts {
		out = append(out, toComment(c))
	}
	return out
}

func writeResponse(row sqlc.Artifact, thread Thread, mentioned, unreachable []string) WriteResponse {
	out := WriteResponse{
		ArtifactID: uuidStr(row.ArtifactID), Thread: thread,
		MentionsNotified: mentioned, MentionsUnreachable: unreachable,
	}
	if row.NamedSlug != nil {
		out.Slug = *row.NamedSlug
	}
	out.Version = row.Version
	return out
}

func (lr *ListResponse) withArtifact(row sqlc.Artifact) {
	lr.ArtifactID = uuidStr(row.ArtifactID)
	if row.NamedSlug != nil {
		lr.Slug = *row.NamedSlug
	}
	lr.Version = row.Version
}

func sourceFromRequest(r *http.Request) string {
	if _, ok := r.Context().Value(embedArtifactKey{}).(string); ok {
		return "embed"
	}
	return "web"
}

// ─── mention typeahead ───────────────────────────────────────────────

// mentionPeopleLimit is how many suggestions the composer's @-menu shows.
const mentionPeopleLimit = 10

type mentionPerson struct {
	Email string `json:"email"`
	// CanRead is why this endpoint exists rather than the composer reusing
	// GET /api/people: a suggestion that cannot read the document is a mention
	// that will never be delivered, and the menu has to be able to say so.
	CanRead bool `json:"can_read"`
}

type mentionPeopleResponse struct {
	People []mentionPerson `json:"people"`
	// CanGrant reports whether THIS caller, on THIS surface, may widen the
	// document's access from the composer. It is computed server-side and is
	// the only thing the client consults — the client never re-derives "am I
	// the owner" from anything it happens to know.
	CanGrant bool `json:"can_grant"`
	MinQuery int  `json:"min_query"`
}

// mentionPeople answers the composer's @-menu: known addresses matching a
// partial query, each flagged with whether it can read this document.
//
// Who sees what is decided here, not in the browser:
//
//   - A caller who cannot widen access is offered ONLY addresses that can
//     already read the doc. Nothing is hidden from them that GET /api/people
//     would not also return — the point is not to offer a mention that would
//     silently go nowhere.
//   - A caller who CAN widen access (the doc owner, or an admin) also sees the
//     ones who cannot read it, flagged, so the composer can offer to grant.
//   - On the embed surface CanGrant is always false. The embed token lives in
//     `window.__ARTI_COMMENTS__` inside a SANDBOXED page of author-supplied
//     HTML; that page can read it and call this API itself. Commenting on its
//     own artifact is all that token has ever authorized, and an ACL change is
//     not something a served page should be able to make on its viewer's
//     behalf. Granting stays on the cookie-authed app surface, next to the
//     access editor that does the same thing.
func (s *Service) mentionPeople(w http.ResponseWriter, r *http.Request) {
	_, row, caller, ok := s.gateArtifact(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx := r.Context()
	out := mentionPeopleResponse{People: []mentionPerson{}, MinQuery: pgstore.MinPeopleQuery}
	// Commenting closed → there is no composer to feed, and no reason to answer
	// a directory query through a document that isn't taking comments.
	if !row.CommentsEnabled {
		writeJSON(w, http.StatusOK, out)
		return
	}

	_, embed := ctx.Value(embedArtifactKey{}).(string)
	if !embed {
		canGrant, err := s.art.IsDocOwner(ctx, row, caller)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out.CanGrant = canGrant
	}

	// Over-fetch: when the caller can't grant, non-readers are dropped below,
	// and asking for exactly the display count would return a short menu on a
	// restricted doc.
	people, err := s.art.SearchKnownPeople(ctx, r.URL.Query().Get("q"), mentionPeopleLimit*2)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A doc granted to `*` is readable by everyone, so the per-address check is
	// skipped — that is the default for new artifacts, i.e. nearly every doc.
	// An archived version is the exception: it narrows to its creator whatever
	// the access list says, so it still goes the long way round.
	openToAll := !row.DeletedAt.Valid && slices.Contains(row.AllowedAccess, "*")
	for _, p := range people {
		if len(out.People) == mentionPeopleLimit {
			break
		}
		if strings.EqualFold(p.Email, caller) {
			continue // mentioning yourself notifies nobody
		}
		canRead := openToAll
		if !openToAll {
			if canRead, err = s.canReadRow(ctx, row, p.Email); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if !canRead && !out.CanGrant {
			continue
		}
		out.People = append(out.People, mentionPerson{Email: p.Email, CanRead: canRead})
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── slack notifications ─────────────────────────────────────────────

// fireNotify dispatches a Slack notification for a comment event AFTER the
// write has committed. It is fire-and-forget: all the work (DB reads to
// resolve owner/participants/anchor, Slack lookups + DMs) happens in a
// recover-guarded goroutine with its own context, so neither latency nor a
// Slack failure ever touches the request. A nil notifier is a no-op.
//
// linkComment is the comment the permalink should target; pass uuid.Nil for
// resolve/reopen (no new comment) and the thread's originating comment is used.
func (s *Service) fireNotify(action slacknotify.Action, artifactID, threadID, linkComment uuid.UUID, actor, actorName, body string) {
	if s.notifier == nil {
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("comments: notify goroutine panicked", "recover", rec)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ev, err := s.buildEvent(ctx, action, artifactID, threadID, linkComment, actor, actorName, body)
		if err != nil {
			slog.Warn("comments: building notify event failed", "err", err)
			return
		}
		s.notifier.Notify(ctx, ev)
	}()
}

// buildEvent gathers everything the notifier needs: the artifact (owner,
// title, permalink), the thread's anchor + participants, and the pin number.
func (s *Service) buildEvent(ctx context.Context, action slacknotify.Action, artifactID, threadID, linkComment uuid.UUID, actor, actorName, body string) (slacknotify.Event, error) {
	art, err := s.art.GetByID(ctx, artifactID)
	if err != nil {
		return slacknotify.Event{}, err
	}
	thread, err := s.q.GetThread(ctx, pgUUID(threadID))
	if err != nil {
		return slacknotify.Event{}, err
	}
	cmts, err := s.q.ListCommentsByThread(ctx, pgUUID(threadID))
	if err != nil {
		return slacknotify.Event{}, err
	}

	// Participants = distinct comment authors (case-insensitive), keeping the
	// original-cased email of the first occurrence.
	seen := map[string]bool{}
	var participants []string
	for _, c := range cmts {
		k := strings.ToLower(c.Author)
		if !seen[k] {
			seen[k] = true
			participants = append(participants, c.Author)
		}
	}

	if linkComment == uuid.Nil && len(cmts) > 0 {
		linkComment = uuidFrom(cmts[0].CommentID) // originating comment
	}
	url := s.artifactURL(art)
	if linkComment != uuid.Nil {
		url += "#comment-" + linkComment.String()
	}

	// Mentions are read off the body that was just written, and split by the
	// artifact's own read rule: a mention of somebody who cannot open the
	// document is not notified (the DM quotes the document), only reported to
	// the owner so the request reaches the one person who can act on it.
	mentioned, unreachable, err := s.splitMentions(ctx, art, actor, body)
	if err != nil {
		return slacknotify.Event{}, err
	}

	kind, quote := anchorKindQuote(thread.Anchor)
	pin := 0
	if kind == "pin" {
		pin = s.pinNumber(ctx, artifactID, threadID)
	}
	if actorName == "" {
		actorName = nameFromEmail(actor)
	}

	return slacknotify.Event{
		Action:        action,
		ArtifactURL:   url,
		ArtifactTitle: art.Title,
		ActorName:     actorName,
		Actor:         actor,
		Owner:         s.docOwner(ctx, art),
		Participants:  participants,
		Mentions:      mentioned,
		Unreachable:   unreachable,
		AnchorKind:    kind,
		AnchorQuote:   quote,
		PinNumber:     pin,
		Body:          body,
	}, nil
}

// docOwner returns the address that OWNS the document, falling back to this
// row's creator when the lookup fails. Not row.Creator, which versioning
// reassigns to whoever pushed the latest version — a delegated writer
// publishing v2 should not inherit the owner's notifications, and the
// "somebody was mentioned who can't read this" line is only actionable in the
// inbox of the person who can widen access.
func (s *Service) docOwner(ctx context.Context, art sqlc.Artifact) string {
	if owner, err := s.art.DocOwner(ctx, art); err == nil && owner != "" {
		return owner
	}
	return art.Creator
}

// artifactURL builds the canonical viewer URL for an artifact row, mirroring
// artifacts.ToInfo: a named slug (with version) when present, else the UUID.
func (s *Service) artifactURL(a sqlc.Artifact) string {
	if a.NamedSlug != nil {
		url := s.baseURL + "/s/" + *a.NamedSlug
		if a.Version != nil {
			url += "/" + strconv.Itoa(int(*a.Version))
		}
		return url
	}
	return s.baseURL + "/a/" + uuidFrom(a.ArtifactID).String()
}

// pinNumber replicates the frontend's stable pin numbering
// (web/lib/commentsOverlay.ts): among OPEN pin threads ordered by created_at,
// the target thread's 1-based position. Returns 0 when the thread isn't an
// open pin (e.g. it was just resolved — resolved pins aren't numbered).
func (s *Service) pinNumber(ctx context.Context, artifactID, threadID uuid.UUID) int {
	threads, err := s.q.ListThreadsByArtifact(ctx, pgUUID(artifactID)) // already ORDER BY created_at
	if err != nil {
		return 0
	}
	n := 0
	for _, t := range threads {
		if t.Status == "resolved" {
			continue
		}
		if kind, _ := anchorKindQuote(t.Anchor); kind != "pin" {
			continue
		}
		n++
		if uuidFrom(t.ThreadID) == threadID {
			return n
		}
	}
	return 0
}

// anchorKindQuote classifies an anchor and extracts the text-anchor quote.
func anchorKindQuote(a json.RawMessage) (kind, quote string) {
	var v struct {
		Type  string `json:"type"`
		Quote string `json:"quote"`
	}
	_ = json.Unmarshal(a, &v)
	switch v.Type {
	case "text":
		return "text", v.Quote
	case "pin":
		return "pin", ""
	default:
		return "doc", ""
	}
}

// editComment updates the body of a comment. You can edit ONLY your own.
func (s *Service) editComment(w http.ResponseWriter, r *http.Request) {
	t, caller, ok := s.gateThread(w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(chi.URLParam(r, "commentID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad comment id")
		return
	}
	c, err := s.q.GetComment(r.Context(), pgUUID(cid))
	if err != nil || uuidFrom(c.ThreadID) != uuidFrom(t.ThreadID) || c.DeletedAt.Valid {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !strings.EqualFold(c.Author, caller) {
		writeErr(w, http.StatusForbidden, "you can only edit your own comments")
		return
	}
	var req editReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, "body required")
		return
	}
	updated, err := s.q.UpdateComment(r.Context(), sqlc.UpdateCommentParams{
		CommentID: pgUUID(cid), Body: req.Body,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toComment(updated))
}

// deleteComment soft-deletes one comment. You can delete ONLY your own
// comment — never anyone else's. If it was the last live comment in its
// thread, the now-empty thread is removed too.
func (s *Service) deleteComment(w http.ResponseWriter, r *http.Request) {
	t, caller, ok := s.gateThread(w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(chi.URLParam(r, "commentID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad comment id")
		return
	}
	c, err := s.q.GetComment(r.Context(), pgUUID(cid))
	if err != nil || uuidFrom(c.ThreadID) != uuidFrom(t.ThreadID) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !strings.EqualFold(c.Author, caller) {
		writeErr(w, http.StatusForbidden, "you can only delete your own comments")
		return
	}
	if _, err := s.q.DeleteComment(r.Context(), pgUUID(cid)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, err := s.q.CountLiveComments(r.Context(), t.ThreadID); err == nil && n == 0 {
		_, _ = s.q.DeleteThread(r.Context(), t.ThreadID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── gating helpers ──────────────────────────────────────────────────

// gateArtifact parses the artifact id, resolves the caller, and enforces read
// access, returning the artifact row so the handler can also consult the
// per-doc comment switch. Returns ok=false (and writes the response) on any
// failure.

func (s *Service) gateSlug(w http.ResponseWriter, r *http.Request) (sqlc.Artifact, string, bool) {
	slug := chi.URLParam(r, "slug")
	caller := auth.EmailFromContext(r.Context())
	var ver *int32
	if raw := r.URL.Query().Get("version"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad version")
			return sqlc.Artifact{}, "", false
		}
		vv := int32(v)
		ver = &vv
	}
	admin, err := s.art.HasPermission(r.Context(), caller, rbac.ManageArtifacts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return sqlc.Artifact{}, "", false
	}
	var row sqlc.Artifact
	if ver == nil && !admin {
		row, err = s.art.GetLatestBySlugForCaller(r.Context(), slug, caller)
	} else {
		row, err = s.art.GetBySlug(r.Context(), slug, ver)
		if err == nil {
			var allowed bool
			allowed, err = s.canReadRow(r.Context(), row, caller)
			if !allowed && err == nil {
				err = pgstore.ErrNotFound
			}
		}
	}
	if err != nil {
		if errors.Is(err, pgstore.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return sqlc.Artifact{}, "", false
	}
	return row, caller, true
}

func (s *Service) gateArtifact(w http.ResponseWriter, r *http.Request, idStr string) (uuid.UUID, sqlc.Artifact, string, bool) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad artifact id")
		return uuid.UUID{}, sqlc.Artifact{}, "", false
	}
	if ea, ok := r.Context().Value(embedArtifactKey{}).(string); ok && ea != id.String() {
		// embed token is scoped to a single artifact — refuse others
		writeErr(w, http.StatusNotFound, "not found")
		return uuid.UUID{}, sqlc.Artifact{}, "", false
	}
	caller := auth.EmailFromContext(r.Context())
	row, allowed, err := s.canRead(r.Context(), id, caller)
	if err != nil || !allowed {
		// 404 (not 403) so callers can't probe restricted artifacts.
		writeErr(w, http.StatusNotFound, "not found")
		return uuid.UUID{}, sqlc.Artifact{}, "", false
	}
	return id, row, caller, true
}

// commentsOff writes a 403 and reports true when the document's owner has
// turned commenting off. Every mutating handler runs this after its read gate,
// so a stale page (or a direct API call) can't write to a closed document.
// 403 — not 404 — because the artifact itself is readable; only commenting is
// closed, and saying so is what lets the client explain it.
func commentsOff(w http.ResponseWriter, row sqlc.Artifact) bool {
	if row.CommentsEnabled {
		return false
	}
	writeErr(w, http.StatusForbidden, "commenting is turned off for this document")
	return true
}

func (s *Service) gateThread(w http.ResponseWriter, r *http.Request) (sqlc.CommentThread, string, bool) {
	tid, err := uuid.Parse(chi.URLParam(r, "threadID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad thread id")
		return sqlc.CommentThread{}, "", false
	}
	t, err := s.q.GetThread(r.Context(), pgUUID(tid))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return sqlc.CommentThread{}, "", false
	}
	if ea, ok := r.Context().Value(embedArtifactKey{}).(string); ok && ea != uuidFrom(t.ArtifactID).String() {
		writeErr(w, http.StatusNotFound, "not found")
		return sqlc.CommentThread{}, "", false
	}
	caller := auth.EmailFromContext(r.Context())
	row, allowed, err := s.canRead(r.Context(), uuidFrom(t.ArtifactID), caller)
	if err != nil || !allowed {
		writeErr(w, http.StatusNotFound, "not found")
		return sqlc.CommentThread{}, "", false
	}
	// Every gateThread caller mutates (reply / resolve / reopen / edit /
	// delete), so the comment switch is enforced here rather than five times
	// over. Reads go through list() / ListForCaller, which report no threads
	// at all when the switch is off.
	if commentsOff(w, row) {
		return sqlc.CommentThread{}, "", false
	}
	return t, caller, true
}

// ─── mapping + small helpers ─────────────────────────────────────────

func isDocAnchor(a json.RawMessage) bool {
	var v struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(a, &v)
	return v.Type == "doc"
}

func toComment(c sqlc.Comment) Comment {
	name := deref(c.AuthorName)
	if name == "" {
		name = nameFromEmail(c.Author)
	}
	cmt := Comment{
		ID: uuidStr(c.CommentID), Author: c.Author, AuthorName: name,
		AuthorPicture: deref(c.AuthorPicture), Body: c.Body, CreatedAt: c.CreatedAt.Time,
	}
	if c.EditedAt.Valid {
		t := c.EditedAt.Time
		cmt.EditedAt = &t
	}
	cmt.Source = c.Source
	return cmt
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
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

func toThread(t sqlc.CommentThread, cmts []Comment) Thread {
	if cmts == nil {
		cmts = []Comment{}
	}
	return Thread{
		ID: uuidStr(t.ThreadID), Anchor: t.Anchor, Status: t.Status,
		CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt.Time, ResolvedBy: t.ResolvedBy, Comments: cmts,
	}
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	var p pgtype.UUID
	copy(p.Bytes[:], id[:])
	p.Valid = true
	return p
}

func uuidFrom(p pgtype.UUID) uuid.UUID {
	var u uuid.UUID
	copy(u[:], p.Bytes[:])
	return u
}

func uuidStr(p pgtype.UUID) string { return uuidFrom(p).String() }

// WriteStatus maps a comment write failure to the status the REST front door
// answers with. ok is false for anything it does not recognize, so a caller
// outside this package can fall back to its own mapping rather than reporting
// a refusal as a server error.
func WriteStatus(err error) (status int, msg string, ok bool) {
	switch {
	case errors.Is(err, pgstore.ErrNotFound):
		return http.StatusNotFound, "not found", true
	case errors.Is(err, ErrEmptyBody):
		return http.StatusBadRequest, "body required", true
	case errors.Is(err, ErrQuoteNotFound):
		return http.StatusBadRequest, ErrQuoteNotFound.Error(), true
	case errors.Is(err, ErrQuoteUnsupported):
		return http.StatusBadRequest, ErrQuoteUnsupported.Error(), true
	case errors.Is(err, ErrQuoteIsSource):
		return http.StatusBadRequest, ErrQuoteIsSource.Error(), true
	case errors.Is(err, ErrCommentsDisabled):
		return http.StatusForbidden, ErrCommentsDisabled.Error(), true
	case errors.Is(err, ErrRateLimited):
		return http.StatusTooManyRequests, ErrRateLimited.Error(), true
	}
	return http.StatusInternalServerError, err.Error(), false
}

func writeServiceErr(w http.ResponseWriter, err error) {
	status, msg, _ := WriteStatus(err)
	writeErr(w, status, msg)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"detail": msg, "code": http.StatusText(code)})
}
