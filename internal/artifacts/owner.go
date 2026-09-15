package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/slacknotify"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// owner.go — reading and transferring document ownership.
//
// Ownership is per-DOCUMENT and does not move on its own: it is claimed by the
// first version and stays put through every later version, archive and ACL
// change (pgstore/owner.go, migration 0029). This is the only way to move it.

// OwnerRequest is the body of POST /api/artifacts/by-slug/{slug}/owner.
type OwnerRequest struct {
	Owner string `json:"owner"`
	// KeepAccess grants the outgoing owner read and write on the way out.
	// Absent means true, because losing write on a document you were working in
	// is the surprising half of a hand-off: CanWrite answers to the owner and
	// has no creator fallback. It only ever ADDS — unticking it withdraws
	// nothing, and read on versions they created is theirs either way.
	KeepAccess *bool `json:"keep_access"`
}

// OwnerResponse reports the outcome of a transfer, naming both ends so the
// caller can log what actually moved rather than what they asked for.
type OwnerResponse struct {
	Slug          string `json:"slug"`
	Owner         string `json:"owner"`
	PreviousOwner string `json:"previous_owner"`
}

// emailish is a deliberately loose check: it rejects the mistakes that brick a
// document (a name, a slug, a group token, a glob) without trying to decide
// which addresses are real. A transfer to a valid-but-wrong address is
// recoverable — an admin can always transfer again.
//
// Globs matter here in a way they don't in an ACL: `*@example.com` grants a
// whole domain read access, but ownership is one principal, and a pattern
// stored as an owner would match nobody at all.
var emailish = regexp.MustCompile(`^[^@\s*?]+@[^@\s*?]+\.[^@\s*?]+$`)

// TransferOwner moves ownership of slug to newOwner. Owner-or-admin only: the
// same authority as an ACL change or the comment switch, resolved through
// isDocOwner so no surface can offer a transfer the store would refuse.
//
// keepAccess grants the outgoing owner read and write inside the same
// transaction as the move.
func (s *Service) TransferOwner(ctx context.Context, slug, newOwner, caller string, keepAccess bool) (OwnerResponse, error) {
	newOwner = strings.ToLower(strings.TrimSpace(newOwner))
	if !emailish.MatchString(newOwner) {
		return OwnerResponse{}, badRequest{msg: "owner must be an email address"}
	}
	// Read unfiltered: an owner or admin who holds no read token on the
	// document is exactly who needs this endpoint, and 404ing them would leave
	// the document unfixable. Authority is decided by isDocOwner below, and a
	// caller who is neither the owner nor able to read the document is told
	// only that it does not exist — no existence oracle on a private slug.
	row, err := s.store.GetBySlug(ctx, slug, nil)
	if err != nil {
		return OwnerResponse{}, err
	}
	if oerr := s.isDocOwner(ctx, row, caller); oerr != nil {
		if _, rerr := s.store.GetLatestBySlugForCaller(ctx, slug, caller); rerr != nil {
			return OwnerResponse{}, rerr
		}
		return OwnerResponse{}, oerr
	}
	admin, err := s.canManageArtifacts(ctx, caller)
	if err != nil {
		return OwnerResponse{}, err
	}
	prev, err := s.store.TransferSlugOwner(ctx, slug, newOwner, caller, keepAccess, func(prev string) error {
		if admin || strings.EqualFold(prev, caller) {
			return nil
		}
		return errForbidden("only the document's owner or an admin may transfer it")
	})
	if err != nil {
		if errors.Is(err, pgstore.ErrNoOwner) {
			return OwnerResponse{}, pgstore.ErrNotFound
		}
		return OwnerResponse{}, err
	}
	// A transfer can widen allowed_access (the new owner's read grant), so the
	// index has to converge the same way an ACL fan-out does.
	if s.osIndexer.Enabled() {
		go s.reindexSlugSiblings(context.Background(), slug, pgtype.UUID{})
	}
	s.notifyOwnerTransfer(ctx, row, newOwner, prev, caller)
	return OwnerResponse{Slug: slug, Owner: newOwner, PreviousOwner: prev}, nil
}

// notifyOwnerTransfer tells the recipient they now hold the document. A failed
// DM never fails the transfer: the move has already committed, and undoing an
// authority change because Slack was unreachable would be worse than a missed
// message. Someone acquiring authority over a document without being told is
// the case this exists for.
func (s *Service) notifyOwnerTransfer(ctx context.Context, row sqlc.Artifact, recipient, prev, caller string) {
	if s.notifier == nil {
		return
	}
	actor := auth.NameFromContext(ctx)
	if actor == "" {
		actor = caller
	}
	s.notifier.NotifyOwnerTransfer(ctx, slacknotify.OwnerTransfer{
		Recipient:     recipient,
		PreviousOwner: prev,
		ActorName:     actor,
		Title:         row.Title,
		ArtifactURL:   ToInfo(row, s.baseURL).URL,
	})
}

// httpTransferOwner handles POST /api/artifacts/by-slug/{slug}/owner.
func (s *Service) httpTransferOwner(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "bad-request", "missing slug")
		return
	}
	var body OwnerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "decode body: "+err.Error())
		return
	}
	keepAccess := body.KeepAccess == nil || *body.KeepAccess
	resp, err := s.TransferOwner(r.Context(), slug, body.Owner, auth.EmailFromContext(r.Context()), keepAccess)
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
		if errors.Is(err, pgstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not-found", "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
