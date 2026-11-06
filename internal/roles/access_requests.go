package roles

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/slacknotify"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// AccessRequests is the auth.AccessRequester behind the login page: it stores
// the request and announces a new one in Slack.
type AccessRequests struct {
	Store *pgstore.Store
	// Post sends one Slack message; nil leaves requests unannounced.
	Post func(ctx context.Context, text string) error
	// Mention is a Slack user ID tagged in the announcement ("" for none).
	Mention string
	// ReviewURL is the admin page where a request is approved.
	ReviewURL string
}

func (a AccessRequests) AccessRequestStatus(ctx context.Context, email string) (string, error) {
	return a.Store.AccessRequestStatus(ctx, email)
}

// RequestAccess records the request and posts only when it is new, so a
// resubmit does not repeat the announcement. A failed post is logged; the
// request stands either way.
func (a AccessRequests) RequestAccess(ctx context.Context, email, name string) error {
	created, err := a.Store.CreateAccessRequest(ctx, email, name)
	if err != nil || !created || a.Post == nil {
		return err
	}
	text := accessRequestText(a.Mention, name, strings.ToLower(strings.TrimSpace(email)), a.ReviewURL)
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if perr := a.Post(postCtx, text); perr != nil {
		slog.Warn("access request: slack post failed", "email", email, "err", perr)
	}
	return nil
}

func accessRequestText(mention, name, email, reviewURL string) string {
	var b strings.Builder
	if mention != "" {
		b.WriteString("<@" + mention + "> ")
	}
	b.WriteString(slacknotify.Escape(strings.TrimSpace(name)) + " (" + slacknotify.Escape(email) + ") has requested an arti account.")
	if reviewURL != "" {
		b.WriteString(" <" + reviewURL + "|Review request>")
	}
	return b.String()
}

// ─── admin endpoints ─────────────────────────────────────────────────

type AccessRequestDTO struct {
	Email       string `json:"email"`
	Name        string `json:"name"`
	RequestedAt string `json:"requested_at"`
}

func (s *Service) listAccessRequests(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	reqs, err := s.store.ListPendingAccessRequests(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]AccessRequestDTO, 0, len(reqs))
	for _, q := range reqs {
		out = append(out, AccessRequestDTO{Email: q.Email, Name: q.Name, RequestedAt: q.RequestedAt.Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (s *Service) approveAccessRequest(w http.ResponseWriter, r *http.Request) {
	s.decideAccessRequest(w, r, true)
}

func (s *Service) declineAccessRequest(w http.ResponseWriter, r *http.Request) {
	s.decideAccessRequest(w, r, false)
}

func (s *Service) decideAccessRequest(w http.ResponseWriter, r *http.Request, approve bool) {
	if !s.requireManageRoles(w, r) {
		return
	}
	var body userEmailReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.store.DecideAccessRequest(r.Context(), body.Email, approve, auth.EmailFromContext(r.Context())); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
