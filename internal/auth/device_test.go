package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

var userCodeRe = regexp.MustCompile(`^[0-9A-Z]{4}-[0-9A-Z]{4}$`)

func TestNewUserCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		uc, err := newUserCode()
		if err != nil {
			t.Fatal(err)
		}
		if !userCodeRe.MatchString(uc) {
			t.Fatalf("bad user_code shape: %q", uc)
		}
		// unambiguous alphabet: no I, L, O, U, 0, 1
		for _, bad := range []string{"I", "L", "O", "U", "0", "1"} {
			if regexp.MustCompile(bad).MatchString(uc) {
				t.Fatalf("user_code %q contains ambiguous char %s", uc, bad)
			}
		}
		seen[uc] = true
	}
	if len(seen) < 95 {
		t.Fatalf("user_codes not random enough: %d unique of 100", len(seen))
	}
}

func TestNewDeviceCodeUnique(t *testing.T) {
	a, _ := newDeviceCode()
	b, _ := newDeviceCode()
	if a == b || len(a) < 40 {
		t.Fatalf("device_code weak/duplicate: %q %q", a, b)
	}
}

// fakeDeviceStore is an in-memory DeviceStore for handler unit tests.
// It mirrors the SQL semantics faithfully: status transitions are enforced,
// expiry is checked, and family JTI rotation is tracked.
type fakeDeviceStore struct {
	auths    map[string]sqlc.DeviceAuth  // keyed by device_code
	byUser   map[string]string           // user_code -> device_code
	families map[string]sqlc.DeviceToken // keyed by family_id
}

func newFakeStore() *fakeDeviceStore {
	return &fakeDeviceStore{
		auths:    map[string]sqlc.DeviceAuth{},
		byUser:   map[string]string{},
		families: map[string]sqlc.DeviceToken{},
	}
}

func (f *fakeDeviceStore) InsertDeviceCode(_ context.Context, arg sqlc.InsertDeviceCodeParams) error {
	f.auths[arg.DeviceCode] = sqlc.DeviceAuth{
		DeviceCode: arg.DeviceCode,
		UserCode:   arg.UserCode,
		Duration:   arg.Duration,
		Status:     "pending",
		ExpiresAt:  arg.ExpiresAt,
	}
	f.byUser[arg.UserCode] = arg.DeviceCode
	return nil
}

func (f *fakeDeviceStore) GetDeviceByUserCode(_ context.Context, userCode string) (sqlc.DeviceAuth, error) {
	dc, ok := f.byUser[userCode]
	if !ok {
		return sqlc.DeviceAuth{}, pgx.ErrNoRows
	}
	da, ok := f.auths[dc]
	if !ok || da.ExpiresAt.Time.Before(time.Now()) {
		return sqlc.DeviceAuth{}, pgx.ErrNoRows
	}
	return da, nil
}

func (f *fakeDeviceStore) GetDeviceCode(_ context.Context, deviceCode string) (sqlc.DeviceAuth, error) {
	da, ok := f.auths[deviceCode]
	if !ok {
		return sqlc.DeviceAuth{}, pgx.ErrNoRows
	}
	return da, nil
}

// ApproveDeviceCode only flips pending->approved; mirrors the SQL WHERE status='pending'.
func (f *fakeDeviceStore) ApproveDeviceCode(_ context.Context, arg sqlc.ApproveDeviceCodeParams) error {
	dc, ok := f.byUser[arg.UserCode]
	if !ok {
		return nil // no-op if not found (SQL :exec returns no error)
	}
	da := f.auths[dc]
	if da.Status != "pending" || da.ExpiresAt.Time.Before(time.Now()) {
		return nil // SQL WHERE rejects this silently
	}
	da.Status = "approved"
	da.Email = arg.Email
	f.auths[dc] = da
	return nil
}

// TakeApprovedDeviceCode only flips approved->consumed; returns pgx.ErrNoRows
// for any other status (pending, consumed, expired). This makes a second poll
// return an error, enforcing single-use semantics.
func (f *fakeDeviceStore) TakeApprovedDeviceCode(_ context.Context, deviceCode string) (sqlc.DeviceAuth, error) {
	da, ok := f.auths[deviceCode]
	if !ok || da.Status != "approved" || da.ExpiresAt.Time.Before(time.Now()) {
		return sqlc.DeviceAuth{}, pgx.ErrNoRows
	}
	da.Status = "consumed"
	da.Consumed = true
	f.auths[deviceCode] = da
	return da, nil
}

func (f *fakeDeviceStore) InsertDeviceTokenFamily(_ context.Context, arg sqlc.InsertDeviceTokenFamilyParams) error {
	f.families[arg.FamilyID] = sqlc.DeviceToken{
		FamilyID:          arg.FamilyID,
		Email:             arg.Email,
		CurrentRefreshJti: arg.CurrentRefreshJti,
		ExpiresAt:         arg.ExpiresAt,
		Revoked:           false,
	}
	return nil
}

func (f *fakeDeviceStore) GetDeviceTokenFamily(_ context.Context, familyID string) (sqlc.DeviceToken, error) {
	fam, ok := f.families[familyID]
	if !ok {
		return sqlc.DeviceToken{}, pgx.ErrNoRows
	}
	return fam, nil
}

// RotateDeviceTokenRefresh returns 0 rows when the family is revoked, expired,
// or the presented (prev) jti no longer matches — mirroring the SQL
// WHERE current_refresh_jti = @prev_jti AND NOT revoked AND expires_at > now()
// (the compare-and-swap that defeats concurrent refresh replay).
func (f *fakeDeviceStore) RotateDeviceTokenRefresh(_ context.Context, arg sqlc.RotateDeviceTokenRefreshParams) (int64, error) {
	fam, ok := f.families[arg.FamilyID]
	if !ok || fam.Revoked || fam.ExpiresAt.Time.Before(time.Now()) || fam.CurrentRefreshJti != arg.PrevJti {
		return 0, nil
	}
	fam.CurrentRefreshJti = arg.NewJti
	fam.RotatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.families[arg.FamilyID] = fam
	return 1, nil
}

// RevokeDeviceTokensForEmail marks all non-revoked families for the user as
// revoked, matching email case-insensitively (mirrors SQL LOWER(email)=LOWER($1)).
func (f *fakeDeviceStore) RevokeDeviceTokensForEmail(_ context.Context, email string) error {
	for id, fam := range f.families {
		if strings.EqualFold(fam.Email, email) && !fam.Revoked {
			fam.Revoked = true
			f.families[id] = fam
		}
	}
	return nil
}

func (f *fakeDeviceStore) RevokeDeviceTokenFamily(_ context.Context, familyID string) error {
	fam, ok := f.families[familyID]
	if !ok {
		return nil
	}
	fam.Revoked = true
	f.families[familyID] = fam
	return nil
}

func TestDeviceFlowHappyPath(t *testing.T) {
	st := newFakeStore()
	cfg := DeviceConfig{Store: st, Signer: NewJWTSigner([]byte("k")), BaseURL: "https://arti.example",
		AccessTTL: time.Hour, MaxRefreshTTL: 720 * time.Hour}
	SetAllowedEmails([]string{"example.com"})
	t.Cleanup(func() { SetAllowedEmails([]string{"example.com", "example.org"}) })

	// 1. agent starts the grant (duration=long)
	rr := httptest.NewRecorder()
	DeviceCodeHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/code",
		bytes.NewBufferString(`{"duration":"long"}`)))
	if rr.Code != 200 {
		t.Fatalf("code: want 200 got %d", rr.Code)
	}
	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	json.Unmarshal(rr.Body.Bytes(), &start)

	// 2. poll before approval -> 428
	rr = httptest.NewRecorder()
	DeviceTokenHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/token",
		bytes.NewBufferString(`{"device_code":"`+start.DeviceCode+`"}`)))
	if rr.Code != 428 {
		t.Fatalf("pending poll: want 428 got %d", rr.Code)
	}

	// 3. human approves (simulate the ingress branch)
	st.ApproveDeviceCode(context.Background(), sqlc.ApproveDeviceCodeParams{UserCode: start.UserCode, Email: strptr("dev@example.com")})

	// 4. poll after approval -> 200 with upload-scoped access + refresh
	rr = httptest.NewRecorder()
	DeviceTokenHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/token",
		bytes.NewBufferString(`{"device_code":"`+start.DeviceCode+`"}`)))
	if rr.Code != 200 {
		t.Fatalf("approved poll: want 200 got %d body=%s", rr.Code, rr.Body)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Scope != "upload" || out.RefreshToken == "" {
		t.Fatalf("bad token payload: %+v", out)
	}
	c, _ := cfg.Signer.Verify(out.AccessToken)
	if !c.IsUploadScoped() || c.Fam == "" {
		t.Fatalf("access token not upload-scoped with fam: %+v", c)
	}

	// 5. second poll -> consumed -> 400
	rr = httptest.NewRecorder()
	DeviceTokenHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/token",
		bytes.NewBufferString(`{"device_code":"`+start.DeviceCode+`"}`)))
	if rr.Code != 400 {
		t.Fatalf("consumed poll: want 400 got %d", rr.Code)
	}
}

// TestDeviceShortDurationNoRefresh verifies that duration=short mints a
// single upload-scoped access token with NO refresh_token and NO fam claim.
// Intent: short-lived tokens must not create DB-tracked families, so a leaked
// token cannot be rotated into a long-lived session.
func TestDeviceShortDurationNoRefresh(t *testing.T) {
	st := newFakeStore()
	cfg := DeviceConfig{Store: st, Signer: NewJWTSigner([]byte("k")), BaseURL: "https://arti.example",
		AccessTTL: time.Hour, MaxRefreshTTL: 720 * time.Hour}
	SetAllowedEmails([]string{"example.com"})
	t.Cleanup(func() { SetAllowedEmails([]string{"example.com", "example.org"}) })

	// 1. agent starts the grant (duration=short — the default)
	rr := httptest.NewRecorder()
	DeviceCodeHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/code",
		bytes.NewBufferString(`{"duration":"short"}`)))
	if rr.Code != 200 {
		t.Fatalf("code: want 200 got %d", rr.Code)
	}
	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	json.Unmarshal(rr.Body.Bytes(), &start)

	// 2. human approves
	st.ApproveDeviceCode(context.Background(), sqlc.ApproveDeviceCodeParams{
		UserCode: start.UserCode, Email: strptr("dev@example.com"),
	})

	// 3. poll -> 200
	rr = httptest.NewRecorder()
	DeviceTokenHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/token",
		bytes.NewBufferString(`{"device_code":"`+start.DeviceCode+`"}`)))
	if rr.Code != 200 {
		t.Fatalf("approved poll: want 200 got %d body=%s", rr.Code, rr.Body)
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	json.Unmarshal(rr.Body.Bytes(), &out)

	// short duration must not produce a refresh token
	if out.RefreshToken != "" {
		t.Fatalf("short duration must not emit refresh_token, got non-empty")
	}

	// access token must be upload-scoped but must NOT carry a fam claim
	c, err := cfg.Signer.Verify(out.AccessToken)
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	if !c.IsUploadScoped() {
		t.Fatalf("access token must be upload-scoped: %+v", c)
	}
	if c.Fam != "" {
		t.Fatalf("short-duration access token must have empty Fam, got %q", c.Fam)
	}

	// no family row must have been inserted
	if len(st.families) != 0 {
		t.Fatalf("short-duration flow must not create token families, got %d", len(st.families))
	}
}

// TestDeviceRefreshRotatesAndRejectsStale verifies the token-rotation contract:
// after a successful refresh the old refresh token is rejected (its JTI no
// longer matches the family's current_refresh_jti). This prevents refresh-token
// replay and implements automatic family invalidation on theft.
func TestDeviceRefreshRotatesAndRejectsStale(t *testing.T) {
	st := newFakeStore()
	cfg := DeviceConfig{Store: st, Signer: NewJWTSigner([]byte("k")), BaseURL: "https://arti.example",
		AccessTTL: time.Hour, MaxRefreshTTL: 720 * time.Hour}
	SetAllowedEmails([]string{"example.com"})
	t.Cleanup(func() { SetAllowedEmails([]string{"example.com", "example.org"}) })

	// 1. start a long-duration grant and approve it
	rr := httptest.NewRecorder()
	DeviceCodeHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/code",
		bytes.NewBufferString(`{"duration":"long"}`)))
	if rr.Code != 200 {
		t.Fatalf("code: want 200 got %d", rr.Code)
	}
	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	json.Unmarshal(rr.Body.Bytes(), &start)

	st.ApproveDeviceCode(context.Background(), sqlc.ApproveDeviceCodeParams{
		UserCode: start.UserCode, Email: strptr("dev@example.com"),
	})

	// 2. poll to mint the initial token pair
	rr = httptest.NewRecorder()
	DeviceTokenHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/token",
		bytes.NewBufferString(`{"device_code":"`+start.DeviceCode+`"}`)))
	if rr.Code != 200 {
		t.Fatalf("mint: want 200 got %d body=%s", rr.Code, rr.Body)
	}
	var first struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(rr.Body.Bytes(), &first)
	if first.RefreshToken == "" {
		t.Fatal("long-duration flow must emit refresh_token")
	}
	oldRefresh := first.RefreshToken

	// 3. use the refresh token — must succeed and return a new pair
	rr = httptest.NewRecorder()
	DeviceRefreshHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/refresh",
		bytes.NewBufferString(`{"refresh_token":"`+oldRefresh+`"}`)))
	if rr.Code != 200 {
		t.Fatalf("first refresh: want 200 got %d body=%s", rr.Code, rr.Body)
	}
	var second struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(rr.Body.Bytes(), &second)
	if second.RefreshToken == "" || second.RefreshToken == oldRefresh {
		t.Fatalf("refresh must rotate to a new token, got same or empty")
	}

	// 4. replay the OLD refresh token — must be rejected (JTI mismatch)
	rr = httptest.NewRecorder()
	DeviceRefreshHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/refresh",
		bytes.NewBufferString(`{"refresh_token":"`+oldRefresh+`"}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("stale refresh replay: want 401 got %d body=%s", rr.Code, rr.Body)
	}

	// 5. the new refresh token must still work (family is not revoked)
	rr = httptest.NewRecorder()
	DeviceRefreshHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/refresh",
		bytes.NewBufferString(`{"refresh_token":"`+second.RefreshToken+`"}`)))
	if rr.Code != 200 {
		t.Fatalf("new refresh: want 200 got %d body=%s", rr.Code, rr.Body)
	}
}

// TestDeviceExpiredPendingGrant verifies that polling an expired-but-still-pending
// grant returns 400 expired_token rather than 428 authorization_pending.
// Intent: an expired grant must not keep the agent polling forever.
func TestDeviceExpiredPendingGrant(t *testing.T) {
	st := newFakeStore()
	cfg := DeviceConfig{Store: st, Signer: NewJWTSigner([]byte("k")), BaseURL: "https://arti.example",
		AccessTTL: time.Hour, MaxRefreshTTL: 720 * time.Hour}
	SetAllowedEmails([]string{"example.com"})
	t.Cleanup(func() { SetAllowedEmails([]string{"example.com", "example.org"}) })

	// Insert a device code that is already expired (ExpiresAt in the past).
	dc := "test-expired-device-code"
	uc := "ABCD-EFGH"
	st.auths[dc] = sqlc.DeviceAuth{
		DeviceCode: dc,
		UserCode:   uc,
		Duration:   "short",
		Status:     "pending",
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true},
	}
	st.byUser[uc] = dc

	rr := httptest.NewRecorder()
	DeviceTokenHandler(cfg)(rr, httptest.NewRequest("POST", "/auth/device/token",
		bytes.NewBufferString(`{"device_code":"`+dc+`"}`)))
	if rr.Code != 400 {
		t.Fatalf("expired pending grant: want 400 got %d body=%s", rr.Code, rr.Body)
	}
}
