package auth

import "net/http"

// spoofableIPHeaders are the client-IP headers this deployment's edge does NOT
// manage, so anything arriving in them was written by the caller.
//
// Measured against staging on 2026-08-23 by sending each header from outside
// the network and reading back what the server recorded:
//
//	X-Forwarded-For: 203.0.113.9    → recorded the REAL client IP  (edge rewrites it)
//	X-Real-IP:       203.0.113.77   → recorded the REAL client IP  (edge rewrites it)
//	True-Client-IP:  198.18.0.99    → recorded 198.18.0.99         (passed through)
//
// chi's RealIP consults True-Client-IP FIRST, so that one header let any caller
// choose the address used for rate-limit bucketing and written to audit rows.
// Stripping it makes RealIP fall through to the edge-managed X-Forwarded-For.
//
// Only the header shown to be unmanaged is stripped. X-Forwarded-For is left
// alone deliberately: it is what carries the true client address here, and
// removing it would leave RealIP with nothing but the load balancer's own
// address — which is the failure this middleware exists to prevent.
var spoofableIPHeaders = []string{"True-Client-IP"}

// StripSpoofableIPHeaders removes client-supplied client-IP headers before
// anything downstream reads them. Register it FIRST, ahead of chi's RealIP.
func StripSpoofableIPHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range spoofableIPHeaders {
			r.Header.Del(h)
		}
		next.ServeHTTP(w, r)
	})
}
