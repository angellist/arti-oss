// Package credusage records where each credential is used from.
//
// It answers two questions that `artifacts.creator` cannot: where is this
// credential being used, and has it just appeared somewhere it has never been
// used before. The second is the one that catches a bearer that has been
// copied out of the place it was issued for.
//
// Requests are counted in memory and flushed on a timer, so a busy read path
// costs no extra database round trip per request. The cost of that choice is
// that a pod losing its process drops at most one flush interval of counts.
package credusage

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/auth"
)

// maxUserAgent bounds what a caller can write into a primary-key column.
const maxUserAgent = 200

// maxSourcesPerCredential bounds how many distinct sources one credential can
// add per interval. Truncating the user agent bounds a row's width, not how
// many rows a caller can create: the agent string is caller-chosen, so varying
// it once per request would otherwise mint a row and an alert per request.
// Beyond this many, a credential's further sources collapse onto one row per
// address, which keeps the address — the part an attacker cannot cheaply vary.
const maxSourcesPerCredential = 20

// variedSource stands in for the collapsed agents above the cap, so a reader
// can tell "many clients from this address" from a single named one.
const variedSource = "(varied)"

// maxAlertsPerFlush caps the DMs one flush can send. Flooding an owner with
// alerts is a way to bury the one that matters. Nothing is lost by the cap:
// the claim that marks an alert delivered is only taken when it is being sent,
// so a held-back network is alerted on a later flush.
const maxAlertsPerFlush = 5

// alertableKind is the credential kind an owner is DM'd about. A key is the
// credential that gets copied into another system and used by other people; a
// personal bearer or a browser session moving between networks is the owner
// themselves, and telling them about it is noise they learn to ignore.
const alertableKind = auth.CredKindAPIKey + ":"

// retentionDays is how long a usage row is kept. The records answer "where has
// this credential been used lately"; older rows are cost, and forged ones
// should not persist.
const retentionDays = 90

// pruneEvery is how often the retention delete runs.
const pruneEvery = time.Hour

// FlushInterval is how long counts sit in memory before they are written.
// Short enough that the new-source alert is close to live, long enough that a
// read-heavy minute is one write rather than thousands.
const FlushInterval = 30 * time.Second

type store interface {
	UpsertCredentialUsage(ctx context.Context, arg sqlc.UpsertCredentialUsageParams) error
	ClaimCredentialAlert(ctx context.Context, arg sqlc.ClaimCredentialAlertParams) (pgtype.Timestamptz, error)
	DeleteCredentialUsageBefore(ctx context.Context, day pgtype.Date) (int64, error)
}

// NewSource describes an API key being used from a network it has not been used
// from before.
type NewSource struct {
	OwnerEmail string
	Cred       string // auth.Credential.Ref
	CredName   string // the owner's name for the key
	Network    string // the address group the claim is keyed on
	IP         string
	UserAgent  string
	Reads      int64
	Writes     int64
}

type key struct {
	cred, credName, owner, ip, userAgent string
	day                                  time.Time
}

type counts struct{ reads, writes int64 }

// Recorder counts authenticated requests per credential and source.
type Recorder struct {
	store  store
	alert  func(context.Context, NewSource)
	logger *slog.Logger
	// alertsEnabled reports whether an alert to this owner would actually be
	// delivered. It has to be asked BEFORE the claim: the claim records that an
	// owner was told, so taking one for a message the notifier then drops would
	// spend the only chance to tell them. nil means "assume yes".
	alertsEnabled func(ctx context.Context, owner string) bool

	mu  sync.Mutex
	buf map[key]*counts

	lastPrune time.Time
}

// New returns a Recorder. alert may be nil, which disables new-source
// notification but keeps the usage records.
func New(s store, alert func(context.Context, NewSource), logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{store: s, alert: alert, logger: logger, buf: map[key]*counts{}}
}

// WithAlertGate installs the check for whether alerts are currently deliverable
// — in production, the admin switch. Without it a Recorder assumes they are.
func (r *Recorder) WithAlertGate(enabled func(ctx context.Context, owner string) bool) *Recorder {
	r.alertsEnabled = enabled
	return r
}

// mayAlert reports whether this owner is currently accepting the alert.
func (r *Recorder) mayAlert(ctx context.Context, owner string) bool {
	return r.alertsEnabled == nil || r.alertsEnabled(ctx, owner)
}

// Middleware counts every request that carries a credential. It must be
// registered AFTER the authentication middleware, which is what puts the
// credential in the request context.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(w, req)
		r.record(req)
	})
}

func (r *Recorder) record(req *http.Request) {
	cred := auth.CredentialFromContext(req.Context())
	ref := cred.Ref()
	email := auth.EmailFromContext(req.Context())
	if ref == "" || email == "" {
		return // unauthenticated route, or a request that never got past auth
	}
	ua := req.UserAgent()
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	k := key{
		cred:      ref,
		credName:  cred.Label,
		owner:     email,
		ip:        clientIP(req),
		userAgent: ua,
		day:       time.Now().UTC().Truncate(24 * time.Hour),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.buf[k]
	if c == nil {
		if r.sourcesFor(k) >= maxSourcesPerCredential {
			k.userAgent = variedSource
			c = r.buf[k]
		}
		if c == nil {
			c = &counts{}
			r.buf[k] = c
		}
	}
	if isWrite(req.Method) {
		c.writes++
	} else {
		c.reads++
	}
}

// sourcesFor counts the distinct sources already buffered for a key's
// credential and owner. Called with the lock held.
func (r *Recorder) sourcesFor(k key) int {
	n := 0
	for existing := range r.buf {
		if existing.cred == k.cred && existing.owner == k.owner {
			n++
		}
	}
	return n
}

// Run flushes on a timer until ctx is cancelled, then flushes once more so a
// clean shutdown does not drop the last interval.
func (r *Recorder) Run(ctx context.Context) {
	t := time.NewTicker(FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// The flush needs a live context of its own: ctx is already done.
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r.Flush(fctx)
			cancel()
			return
		case <-t.C:
			r.Flush(ctx)
			r.pruneDue(ctx)
		}
	}
}

// Flush writes the buffered counts and alerts on sources not seen before. It
// takes the buffer under the lock and releases it before touching the
// database, so request handling never waits on a flush.
func (r *Recorder) Flush(ctx context.Context) {
	r.mu.Lock()
	buf := r.buf
	r.buf = map[key]*counts{}
	r.mu.Unlock()

	alerts := 0
	for k, c := range buf {
		if err := r.store.UpsertCredentialUsage(ctx, sqlc.UpsertCredentialUsageParams{
			Cred:       k.cred,
			Day:        pgtype.Date{Time: k.day, Valid: true},
			Ip:         k.ip,
			UserAgent:  k.userAgent,
			OwnerEmail: k.owner,
			Reads:      c.reads,
			Writes:     c.writes,
		}); err != nil {
			r.logger.Warn("credential usage flush failed", "cred", k.cred, "err", err)
			continue
		}
		// Everything above is recorded for every credential; only a key is
		// worth interrupting someone about.
		if r.alert == nil || !strings.HasPrefix(k.cred, alertableKind) {
			continue
		}
		// Asked before the claim. With the category switched off the notifier
		// would drop the message, and a claim taken for a dropped message can
		// never be taken again — so switching the category on later would find
		// every network already "told" and say nothing.
		if !r.mayAlert(ctx, k.owner) {
			continue
		}
		if alerts >= maxAlertsPerFlush {
			// Deliberately before the claim, so this network is still
			// unclaimed and gets its alert on a later flush.
			r.logger.Warn("new-network alerts deferred to a later interval",
				"cred", k.cred, "owner", k.owner, "cap", maxAlertsPerFlush)
			continue
		}
		network := networkOf(k.ip)
		if _, err := r.store.ClaimCredentialAlert(ctx, sqlc.ClaimCredentialAlertParams{
			Cred: k.cred, OwnerEmail: k.owner, Network: network,
		}); err != nil {
			// pgx.ErrNoRows means another pod claimed it, or the owner has
			// already been told. Anything else is a real failure, and losing
			// the claim means the alert is retried rather than dropped.
			if !errors.Is(err, pgx.ErrNoRows) {
				r.logger.Warn("credential alert claim failed", "cred", k.cred, "err", err)
			}
			continue
		}
		alerts++
		r.logger.Info("key used from a new network",
			"cred", k.cred, "owner", k.owner, "network", network)
		// Sent off the flush path: delivery is a Slack round trip with its own
		// retries, and a burst of new sources must not stall the writes behind
		// it. ctx belongs to the flush, so the send gets its own.
		ev := NewSource{
			OwnerEmail: k.owner,
			Cred:       k.cred,
			CredName:   k.credName,
			Network:    network,
			IP:         k.ip,
			UserAgent:  k.userAgent,
			Reads:      c.reads,
			Writes:     c.writes,
		}
		go func() {
			actx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r.alert(actx, ev)
		}()
	}
}

// pruneDue drops usage rows past the retention window, at most once an hour.
// Every pod runs it; the delete is idempotent and cheap after the first.
func (r *Recorder) pruneDue(ctx context.Context) {
	if time.Since(r.lastPrune) < pruneEvery {
		return
	}
	r.lastPrune = time.Now()
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	n, err := r.store.DeleteCredentialUsageBefore(ctx, pgtype.Date{Time: cutoff, Valid: true})
	if err != nil {
		r.logger.Warn("credential usage prune failed", "err", err)
		return
	}
	if n > 0 {
		r.logger.Info("credential usage pruned", "rows", n, "older_than_days", retentionDays)
	}
}

func isWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// networkOf groups an address with its neighbours: /24 for IPv4, /64 for IPv6.
// Alerting on the exact address means alerting on IPv6 privacy addressing and
// on every DHCP lease, which is churn the owner cannot act on. The network is
// the part that changes when a credential actually moves somewhere else.
func networkOf(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip // unparseable: group it with itself rather than with everything
	}
	bits := 24
	if addr.Is6() && !addr.Is4In6() {
		bits = 64
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return ip
	}
	return prefix.String()
}

// clientIP reads the address chi's RealIP resolved from the edge-managed
// forwarded headers (see auth.StripSpoofableIPHeaders for why that is the
// trustworthy one here). The TCP peer is deliberately not used: behind the
// load balancer it is an ingress pod and cannot tell clients apart.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr // already bare (no port), including bare IPv6
}
