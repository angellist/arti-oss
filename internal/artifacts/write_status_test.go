package artifacts

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Three front doors answer from this table now — the REST create handlers, the
// append handler, and the apps proxy running a write in-process. A row missing
// here does not fail loudly: the caller gets 500, and the apps proxy turns that
// into 502, so a lost append race reads as an upstream outage and a retrying
// app gives up instead of retrying.
func TestWriteStatusCoversEveryWriteFailure(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"bad request", errBadRequest("no content"), http.StatusBadRequest, "bad-request"},
		{"forbidden", errForbidden("not yours"), http.StatusForbidden, "forbidden"},
		{"slug conflict", slugConflict{slug: "s"}, http.StatusConflict, "slug-exists"},
		{"stale base version", staleBaseVersion{slug: "s", expected: 2}, http.StatusConflict, "stale-base-version"},
		{"type change", typeChange{}, http.StatusConflict, "type-change"},
		{"idempotency stale", idempotencyStale{}, http.StatusGone, "idempotency-stale"},
		{"append lost the race", pgstore.ErrConflict, http.StatusConflict, "conflict"},
		{"not found", pgstore.ErrNotFound, http.StatusNotFound, "not-found"},
		{"anything else", errors.New("boom"), http.StatusInternalServerError, "internal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, code, _ := WriteStatus(c.err)
			if status != c.status || code != c.code {
				t.Errorf("WriteStatus(%v) = %d/%q, want %d/%q", c.err, status, code, c.status, c.code)
			}
			// Every caller wraps: Create wraps its slug read, Append wraps its
			// retry loop. A table matching only bare errors would answer 500
			// for the wrapped form of the same failure.
			wrapped := fmt.Errorf("create: %w", c.err)
			if status, code, _ := WriteStatus(wrapped); status != c.status || code != c.code {
				t.Errorf("WriteStatus(wrapped %v) = %d/%q, want %d/%q", c.err, status, code, c.status, c.code)
			}
		})
	}
}
