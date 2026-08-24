package httperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientGone(t *testing.T) {
	live := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil)
	}
	withCtx := func(ctx context.Context) *http.Request {
		return live().WithContext(ctx)
	}
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	// A context that has already passed its deadline, which is the shape chi's
	// 60s Timeout leaves on r inside a handler that ran too long.
	expired := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}

	for _, tc := range []struct {
		name string
		req  *http.Request
		err  error
		want bool
		why  string
	}{
		{"cancelled query on live request", live(), context.Canceled, true,
			"the caller left; the query reports it before the request does"},
		{"cancellation wrapped by pgstore", live(),
			fmt.Errorf("pgstore: aggregates scope: %w", context.Canceled), true,
			"the store wraps its errors, so the check must unwrap"},
		{"other error on a cancelled request", withCtx(cancelled()), errors.New("conn closed"), true,
			"a finished query can fail on its own while the connection is already gone"},
		{"request only, no error", withCtx(cancelled()), nil, true,
			"reqLogger has no error to inspect, only the request"},
		{"server-side deadline", live(), context.DeadlineExceeded, false,
			"a timeout is a real fault and must stay in the error stream"},
		{"expired handler context", withCtx(expired()), errors.New("upstream: i/o timeout"), false,
			"chi's Timeout context must not make every slow request the client's fault"},
		{"deadline wrapped by the mcp client", live(),
			fmt.Errorf("upstream: %w", context.DeadlineExceeded), false,
			"wrapping must not launder a deadline into a cancellation"},
		{"ordinary failure on a live request", live(), errors.New("relation does not exist"), false,
			"a genuine 500 must be logged and answered as one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientGone(tc.req, tc.err); got != tc.want {
				t.Errorf("ClientGone = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}
