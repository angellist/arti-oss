package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The exempt path must see no deadline from the blanket timeout; every other
// path must.
func TestTimeoutExceptSparesOnePath(t *testing.T) {
	mw := timeoutExcept(20*time.Millisecond, "/api/apps/mcp")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); ok {
			w.WriteHeader(http.StatusRequestTimeout)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	for path, want := range map[string]int{"/api/apps/mcp": http.StatusOK, "/api/artifacts": http.StatusRequestTimeout} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil).WithContext(context.Background()))
		if rr.Code != want {
			t.Errorf("%s: status = %d, want %d", path, rr.Code, want)
		}
	}
}
