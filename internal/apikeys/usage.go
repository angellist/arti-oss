package apikeys

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
)

// usageStore reads the credential usage records written by internal/credusage.
type usageStore interface {
	ListCredentialSourcesByOwner(ctx context.Context, arg sqlc.ListCredentialSourcesByOwnerParams) ([]sqlc.ListCredentialSourcesByOwnerRow, error)
	ListCredentialSources(ctx context.Context, day pgtype.Date) ([]sqlc.ListCredentialSourcesRow, error)
	CountArtifactsByCredential(ctx context.Context, creator string) ([]sqlc.CountArtifactsByCredentialRow, error)
	CountArtifactsByCredentialAll(ctx context.Context) ([]sqlc.CountArtifactsByCredentialAllRow, error)
}

// defaultUsageDays is the window the settings page asks for.
const defaultUsageDays = 30

// maxUsageDays caps the window so one request cannot scan the whole table.
const maxUsageDays = 365

// sourceView is one place a credential has been used from.
type sourceView struct {
	Cred       string     `json:"cred"`
	OwnerEmail string     `json:"owner_email,omitempty"` // set on the admin view only
	IP         string     `json:"ip"`
	UserAgent  string     `json:"user_agent"`
	Reads      int64      `json:"reads"`
	Writes     int64      `json:"writes"`
	FirstSeen  *time.Time `json:"first_seen"`
	LastSeen   *time.Time `json:"last_seen"`
}

type usageResponse struct {
	Sources []sourceView     `json:"sources"`
	Docs    map[string]int64 `json:"docs"` // credential reference → live documents written
	Days    int              `json:"days"`
}

// httpUsage answers GET /api/keys/usage: where each of the caller's
// credentials has been used, and how many documents each has written.
// `?all=true` widens it to every owner for a MANAGE_API_KEYS holder, the same
// authority that lists everyone's keys.
func (s *Service) httpUsage(w http.ResponseWriter, r *http.Request) {
	email := auth.EmailFromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if s.usage == nil {
		writeJSON(w, http.StatusOK, usageResponse{Sources: []sourceView{}, Docs: map[string]int64{}, Days: 0})
		return
	}

	days := defaultUsageDays
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = min(n, maxUsageDays)
		}
	}
	since := pgtype.Date{Time: time.Now().UTC().AddDate(0, 0, -days), Valid: true}

	all := false
	if r.URL.Query().Get("all") == "true" {
		ok, err := s.canManageAll(r.Context(), email)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "permission check failed")
			return
		}
		all = ok // silently narrows to own usage, as the key list does
	}

	out := usageResponse{Sources: []sourceView{}, Docs: map[string]int64{}, Days: days}
	if all {
		rows, err := s.usage.ListCredentialSources(r.Context(), since)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to read usage")
			return
		}
		for _, row := range rows {
			out.Sources = append(out.Sources, sourceView{
				Cred: row.Cred, OwnerEmail: row.OwnerEmail, IP: row.Ip, UserAgent: row.UserAgent,
				Reads: row.Reads, Writes: row.Writes,
				FirstSeen: tsPtr(row.FirstSeen), LastSeen: tsPtr(row.LastSeen),
			})
		}
		counts, err := s.usage.CountArtifactsByCredentialAll(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to count documents")
			return
		}
		for _, c := range counts {
			if c.WrittenVia != nil {
				out.Docs[*c.WrittenVia] = c.Docs
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	rows, err := s.usage.ListCredentialSourcesByOwner(r.Context(), sqlc.ListCredentialSourcesByOwnerParams{
		OwnerEmail: email, Day: since,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read usage")
		return
	}
	for _, row := range rows {
		out.Sources = append(out.Sources, sourceView{
			Cred: row.Cred, IP: row.Ip, UserAgent: row.UserAgent,
			Reads: row.Reads, Writes: row.Writes,
			FirstSeen: tsPtr(row.FirstSeen), LastSeen: tsPtr(row.LastSeen),
		})
	}
	counts, err := s.usage.CountArtifactsByCredential(r.Context(), email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to count documents")
		return
	}
	for _, c := range counts {
		if c.WrittenVia != nil {
			out.Docs[*c.WrittenVia] = c.Docs
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
