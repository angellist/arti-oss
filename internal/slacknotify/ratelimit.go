package slacknotify

import (
	"context"
	"time"
)

// tokenBucket is a minimal global rate limiter: it hands out up to `burst`
// tokens immediately, then refills at `perSec` tokens/second. wait blocks until
// a token is free (or ctx is done). One background refiller goroutine runs for
// the bucket's lifetime — fine, since the Notifier (and thus its bucket) is a
// process-lifetime singleton.
//
// It smooths bursts of outbound Slack calls so a flurry of comment events
// doesn't thunder into the API (and into repeated 429→retry loops). It does NOT
// enforce Slack's per-channel postMessage limit — the client's Retry-After-aware
// retry is the backstop for that.
type tokenBucket struct {
	tokens chan struct{}
}

func newTokenBucket(perSec float64, burst int) *tokenBucket {
	tb := &tokenBucket{tokens: make(chan struct{}, burst)}
	for i := 0; i < burst; i++ {
		tb.tokens <- struct{}{}
	}
	interval := time.Duration(float64(time.Second) / perSec)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			select {
			case tb.tokens <- struct{}{}: // refill one
			default: // bucket full — drop
			}
		}
	}()
	return tb
}

func (tb *tokenBucket) wait(ctx context.Context) error {
	select {
	case <-tb.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
