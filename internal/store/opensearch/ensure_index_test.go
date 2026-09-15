package opensearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The domain runs a single data node, so a replica can never be allocated
// and leaves the cluster yellow. EnsureIndex must converge the replica count
// on an index that already exists, not only on a fresh create.
func TestEnsureIndex_ReplicaCount(t *testing.T) {
	type call struct {
		method, path string
		body         map[string]any
	}
	serve := func(exists bool, calls *[]call) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c := call{method: r.Method, path: r.URL.Path}
			_ = json.NewDecoder(r.Body).Decode(&c.body)
			*calls = append(*calls, c)
			if r.Method == "HEAD" && !exists {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})
		}
	}

	t.Run("existing index gets its replicas set to zero", func(t *testing.T) {
		var calls []call
		srv := httptest.NewServer(serve(true, &calls))
		defer srv.Close()
		if err := New(Config{Endpoint: srv.URL}, nil).EnsureIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 2 || calls[1].method != "PUT" || calls[1].path != "/artifacts/_settings" {
			t.Fatalf("calls = %+v, want HEAD then PUT /artifacts/_settings", calls)
		}
		idx := calls[1].body["index"].(map[string]any)
		if idx["number_of_replicas"] != float64(0) {
			t.Errorf("number_of_replicas = %v, want 0", idx["number_of_replicas"])
		}
	})

	t.Run("a failed settings update does not fail EnsureIndex", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "PUT" {
				w.WriteHeader(500)
			}
		}))
		defer srv.Close()
		if err := New(Config{Endpoint: srv.URL}, nil).EnsureIndex(context.Background()); err != nil {
			t.Fatalf("EnsureIndex = %v, want nil: a settings failure must not disable search", err)
		}
	})

	t.Run("new index is created with zero replicas", func(t *testing.T) {
		var calls []call
		srv := httptest.NewServer(serve(false, &calls))
		defer srv.Close()
		if err := New(Config{Endpoint: srv.URL}, nil).EnsureIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 2 || calls[1].method != "PUT" || calls[1].path != "/artifacts" {
			t.Fatalf("calls = %+v, want HEAD then PUT /artifacts", calls)
		}
		settings := calls[1].body["settings"].(map[string]any)
		if settings["number_of_replicas"] != float64(0) {
			t.Errorf("number_of_replicas = %v, want 0", settings["number_of_replicas"])
		}
	})
}
