package main

import (
	"net/http"
	"time"

	"github.com/go-chi/httprate"
)

// ipRateLimiter builds a per-client-IP rate limiter (rpm requests per minute)
// for the public, unauthenticated, DB-writing setup endpoints (M-2). It relies
// on the chimid.RealIP middleware — registered globally before any route — to
// rewrite r.RemoteAddr from X-Forwarded-For / X-Real-IP, so distinct clients
// bucket separately instead of all collapsing onto the ingress pod's IP.
//
// That rewrite is only trustworthy because auth.StripSpoofableIPHeaders runs
// ahead of RealIP and removes the one client-IP header this edge does not
// manage. Do not key this on the TCP peer address instead: behind the load
// balancer that is the ingress pod, and bucketing on it puts every client on
// the internet into a handful of shared buckets, so one caller can exhaust the
// limit for everyone.
// Over-limit requests get a 429 before the handler (and its DB insert) runs.
//
// rpm <= 0 disables the limiter, matching the "0 = no cap" convention used for
// the LLM budgets in this config — an env-only escape hatch for ops.
func ipRateLimiter(rpm int) func(http.Handler) http.Handler {
	if rpm <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return httprate.LimitByIP(rpm, time.Minute)
}
