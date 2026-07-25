package pgstore

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	authzInvalidationChannel = "arti_authz_invalidate"
	rbacInvalidationKind     = "rbac"
	groupsInvalidationKind   = "groups"
)

// invalidationBus keeps the small authorization snapshots coherent across
// arti-server replicas. It deliberately uses a dedicated pgx connection rather
// than occupying a pool slot forever: LISTEN connections are long-lived, while
// ordinary store work should continue to use the pool normally.
//
// Health is fail-closed. A snapshot may be served only while this connection
// has successfully LISTENed and is still receiving notifications. If the
// connection drops, reads go to Postgres until the reconnect succeeds; the
// existing 15-second TTL remains a backstop for notifications that are missed
// while a replica is restarting.
type invalidationBus struct {
	store  *Store
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	connMu     sync.Mutex
	connCancel context.CancelFunc
}

// StartInvalidationBus starts the cross-replica authorization invalidation
// listener. The returned function stops it and waits for its dedicated
// connection and goroutine to exit. The listener reconnects indefinitely with
// bounded exponential backoff; startup and reconnect failures therefore do not
// prevent the server from serving fail-closed DB-backed authorization checks.
func (s *Store) StartInvalidationBus(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	bus := &invalidationBus{
		store:  s,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	s.busMu.Lock()
	s.busHealthy.Store(false)
	s.bus = bus
	s.busMu.Unlock()
	go bus.run()
	return func() {
		cancel()
		<-bus.done
		s.busMu.Lock()
		if s.bus == bus {
			s.bus = nil
		}
		s.busMu.Unlock()
	}
}

// InvalidationBusHealthy reports whether this Store currently has a live,
// successfully LISTENed invalidation connection. It is primarily useful for
// readiness diagnostics and integration tests; authorization reads use the
// same cheap health check internally.
func (s *Store) InvalidationBusHealthy() bool {
	return s.authzCacheHealthy()
}

func (b *invalidationBus) run() {
	defer close(b.done)
	defer b.store.busHealthy.Store(false)

	const (
		initialBackoff = 100 * time.Millisecond
		maxBackoff     = 5 * time.Second
	)
	backoff := initialBackoff
	for {
		if b.ctx.Err() != nil {
			return
		}
		conn, err := pgx.ConnectConfig(b.ctx, b.store.pool.Config().ConnConfig.Copy())
		if err == nil {
			_, err = conn.Exec(b.ctx, "LISTEN "+authzInvalidationChannel)
		}
		if err != nil {
			if conn != nil {
				_ = conn.Close(context.Background())
			}
			b.markUnhealthy()
			slog.Default().Warn("pgstore authz invalidation listener unavailable", "err", err)
			if !waitInvalidationBackoff(b.ctx, backoff) {
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}

		b.store.busHealthy.Store(true)
		backoff = initialBackoff
		connCtx, cancelConn := context.WithCancel(b.ctx)
		b.connMu.Lock()
		b.connCancel = cancelConn
		b.connMu.Unlock()
		slog.Default().Info("pgstore authz invalidation listener connected")
		for {
			notification, err := conn.WaitForNotification(connCtx)
			if err != nil {
				cancelConn()
				b.connMu.Lock()
				b.connCancel = nil
				b.connMu.Unlock()
				_ = conn.Close(context.Background())
				b.markUnhealthy()
				if b.ctx.Err() != nil {
					return
				}
				slog.Default().Warn("pgstore authz invalidation listener disconnected", "err", err)
				break
			}
			switch notification.Payload {
			case rbacInvalidationKind:
				b.store.rbac.invalidate()
			case groupsInvalidationKind:
				b.store.groups.invalidate()
			default:
				// Unknown payloads invalidate both caches so a future producer
				// cannot accidentally leave one authorization snapshot stale.
				b.store.rbac.invalidate()
				b.store.groups.invalidate()
			}
		}
	}
}

func waitInvalidationBackoff(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Store) authzCacheHealthy() bool {
	return s.busHealthy.Load()
}

func (s *Store) invalidateRBAC(ctx context.Context) {
	s.rbac.invalidate()
	s.publishInvalidation(ctx, rbacInvalidationKind)
}

func (s *Store) invalidateGroups(ctx context.Context) {
	s.groups.invalidate()
	s.publishInvalidation(ctx, groupsInvalidationKind)
}

func (s *Store) publishInvalidation(ctx context.Context, kind string) {
	// The write has committed before this helper runs, so the notification
	// must outlive a canceled client request to avoid reopening the TTL window.
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(notifyCtx, "SELECT pg_notify($1, $2)", authzInvalidationChannel, kind); err != nil {
		s.busMu.RLock()
		bus := s.bus
		s.busMu.RUnlock()
		if bus != nil {
			bus.markUnhealthy()
		}
		slog.Default().Warn("pgstore authz invalidation broadcast failed", "kind", kind, "err", err)
	}
}

func (b *invalidationBus) markUnhealthy() {
	b.store.busHealthy.Store(false)
	b.connMu.Lock()
	cancelConn := b.connCancel
	b.connMu.Unlock()
	if cancelConn != nil {
		cancelConn()
	}
	// Do not let a snapshot loaded while the bus was disconnected become
	// eligible immediately after reconnect. Reads during the outage bypass the
	// cache, but a DB change can still race with one of those reads.
	b.store.rbac.invalidate()
	b.store.groups.invalidate()
}
