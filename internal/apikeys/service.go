package apikeys

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
)

const (
	defaultTTL = 90 * 24 * time.Hour
	minTTL     = 24 * time.Hour
)

// serviceStore is the minimal store interface needed by Service.
type serviceStore interface {
	InsertAPIKey(ctx context.Context, arg sqlc.InsertAPIKeyParams) (sqlc.ApiKey, error)
	ListAPIKeysByOwner(ctx context.Context, email string) ([]sqlc.ApiKey, error)
	ListAllAPIKeys(ctx context.Context) ([]sqlc.ApiKey, error)
	RevokeAPIKey(ctx context.Context, arg sqlc.RevokeAPIKeyParams) (int64, error)
	RevokeAPIKeyByID(ctx context.Context, id pgtype.UUID) (int64, error)
}

// Service handles the mint/list/revoke HTTP layer for API keys.
type Service struct {
	store        serviceStore
	maxTTL       time.Duration
	canManageAll func(ctx context.Context, email string) (bool, error)
}

// NewService returns a Service backed by store with the given TTL cap.
// canManageAll is called to decide whether the caller may see/revoke all users'
// keys (the cross-owner admin view). Wire it to pgstoreInst.HasPermission with
// rbac.ManageAPIKeys in production.
func NewService(store serviceStore, maxTTL time.Duration, canManageAll func(ctx context.Context, email string) (bool, error)) *Service {
	return &Service{store: store, maxTTL: maxTTL, canManageAll: canManageAll}
}

// Mount registers the API key routes on r. mintLimiter is applied only to POST
// (the mint path); list and revoke are authenticated but not separately rate-limited.
func (s *Service) Mount(r chi.Router, mintLimiter func(http.Handler) http.Handler) {
	r.With(mintLimiter).Post("/api/keys", s.httpCreate)
	r.Get("/api/keys", s.httpList)
	r.Delete("/api/keys/{id}", s.httpRevoke)
}

// ─── request/response types ─────────────────────────────────────────────────

type createReq struct {
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
	TTLDays int      `json:"ttl_days"`
}

// apiKeyView is the JSON view of an api_keys row; never exposes key_hash.
type apiKeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	OwnerEmail string     `json:"owner_email"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// createdKeyView extends apiKeyView with the one-time plaintext key.
type createdKeyView struct {
	apiKeyView
	Key string `json:"key"`
}

func toView(row sqlc.ApiKey) apiKeyView {
	v := apiKeyView{
		ID:         uuid.UUID(row.ID.Bytes).String(),
		Name:       row.Name,
		KeyPrefix:  row.KeyPrefix,
		OwnerEmail: row.OwnerEmail,
		Scopes:     row.Scopes,
		CreatedAt:  row.CreatedAt.Time,
		ExpiresAt:  row.ExpiresAt.Time,
	}
	if row.LastUsedAt.Valid {
		t := row.LastUsedAt.Time
		v.LastUsedAt = &t
	}
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		v.RevokedAt = &t
	}
	return v
}

// ─── handlers ───────────────────────────────────────────────────────────────

func (s *Service) httpCreate(w http.ResponseWriter, r *http.Request) {
	// Belt-and-suspenders: api keys can't mint keys.
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims.Typ == "api-key" {
		writeErr(w, http.StatusForbidden, "api keys cannot mint other keys")
		return
	}
	email := auth.EmailFromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body createReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Default scopes to ["upload"].
	if len(body.Scopes) == 0 {
		body.Scopes = []string{auth.UploadScope}
	}
	// v1: exactly one scope per key, so the stored scopes always match the key's
	// {scope} prefix (no divergence once more scopes are supported).
	if len(body.Scopes) != 1 {
		writeErr(w, http.StatusBadRequest, "exactly one scope is supported per key")
		return
	}
	scope := body.Scopes[0]
	if !IsSupportedScope(scope) {
		writeErr(w, http.StatusBadRequest, "unsupported scope: "+scope)
		return
	}

	if body.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}

	// Clamp TTL: default 90d, minimum 1d, maximum maxTTL.
	ttl := defaultTTL
	if body.TTLDays > 0 {
		ttl = time.Duration(body.TTLDays) * 24 * time.Hour
	}
	if ttl < minTTL {
		ttl = minTTL
	}
	if ttl > s.maxTTL {
		ttl = s.maxTTL
	}

	plaintext, hash, prefix, err := GenerateKey(scope)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "key generation failed")
		return
	}

	row, err := s.store.InsertAPIKey(r.Context(), sqlc.InsertAPIKeyParams{
		KeyHash:    hash,
		KeyPrefix:  prefix,
		OwnerEmail: email,
		Name:       body.Name,
		Scopes:     body.Scopes,
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(ttl), Valid: true},
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to store key")
		return
	}

	slog.Info("api key minted",
		"owner", email,
		"id", uuid.UUID(row.ID.Bytes).String(),
		"scopes", body.Scopes,
	)

	writeJSON(w, http.StatusCreated, createdKeyView{
		apiKeyView: toView(row),
		Key:        plaintext,
	})
}

func (s *Service) httpList(w http.ResponseWriter, r *http.Request) {
	email := auth.EmailFromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var rows []sqlc.ApiKey
	var err error
	all := false
	if r.URL.Query().Get("all") == "true" {
		ok, cerr := s.canManageAll(r.Context(), email)
		if cerr != nil {
			// Degrade to own-keys rather than 500: a permission-check blip must
			// not block a user from listing their own keys. Non-admins already
			// fall back to own-keys for ?all=true, so this stays consistent.
			slog.Error("api key list: canManageAll failed; showing own keys", "email", email, "err", cerr)
		}
		all = ok
	}
	if all {
		rows, err = s.store.ListAllAPIKeys(r.Context())
	} else {
		rows, err = s.store.ListAPIKeysByOwner(r.Context(), email)
	}
	if err != nil {
		slog.Error("api key list failed", "email", email, "err", err)
		writeErr(w, http.StatusInternalServerError, "failed to list keys")
		return
	}

	out := make([]apiKeyView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toView(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) httpRevoke(w http.ResponseWriter, r *http.Request) {
	email := auth.EmailFromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	u, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	pgID := pgtype.UUID{Bytes: u, Valid: true}

	// Self-service first: revoking your OWN key must not depend on the
	// (DB-backed, fallible) admin permission check. Only when the owner-scoped
	// revoke matches nothing do we consult canManageAll to let an admin revoke
	// another user's key.
	n, err := s.store.RevokeAPIKey(r.Context(), sqlc.RevokeAPIKeyParams{
		ID:         pgID,
		OwnerEmail: email,
	})
	if err != nil {
		slog.Error("api key revoke failed", "revoker", email, "err", err)
		writeErr(w, http.StatusInternalServerError, "failed to revoke key")
		return
	}
	if n == 0 {
		ok, cerr := s.canManageAll(r.Context(), email)
		if cerr != nil {
			slog.Error("api key revoke: canManageAll failed", "revoker", email, "err", cerr)
			writeErr(w, http.StatusInternalServerError, "failed to check permissions")
			return
		}
		if ok {
			n, err = s.store.RevokeAPIKeyByID(r.Context(), pgID)
			if err != nil {
				slog.Error("api key revoke (admin) failed", "revoker", email, "err", err)
				writeErr(w, http.StatusInternalServerError, "failed to revoke key")
				return
			}
		}
	}
	if n == 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	slog.Info("api key revoked",
		"revoker", email,
		"id", u.String(),
	)

	w.WriteHeader(http.StatusNoContent)
}

// ─── JSON helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"detail": msg, "code": http.StatusText(code)})
}
