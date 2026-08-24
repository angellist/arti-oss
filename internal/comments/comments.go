// Package comments implements per-artifact-version commenting: threads
// (doc-level, text-quote, or pin anchors) with open/resolved status, and
// comments hanging off them. It reuses the artifact's read-access rules —
// anyone who can read an artifact can read and add comments on it.
package comments

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
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
}

func NewService(pool *pgxpool.Pool, art *pgstore.Store, signer *auth.JWTSigner) *Service {
	return &Service{pool: pool, q: sqlc.New(pool), art: art, signer: signer}
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
	Threads []Thread `json:"threads"`
}

type createReq struct {
	Anchor json.RawMessage `json:"anchor"`
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
	r.Post("/api/artifacts/{id}/comments", s.create)
	r.Post("/api/comments/{threadID}/replies", s.reply)
	r.Post("/api/comments/{threadID}/resolve", s.resolve)
	r.Post("/api/comments/{threadID}/reopen", s.reopen)
	r.Put("/api/comments/{threadID}/comments/{commentID}", s.editComment)
	r.Delete("/api/comments/{threadID}/comments/{commentID}", s.deleteComment)
}

// MountEmbed registers the same comment endpoints under /api/embed/*, but
// authed by a scoped embed token (Bearer) instead of the session cookie,
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
	if ok, err := s.art.HasPermission(ctx, caller, rbac.ManageArtifacts); err != nil {
		return sqlc.Artifact{}, false, err
	} else if ok {
		return row, true, nil
	}
	if row.DeletedAt.Valid && !strings.EqualFold(row.Creator, caller) {
		return row, false, nil
	}
	groups, err := s.art.CallerGroups(ctx, caller)
	if err != nil {
		return sqlc.Artifact{}, false, err
	}
	return row, pgstore.CanAccess(row, caller, groups), nil
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

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, row, caller, ok := s.gateArtifact(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if commentsOff(w, row) {
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, "body required")
		return
	}
	anchor := req.Anchor
	if len(anchor) == 0 {
		anchor = json.RawMessage(`{"type":"doc"}`)
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	// Single doc-level thread per artifact. Serialize doc creates for this
	// artifact with a transaction-scoped advisory lock, then find-or-create
	// inside the same tx — so two concurrent doc comments fold into one thread
	// instead of racing to insert duplicates.
	if isDocAnchor(anchor) {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1)::bigint)", id.String()+":doc-thread"); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		threads, err := qtx.ListThreadsByArtifact(ctx, pgUUID(id))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, t := range threads {
			if !isDocAnchor(t.Anchor) {
				continue
			}
			c, err := qtx.AddComment(ctx, sqlc.AddCommentParams{
				CommentID: pgUUID(uuid.New()), ThreadID: t.ThreadID, Author: caller, Body: req.Body,
				AuthorName: strPtr(auth.NameFromContext(ctx)), AuthorPicture: strPtr(auth.PictureFromContext(ctx)),
			})
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := tx.Commit(ctx); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			s.fireNotify(slacknotify.ActionNewComment, id, uuidFrom(t.ThreadID), uuidFrom(c.CommentID), caller, auth.NameFromContext(ctx), req.Body)
			writeJSON(w, http.StatusCreated, toThread(t, []Comment{toComment(c)}))
			return
		}
	}

	thread, err := qtx.CreateThread(ctx, sqlc.CreateThreadParams{
		ThreadID: pgUUID(uuid.New()), ArtifactID: pgUUID(id), Anchor: anchor, CreatedBy: caller,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	c, err := qtx.AddComment(ctx, sqlc.AddCommentParams{
		CommentID: pgUUID(uuid.New()), ThreadID: thread.ThreadID, Author: caller, Body: req.Body,
		AuthorName: strPtr(auth.NameFromContext(ctx)), AuthorPicture: strPtr(auth.PictureFromContext(ctx)),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.fireNotify(slacknotify.ActionNewComment, id, uuidFrom(thread.ThreadID), uuidFrom(c.CommentID), caller, auth.NameFromContext(ctx), req.Body)
	writeJSON(w, http.StatusCreated, toThread(thread, []Comment{toComment(c)}))
}

func (s *Service) reply(w http.ResponseWriter, r *http.Request) {
	t, caller, ok := s.gateThread(w, r)
	if !ok {
		return
	}
	var req replyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, "body required")
		return
	}
	c, err := s.q.AddComment(r.Context(), sqlc.AddCommentParams{
		CommentID: pgUUID(uuid.New()), ThreadID: t.ThreadID, Author: caller, Body: req.Body,
		AuthorName: strPtr(auth.NameFromContext(r.Context())), AuthorPicture: strPtr(auth.PictureFromContext(r.Context())),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.fireNotify(slacknotify.ActionReply, uuidFrom(t.ArtifactID), uuidFrom(t.ThreadID), uuidFrom(c.CommentID), caller, auth.NameFromContext(r.Context()), req.Body)
	writeJSON(w, http.StatusCreated, toComment(c))
}

func (s *Service) resolve(w http.ResponseWriter, r *http.Request) { s.setStatus(w, r, "resolved") }
func (s *Service) reopen(w http.ResponseWriter, r *http.Request)  { s.setStatus(w, r, "open") }

func (s *Service) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	t, caller, ok := s.gateThread(w, r)
	if !ok {
		return
	}
	var by *string
	var at pgtype.Timestamptz
	if status == "resolved" {
		by = &caller
		at = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	if _, err := s.q.SetThreadStatus(r.Context(), sqlc.SetThreadStatusParams{
		ThreadID: t.ThreadID, Status: status, ResolvedBy: by, ResolvedAt: at,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	action := slacknotify.ActionResolve
	if status == "open" {
		action = slacknotify.ActionReopen
	}
	// uuid.Nil link target → buildEvent links the thread's originating comment.
	s.fireNotify(action, uuidFrom(t.ArtifactID), uuidFrom(t.ThreadID), uuid.Nil, caller, auth.NameFromContext(r.Context()), "")
	w.WriteHeader(http.StatusNoContent)
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
		Owner:         art.Creator,
		Participants:  participants,
		AnchorKind:    kind,
		AnchorQuote:   quote,
		PinNumber:     pin,
		Body:          body,
	}, nil
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"detail": msg, "code": http.StatusText(code)})
}

var _ = errors.New // reserved for future typed errors
