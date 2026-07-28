package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html"
	"math/big"
	"net/http"
	"strings"
	"time"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/google/uuid"
)

// userCodeAlphabet excludes visually ambiguous characters (I, L, O, U, 0, 1)
// so a human reading the code off an agent's output can't mistype it.
const userCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// newUserCode returns an 8-char code formatted XXXX-XXXX (~30^8 ≈ 6.5e11 space),
// rate-limited by the 10-minute grant window and single-use pickup.
func newUserCode() (string, error) {
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = userCodeAlphabet[n.Int64()]
	}
	return string(b[:4]) + "-" + string(b[4:]), nil
}

// newDeviceCode returns a 32-byte base64url secret — the agent's polling token,
// never seen by the human.
func newDeviceCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DeviceStore is the DB surface the device-flow handlers need. *pgstore.Store
// satisfies it via its embedded *sqlc.Queries (same pattern as MCPOAuthStore).
type DeviceStore interface {
	InsertDeviceCode(ctx context.Context, arg sqlc.InsertDeviceCodeParams) error
	GetDeviceByUserCode(ctx context.Context, userCode string) (sqlc.DeviceAuth, error)
	GetDeviceCode(ctx context.Context, deviceCode string) (sqlc.DeviceAuth, error)
	ApproveDeviceCode(ctx context.Context, arg sqlc.ApproveDeviceCodeParams) error
	TakeApprovedDeviceCode(ctx context.Context, deviceCode string) (sqlc.DeviceAuth, error)
	InsertDeviceTokenFamily(ctx context.Context, arg sqlc.InsertDeviceTokenFamilyParams) error
	GetDeviceTokenFamily(ctx context.Context, familyID string) (sqlc.DeviceToken, error)
	RotateDeviceTokenRefresh(ctx context.Context, arg sqlc.RotateDeviceTokenRefreshParams) (int64, error)
	RevokeDeviceTokensForEmail(ctx context.Context, email string) error
	RevokeDeviceTokenFamily(ctx context.Context, familyID string) error
}

// strptr returns &s — the wire shape sqlc generates for the NULLABLE `email`
// column under emit_pointers_for_null_types=true (nullable TEXT -> *string).
func strptr(s string) *string { return &s }

// DeviceConfig wires the device-flow handlers.
type DeviceConfig struct {
	Store         DeviceStore
	Signer        *JWTSigner
	BaseURL       string
	AccessTTL     time.Duration // short access token lifetime (default 24h)
	MaxRefreshTTL time.Duration // long refresh cap (default 720h = 30d)
}

const deviceGrantWindow = 10 * time.Minute

// DeviceCodeHandler — POST /auth/device/code. Public, server-to-server.
func DeviceCodeHandler(cfg DeviceConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Duration string `json:"duration"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // empty body OK -> short
		dur := "short"
		if body.Duration == "long" {
			dur = "long"
		}
		dc, err := newDeviceCode()
		if err != nil {
			writeErr(w, 500, "code gen", "internal")
			return
		}
		uc, err := newUserCode()
		if err != nil {
			writeErr(w, 500, "code gen", "internal")
			return
		}
		exp := time.Now().Add(deviceGrantWindow)
		if err := cfg.Store.InsertDeviceCode(r.Context(), sqlc.InsertDeviceCodeParams{
			DeviceCode: dc, UserCode: uc, Duration: dur,
			ExpiresAt: pgTimestamp(exp),
		}); err != nil {
			writeErr(w, 500, "store", "internal")
			return
		}
		verURI := cfg.BaseURL + "/auth/device"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               dc,
			"user_code":                 uc,
			"verification_uri":          verURI,
			"verification_uri_complete": verURI + "?user_code=" + uc,
			"expires_in":                int(deviceGrantWindow.Seconds()),
			"interval":                  5,
		})
	}
}

// DeviceConfirmHandler — GET /auth/device. Public code page; shows the code
// + requested duration and a button that starts the SSO-protected
// /auth/login?user_code=… flow. The authenticated user must then explicitly
// confirm the binding on the resulting page.
func DeviceConfirmHandler(cfg DeviceConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uc := r.URL.Query().Get("user_code")
		human := "this device"
		if uc != "" {
			if da, err := cfg.Store.GetDeviceByUserCode(r.Context(), uc); err == nil {
				if da.Duration == "long" {
					human = "an agent requesting upload access for up to 30 days"
				} else {
					human = "an agent requesting one-time upload access (24h)"
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>arti — approve device</title>
<style>body{font-family:system-ui;text-align:center;margin-top:18vh;color:#333}
.code{font:600 28px ui-monospace,monospace;letter-spacing:2px;margin:18px}
button{font-size:16px;padding:10px 22px;border-radius:8px;border:1px solid #888;cursor:pointer}</style>
<h1>Approve upload access?</h1>
<p>You are about to grant ` + html.EscapeString(human) + `.</p>
<div class="code">` + html.EscapeString(uc) + `</div>
<form method="GET" action="/auth/login">
<input type="hidden" name="user_code" value="` + html.EscapeString(uc) + `">
<button type="submit">Sign in &amp; Approve</button></form>`))
	}
}

// DeviceTokenHandler — POST /auth/device/token. Poll; mints on approval.
func DeviceTokenHandler(cfg DeviceConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeviceCode string `json:"device_code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceCode == "" {
			writeErr(w, 400, "bad request", "bad-request")
			return
		}
		da, err := cfg.Store.GetDeviceCode(r.Context(), body.DeviceCode)
		if err != nil {
			writeErr(w, 400, "expired_token", "expired_token")
			return
		}
		if da.ExpiresAt.Valid && da.ExpiresAt.Time.Before(time.Now()) {
			writeErr(w, 400, "expired_token", "expired_token")
			return
		}
		switch da.Status {
		case "pending":
			writeErr(w, 428, "authorization_pending", "authorization_pending")
			return
		case "consumed":
			writeErr(w, 400, "expired_token", "expired_token")
			return
		}
		// approved: atomically consume, then re-check the email is still allowed.
		taken, err := cfg.Store.TakeApprovedDeviceCode(r.Context(), body.DeviceCode)
		if err != nil {
			writeErr(w, 400, "expired_token", "expired_token")
			return
		}
		if taken.Email == nil { // approved row must carry the SSO-verified email
			writeErr(w, 400, "access_denied", "access_denied")
			return
		}
		email := *taken.Email
		if !IsAllowed(email) {
			writeErr(w, 403, "access_denied", "access_denied")
			return
		}
		out, err := issueUploadTokens(r.Context(), cfg, email, taken.Duration)
		if err != nil {
			writeErr(w, 500, "issue", "internal")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// issueUploadTokens mints the upload token(s). short -> one access token, no
// refresh, no family row. long -> access + rotating 30d refresh tracked in a
// device_token family; re-approval first revokes the user's prior families.
func issueUploadTokens(ctx context.Context, cfg DeviceConfig, email, duration string) (map[string]any, error) {
	access, err := cfg.Signer.Sign(Claims{Email: email, Scopes: []string{UploadScope}, TTL: cfg.AccessTTL})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"access_token": access, "token_type": "Bearer",
		"expires_in": int(cfg.AccessTTL.Seconds()), "scope": "upload", "email": email,
	}
	if duration != "long" {
		return out, nil
	}
	// TODO(device-auth, PR #74): revoke + insert (and the TakeApprovedDeviceCode
	// consume that precedes this call) are separate statements, not one
	// transaction. A failure between revoke and insert leaves the user with no
	// active family until they re-approve; concurrent pickups are bounded by the
	// partial unique index on device_token(LOWER(email)) WHERE NOT revoked
	// (migration 0013), which fails the losing insert rather than duplicating.
	// Wrap consume+revoke+insert in a single tx for full atomicity.
	if err := cfg.Store.RevokeDeviceTokensForEmail(ctx, email); err != nil { // one active per user
		return nil, err
	}
	fam := uuid.NewString()
	refreshJTI := uuid.NewString()
	refresh, err := cfg.Signer.Sign(Claims{Email: email, Scopes: []string{"refresh"},
		Fam: fam, JTI: refreshJTI, TTL: cfg.MaxRefreshTTL})
	if err != nil {
		return nil, err
	}
	// Re-sign the access token to carry the family id (so the upload guard can
	// honor revocation of long-lived tokens).
	access, err = cfg.Signer.Sign(Claims{Email: email, Scopes: []string{UploadScope}, Fam: fam, TTL: cfg.AccessTTL})
	if err != nil {
		return nil, err
	}
	if err := cfg.Store.InsertDeviceTokenFamily(ctx, sqlc.InsertDeviceTokenFamilyParams{
		FamilyID: fam, Email: email, CurrentRefreshJti: refreshJTI,
		ExpiresAt: pgTimestamp(time.Now().Add(cfg.MaxRefreshTTL)),
	}); err != nil {
		return nil, err
	}
	out["access_token"] = access
	out["refresh_token"] = refresh
	out["refresh_expires_in"] = int(cfg.MaxRefreshTTL.Seconds())
	return out, nil
}

// DeviceRefreshHandler — POST /auth/device/refresh. Rotates the family.
func DeviceRefreshHandler(cfg DeviceConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RefreshToken == "" {
			writeErr(w, 400, "bad request", "bad-request")
			return
		}
		c, err := cfg.Signer.Verify(body.RefreshToken)
		if err != nil || !containsString(c.Scopes, "refresh") || c.Fam == "" {
			writeErr(w, 401, "invalid refresh", "unauthorized")
			return
		}
		fam, err := cfg.Store.GetDeviceTokenFamily(r.Context(), c.Fam)
		if err != nil || fam.Revoked || c.JTI != fam.CurrentRefreshJti {
			writeErr(w, 401, "refresh revoked or superseded", "unauthorized")
			return
		}
		if !IsAllowed(c.Email) {
			writeErr(w, 403, "access_denied", "access_denied")
			return
		}
		newJTI := uuid.NewString()
		// Compare-and-swap on the presented (old) jti: the in-memory check above
		// is racy, so the rotate is gated in SQL on current_refresh_jti = prev.
		// Two concurrent refreshes with the same token → only one rotates
		// (n==1); the loser gets n==0 and is rejected, defeating replay.
		n, err := cfg.Store.RotateDeviceTokenRefresh(r.Context(), sqlc.RotateDeviceTokenRefreshParams{
			FamilyID: c.Fam, NewJti: newJTI, PrevJti: c.JTI,
		})
		if err != nil || n == 0 {
			writeErr(w, 401, "refresh revoked or superseded", "unauthorized")
			return
		}
		access, _ := cfg.Signer.Sign(Claims{Email: c.Email, Scopes: []string{UploadScope}, Fam: c.Fam, TTL: cfg.AccessTTL})
		refresh, _ := cfg.Signer.Sign(Claims{Email: c.Email, Scopes: []string{"refresh"},
			Fam: c.Fam, JTI: newJTI, TTL: cfg.MaxRefreshTTL})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": access, "refresh_token": refresh, "token_type": "Bearer",
			"expires_in": int(cfg.AccessTTL.Seconds()), "scope": "upload", "email": c.Email,
		})
	}
}

// DeviceRevokeHandler — POST /auth/device/revoke. Authed (RequireAuth). Revokes
// the caller's own families; admins may pass {"email":"…"} to revoke another.
func DeviceRevokeHandler(cfg DeviceConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := EmailFromContext(r.Context())
		target := caller
		var body struct {
			Email string `json:"email"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Email != "" && !strings.EqualFold(body.Email, caller) {
			// TODO(device-auth, PR #74): the rest of the codebase gates admin
			// actions via RBAC store.HasPermission(...), not the legacy env-based
			// IsAdmin. Cross-user revocation should move to a HasPermission check
			// (needs a permission checker wired into DeviceConfig + a decision on
			// which rbac.Permission governs token revocation). Self-revoke (the
			// common path) is unaffected.
			if !IsAdmin(caller) {
				writeErr(w, 403, "only admins may revoke another user's tokens", "forbidden")
				return
			}
			target = body.Email
		}
		if err := cfg.Store.RevokeDeviceTokensForEmail(r.Context(), target); err != nil {
			writeErr(w, 500, "revoke", "internal")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
