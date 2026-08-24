package auth

import (
	"context"
	"net"
	"net/http"
)

// CapturePeerAddr stashes the TCP peer address in the request context, before
// chi's RealIP overwrites r.RemoteAddr from the forwarded headers.
//
// WHAT THIS IS NOT. Behind a load balancer the TCP peer is the ingress pod,
// not the client. Measured on staging on 2026-08-23: requests from one client
// recorded three different peers (2600:1f13:360:ef03::/ef04::/ef05::, rotating
// per request) while the client never changed. So this identifies the path a
// request took through the edge, and it is useless for telling clients apart.
//
// It is therefore NOT a defence against a forged client-IP header, and must
// not be used as a rate-limit key: bucketing on it puts every client on the
// internet into a handful of shared buckets. Forged client-IP headers are
// handled where they arrive, by StripSpoofableIPHeaders.
func CapturePeerAddr(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := r.RemoteAddr
		if host, _, err := net.SplitHostPort(addr); err == nil {
			addr = host
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxPeerAddr, addr)))
	})
}

// PeerAddrFromContext returns the socket address captured by CapturePeerAddr,
// or "" when that middleware did not run.
func PeerAddrFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxPeerAddr).(string)
	return v
}
