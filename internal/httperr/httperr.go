// Package httperr holds the one definition of "this request failed only
// because the caller went away", shared by every arti HTTP surface. It exists
// as its own package because internal/artifacts and internal/apps both need it
// and neither imports the other, and because two copies of this predicate had
// already drifted apart the first time it was written.
package httperr

import (
	"context"
	"errors"
	"net/http"
)

// StatusClientClosedRequest is nginx's 499: the caller went away before the
// response was written. net/http has no constant for it because it is not
// IANA-registered, but it is the conventional way to record the case, and it
// keeps `s>=500` in the access log meaning "the server broke".
const StatusClientClosedRequest = 499

// ClientGone reports whether err means the caller simply left — a browser
// navigating away, or React unmounting a component whose fetch is in flight.
// Such a request cancels its context, so the in-flight query fails with
// context.Canceled; answering 500 there reports a server fault that did not
// happen, and nothing is left reading the response anyway.
//
// Pass a nil err to ask only about the request itself.
//
// A DEADLINE is deliberately excluded. Inside a handler r.Context() is chi's
// 60s Timeout context, not the connection's, so a plain `r.Context().Err() !=
// nil` check would classify every server-side timeout as the client's fault
// and drop it from the error stream — and those timeouts are the signal that
// names a slow dependency. Only cancellation counts.
func ClientGone(r *http.Request, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled)
}
