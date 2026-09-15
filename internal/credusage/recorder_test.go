package credusage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
)

type fakeStore struct {
	mu       sync.Mutex
	upserts  []sqlc.UpsertCredentialUsageParams
	claimed  map[string]bool
	claimErr error
	pruned   []pgtype.Date
}

func (f *fakeStore) DeleteCredentialUsageBefore(_ context.Context, day pgtype.Date) (int64, error) {
	f.pruned = append(f.pruned, day)
	return 0, nil
}

func (f *fakeStore) UpsertCredentialUsage(_ context.Context, arg sqlc.UpsertCredentialUsageParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, arg)
	return nil
}

// ClaimCredentialAlert mirrors the real INSERT … ON CONFLICT DO NOTHING: the
// first caller for a (cred, owner, network) wins, later ones get pgx.ErrNoRows.
func (f *fakeStore) ClaimCredentialAlert(_ context.Context, arg sqlc.ClaimCredentialAlertParams) (pgtype.Timestamptz, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return pgtype.Timestamptz{}, f.claimErr
	}
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	k := arg.Cred + "|" + arg.OwnerEmail + "|" + arg.Network
	if f.claimed[k] {
		return pgtype.Timestamptz{}, pgx.ErrNoRows
	}
	f.claimed[k] = true
	return pgtype.Timestamptz{Time: time.Now(), Valid: true}, nil
}

// claims reports how many alerts were claimed, for the cap assertions.
func (f *fakeStore) claims() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.claimed)
}

// alertSink collects alerts safely: they are delivered from their own
// goroutine now, so a plain slice append would race the assertions.
type alertSink struct {
	mu  sync.Mutex
	got []NewSource
}

func (a *alertSink) fn(_ context.Context, ev NewSource) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, ev)
}

func (a *alertSink) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.got)
}

// await waits for n alerts, then keeps watching briefly so an unexpected extra
// one still fails the test rather than arriving after it passes.
func (a *alertSink) await(t *testing.T, n int) []NewSource {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for a.count() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]NewSource(nil), a.got...)
}

// request builds an authenticated request as the auth middleware would leave it.
func request(method, ip, ua string, cred auth.Credential) *http.Request {
	r := httptest.NewRequest(method, "/api/artifacts", nil)
	// An IPv6 address needs brackets before a port; "addr:port" without them
	// is not a parseable host and would silently defeat network grouping.
	if strings.Contains(ip, ":") {
		r.RemoteAddr = "[" + ip + "]:54321"
	} else {
		r.RemoteAddr = ip + ":54321"
	}
	r.Header.Set("User-Agent", ua)
	ctx := auth.WithIdentity(r.Context(), "owner@example.com")
	ctx = auth.WithTestCredential(ctx, cred)
	return r.WithContext(ctx)
}

func serve(t *testing.T, rec *Recorder, r *http.Request) {
	t.Helper()
	rec.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), r)
}

var apiKeyCred = auth.Credential{Kind: auth.CredKindAPIKey, ID: "k1", Label: "ci-upload", Source: auth.CredSourceBearer}

// Counting per source is the whole point: one credential used from two places
// has to produce two rows, or "where is this key used" has no answer.
func TestFlushSeparatesSourcesAndCountsReadsAndWrites(t *testing.T) {
	st := &fakeStore{}
	rec := New(st, nil, nil)

	serve(t, rec, request(http.MethodGet, "10.0.0.1", "arti-cli/1", apiKeyCred))
	serve(t, rec, request(http.MethodGet, "10.0.0.1", "arti-cli/1", apiKeyCred))
	serve(t, rec, request(http.MethodPost, "10.0.0.1", "arti-cli/1", apiKeyCred))
	serve(t, rec, request(http.MethodGet, "34.72.0.9", "python-requests/2", apiKeyCred))
	rec.Flush(context.Background())

	if len(st.upserts) != 2 {
		t.Fatalf("upserts = %d, want one row per source", len(st.upserts))
	}
	for _, u := range st.upserts {
		switch u.Ip {
		case "10.0.0.1":
			if u.Reads != 2 || u.Writes != 1 {
				t.Errorf("10.0.0.1: reads/writes = %d/%d, want 2/1", u.Reads, u.Writes)
			}
		case "34.72.0.9":
			if u.Reads != 1 || u.Writes != 0 {
				t.Errorf("34.72.0.9: reads/writes = %d/%d, want 1/0", u.Reads, u.Writes)
			}
		default:
			t.Errorf("unexpected source %q", u.Ip)
		}
		if u.Cred != "apikey:k1" || u.OwnerEmail != "owner@example.com" {
			t.Errorf("row attributed to %q/%q", u.Cred, u.OwnerEmail)
		}
	}
}

// The alert exists to be noticed once. Firing again on every later flush from
// the same place would train its recipient to ignore it.
func TestAlertsOncePerNewSource(t *testing.T) {
	st := &fakeStore{}
	sink := &alertSink{}
	rec := New(st, sink.fn, nil)

	serve(t, rec, request(http.MethodPost, "34.72.0.9", "python-requests/2", apiKeyCred))
	rec.Flush(context.Background())
	serve(t, rec, request(http.MethodGet, "34.72.0.9", "python-requests/2", apiKeyCred))
	rec.Flush(context.Background())

	alerts := sink.await(t, 1)
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want exactly one for a new source", len(alerts))
	}
	if alerts[0].CredName != "ci-upload" || alerts[0].IP != "34.72.0.9" {
		t.Errorf("alert = %+v, want the key's name and the new address", alerts[0])
	}
}

// A failed claim must not send: the claim IS the record that the owner was
// told, so sending without it would DM them again every interval.
func TestFailedClaimDoesNotAlert(t *testing.T) {
	st := &fakeStore{claimErr: errors.New("connection refused")}
	sink := &alertSink{}
	rec := New(st, sink.fn, nil)

	serve(t, rec, request(http.MethodPost, "34.72.0.9", "python-requests/2", apiKeyCred))
	rec.Flush(context.Background())

	if alerts := sink.await(t, 0); len(alerts) != 0 {
		t.Fatalf("alerts = %d, want none while the claim is failing", len(alerts))
	}
	if len(st.upserts) != 1 {
		t.Fatalf("upserts = %d, want the counts recorded anyway", len(st.upserts))
	}
}

// A request with no credential (an unauthenticated route) has nothing to
// attribute, and must not create a row keyed on an empty credential.
func TestUnauthenticatedRequestIsNotRecorded(t *testing.T) {
	st := &fakeStore{}
	rec := New(st, nil, nil)

	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	serve(t, rec, r)
	rec.Flush(context.Background())

	if len(st.upserts) != 0 {
		t.Fatalf("upserts = %d, want none", len(st.upserts))
	}
}

// user_agent is part of the primary key, so a caller must not be able to
// choose how wide that column gets.
func TestUserAgentIsTruncated(t *testing.T) {
	st := &fakeStore{}
	rec := New(st, nil, nil)

	serve(t, rec, request(http.MethodGet, "10.0.0.1", strings.Repeat("x", maxUserAgent+50), apiKeyCred))
	rec.Flush(context.Background())

	if len(st.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(st.upserts))
	}
	if got := len(st.upserts[0].UserAgent); got != maxUserAgent {
		t.Errorf("user agent length = %d, want %d", got, maxUserAgent)
	}
}

// `session`, `token` and `service` are the same reference for everyone, so two
// people behind one address and client must still be two rows — otherwise the
// second person vanishes from their own usage view and is never alerted.
func TestSharedCredentialReferenceStaysPerOwner(t *testing.T) {
	st := &fakeStore{}
	sink := &alertSink{}
	rec := New(st, sink.fn, nil)

	sessionCred := auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie}
	for _, email := range []string{"alice@example.com", "bob@example.com"} {
		r := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
		r.RemoteAddr = "10.0.0.1:443"
		r.Header.Set("User-Agent", "Mozilla/5.0")
		ctx := auth.WithIdentity(r.Context(), email)
		serve(t, rec, r.WithContext(auth.WithTestCredential(ctx, sessionCred)))
	}
	rec.Flush(context.Background())

	if len(st.upserts) != 2 {
		t.Fatalf("upserts = %d, want one row per person", len(st.upserts))
	}
	owners := map[string]bool{}
	for _, u := range st.upserts {
		owners[u.OwnerEmail] = true
	}
	if !owners["alice@example.com"] || !owners["bob@example.com"] {
		t.Errorf("rows recorded for %v, want both people", owners)
	}
	// A browser session is not a key, so neither person is DM'd — the rows
	// above are what they see in Settings.
	if alerts := sink.await(t, 0); len(alerts) != 0 {
		t.Errorf("alerts = %d, want none for a session credential", len(alerts))
	}
}

// The user agent is caller-chosen, so varying it per request would otherwise
// mint a row and an alert per request — turning a stolen key's own traffic into
// cover noise. Past the cap, sources collapse onto the address, which an
// attacker cannot vary as cheaply.
func TestVariedUserAgentsCollapseAboveTheCap(t *testing.T) {
	st := &fakeStore{}
	sink := &alertSink{}
	rec := New(st, sink.fn, nil)

	for i := 0; i < maxSourcesPerCredential*3; i++ {
		serve(t, rec, request(http.MethodGet, "34.72.0.9", fmt.Sprintf("forged/%d", i), apiKeyCred))
	}
	rec.Flush(context.Background())

	if len(st.upserts) > maxSourcesPerCredential+1 {
		t.Fatalf("upserts = %d, want at most the cap plus the collapsed row", len(st.upserts))
	}
	var collapsed int64
	for _, u := range st.upserts {
		if u.UserAgent == variedSource {
			collapsed = u.Reads
		}
	}
	if collapsed == 0 {
		t.Fatal("no collapsed row: the requests past the cap were dropped or spread")
	}
	if got := sink.await(t, 0); len(got) > maxAlertsPerFlush {
		t.Errorf("alerts = %d, want no more than %d in one flush", len(got), maxAlertsPerFlush)
	}
}

// Retention runs from the flush loop, so a table nobody prunes cannot outlive
// the window its own queries read.
func TestPruneRunsOnceAnHour(t *testing.T) {
	st := &fakeStore{}
	rec := New(st, nil, nil)

	rec.pruneDue(context.Background())
	rec.pruneDue(context.Background())

	if len(st.pruned) != 1 {
		t.Fatalf("prunes = %d, want one — the second call is inside the interval", len(st.pruned))
	}
	cutoff := st.pruned[0].Time
	if age := time.Since(cutoff).Hours() / 24; age < retentionDays-1 || age > retentionDays+1 {
		t.Errorf("cutoff is %.0f days old, want about %d", age, retentionDays)
	}
}

// The change that made this whole path usable: one afternoon of real traffic
// sent 62 DMs to 15 people, none of them about a key. A personal bearer, a
// browser session and the service credential are recorded and shown in
// Settings; only a key interrupts anyone.
func TestOnlyKeysAlert(t *testing.T) {
	for _, cred := range []auth.Credential{
		{Kind: auth.CredKindToken, Source: auth.CredSourceBearer},
		{Kind: auth.CredKindSession, Source: auth.CredSourceCookie},
		{Kind: auth.CredKindDevice, ID: "fam-1", Source: auth.CredSourceBearer},
		{Kind: auth.CredKindService},
	} {
		st := &fakeStore{}
		sink := &alertSink{}
		rec := New(st, sink.fn, nil)

		serve(t, rec, request(http.MethodPost, "34.72.0.9", "python-requests/2", cred))
		rec.Flush(context.Background())

		if got := sink.await(t, 0); len(got) != 0 {
			t.Errorf("%s alerted %d time(s); only a key should", cred.Kind, len(got))
		}
		if len(st.upserts) != 1 {
			t.Errorf("%s recorded %d rows, want 1 — everything is still recorded", cred.Kind, len(st.upserts))
		}
	}
}

// A tool that upgrades itself, or a phone that picks a new address inside its
// own /64, is not a credential moving somewhere else. Alerting on those is what
// taught people to ignore the alert.
func TestSameNetworkAlertsOnceAcrossAddressesAndAgents(t *testing.T) {
	st := &fakeStore{}
	sink := &alertSink{}
	rec := New(st, sink.fn, nil)

	for _, src := range [][2]string{
		{"2601:643:8b00:4969:d15f:d226:41ae:340b", "claude-code/2.1.252 (cli)"},
		{"2601:643:8b00:4969:aaaa:bbbb:cccc:dddd", "claude-code/2.1.261 (cli)"},
		{"2601:643:8b00:4969:1111:2222:3333:4444", "curl/8.7.1"},
	} {
		serve(t, rec, request(http.MethodPost, src[0], src[1], apiKeyCred))
	}
	rec.Flush(context.Background())

	if got := sink.await(t, 1); len(got) != 1 {
		t.Fatalf("alerts = %d, want one for the network", len(got))
	}
	if got := sink.await(t, 1)[0].Network; got != "2601:643:8b00:4969::/64" {
		t.Errorf("network = %q, want the /64", got)
	}
	if len(st.upserts) != 3 {
		t.Errorf("upserts = %d, want every source still recorded for the UI", len(st.upserts))
	}
}

// A different network is a real signal and must still arrive.
func TestDifferentNetworkAlertsAgain(t *testing.T) {
	st := &fakeStore{}
	sink := &alertSink{}
	rec := New(st, sink.fn, nil)

	serve(t, rec, request(http.MethodPost, "73.71.208.206", "python-httpx/0.28", apiKeyCred))
	rec.Flush(context.Background())
	serve(t, rec, request(http.MethodPost, "34.72.11.8", "python-httpx/0.28", apiKeyCred))
	rec.Flush(context.Background())

	got := sink.await(t, 2)
	if len(got) != 2 {
		t.Fatalf("alerts = %d, want one per network", len(got))
	}
}

// The cap must defer an alert, never swallow it: the claim is what records
// that an owner was told, so it is taken only when the DM is going out.
func TestCappedAlertsAreDeferredNotLost(t *testing.T) {
	st := &fakeStore{}
	sink := &alertSink{}
	rec := New(st, sink.fn, nil)

	for i := 0; i < maxAlertsPerFlush+3; i++ {
		serve(t, rec, request(http.MethodPost, fmt.Sprintf("34.72.%d.8", i), "python-httpx/0.28", apiKeyCred))
	}
	rec.Flush(context.Background())

	if got := sink.await(t, maxAlertsPerFlush); len(got) != maxAlertsPerFlush {
		t.Fatalf("alerts = %d, want the cap", len(got))
	}
	if st.claims() != maxAlertsPerFlush {
		t.Fatalf("claims = %d, want only the sent ones claimed", st.claims())
	}

	// The deferred networks are still unclaimed, so a later interval sends them.
	for i := 0; i < maxAlertsPerFlush+3; i++ {
		serve(t, rec, request(http.MethodPost, fmt.Sprintf("34.72.%d.8", i), "python-httpx/0.28", apiKeyCred))
	}
	rec.Flush(context.Background())

	if got := sink.await(t, maxAlertsPerFlush+3); len(got) != maxAlertsPerFlush+3 {
		t.Errorf("alerts = %d after the second flush, want all %d networks eventually told",
			len(got), maxAlertsPerFlush+3)
	}
}

// clientIP feeds network grouping, and a bare IPv6 address (what the edge
// leaves in RemoteAddr) has colons but no port. Reading it as host:port would
// mangle it and put every request in its own "network".
func TestClientIPHandlesBareAndBracketedAddresses(t *testing.T) {
	for addr, want := range map[string]string{
		"73.71.208.206:54321":         "73.71.208.206",
		"73.71.208.206":               "73.71.208.206",
		"[2601:643:8b00:4969::1]:443": "2601:643:8b00:4969::1",
		"2601:643:8b00:4969::1":       "2601:643:8b00:4969::1",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = addr
		if got := clientIP(r); got != want {
			t.Errorf("clientIP(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestNetworkOfGroupsByPrefix(t *testing.T) {
	for addr, want := range map[string]string{
		"73.71.208.206":                          "73.71.208.0/24",
		"2601:643:8b00:4969:d15f:d226:41ae:340b": "2601:643:8b00:4969::/64",
		"not-an-address":                         "not-an-address",
	} {
		if got := networkOf(addr); got != want {
			t.Errorf("networkOf(%q) = %q, want %q", addr, got, want)
		}
	}
}

// The claim records that an owner was told. While the category is switched off
// nobody is told, so nothing may be claimed — otherwise the day an admin turns
// the notification on, every network is already marked as alerted and the
// feature is silent for exactly the credentials that have been in use.
func TestDisabledAlertsSpendNoClaim(t *testing.T) {
	st := &fakeStore{}
	sink := &alertSink{}
	enabled := false
	rec := New(st, sink.fn, nil).WithAlertGate(func(context.Context, string) bool { return enabled })

	serve(t, rec, request(http.MethodPost, "34.72.11.8", "python-httpx/0.28", apiKeyCred))
	rec.Flush(context.Background())

	if got := sink.await(t, 0); len(got) != 0 {
		t.Fatalf("alerts = %d while the category is off", len(got))
	}
	if st.claims() != 0 {
		t.Fatalf("claims = %d while the category is off; a claim spent now can never alert later", st.claims())
	}
	if len(st.upserts) != 1 {
		t.Errorf("upserts = %d, want usage recorded regardless of the switch", len(st.upserts))
	}

	// Switching it on must alert for the network seen while it was off.
	enabled = true
	serve(t, rec, request(http.MethodPost, "34.72.11.8", "python-httpx/0.28", apiKeyCred))
	rec.Flush(context.Background())

	got := sink.await(t, 1)
	if len(got) != 1 {
		t.Fatalf("alerts = %d after switching on, want the network reported", len(got))
	}
	if got[0].Network != "34.72.11.0/24" {
		t.Errorf("network = %q, want the one seen while the switch was off", got[0].Network)
	}
}
