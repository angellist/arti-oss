//go:build integration

package apps_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/apps"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// newResolvedStack serves "llm" as the only built-in and every other server
// from rows; asked records each name the resolver was consulted for.
func newResolvedStack(t *testing.T, rows map[string]apps.ServerConfig, resolveErr error) (*pgstore.Store, *apps.Service, http.Handler, *[]string) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	t.Cleanup(up.Close)
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	svc := apps.New(st, signer, map[string]apps.ServerConfig{"llm": {Name: "llm", Auth: "service"}}, nil, nil, nil)
	asked := &[]string{}
	svc.SetServerResolver(func(ctx context.Context, name string) (apps.ServerConfig, bool, error) {
		*asked = append(*asked, name)
		if resolveErr != nil {
			return apps.ServerConfig{}, false, resolveErr
		}
		sc, ok := rows[name]
		sc.ResourceURL = up.URL
		return sc, ok, nil
	})
	r := chi.NewRouter()
	svc.MountProxy(r)
	return st, svc, r, asked
}

func errorBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var b map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &b)
	return b
}

func TestProxyResolvesConnectorStates(t *testing.T) {
	rows := map[string]apps.ServerConfig{
		"on":  {Auth: "none"},
		"off": {Auth: "none", Disabled: true},
	}
	st, svc, r, _ := newResolvedStack(t, rows, nil)
	appID := newAppArtifact(t, st, []map[string]string{
		{"server": "on", "tool": "t"}, {"server": "off", "tool": "t"}, {"server": "gone", "tool": "t"},
	})
	tok := mustToken(t, svc, "alice@example.com", appID)

	if rr := proxyPost(t, r, tok, appID, "on", "t", nil); rr.Code != http.StatusOK {
		t.Fatalf("enabled: status %d, body %s", rr.Code, rr.Body.String())
	}
	rr := proxyPost(t, r, tok, appID, "off", "t", nil)
	if b := errorBody(t, rr); rr.Code != http.StatusBadRequest || b["error"] != "unknown_server" || !strings.Contains(b["detail"], "disabled") {
		t.Fatalf("disabled: status %d, body %v; want 400 unknown_server naming disabled", rr.Code, b)
	}
	rr = proxyPost(t, r, tok, appID, "gone", "t", nil)
	if b := errorBody(t, rr); rr.Code != http.StatusBadRequest || b["error"] != "unknown_server" || strings.Contains(b["detail"], "disabled") {
		t.Fatalf("unknown: status %d, body %v; want 400 unknown_server without the disabled wording", rr.Code, b)
	}
}

// A lookup failure must not look like a missing server: an admin chasing
// "unknown server" would go and re-register a connector that is fine.
func TestProxyResolverErrorIs503(t *testing.T) {
	st, svc, r, _ := newResolvedStack(t, nil, errors.New("db down"))
	appID := newAppArtifact(t, st, []map[string]string{{"server": "on", "tool": "t"}})
	rr := proxyPost(t, r, mustToken(t, svc, "alice@example.com", appID), appID, "on", "t", nil)
	if b := errorBody(t, rr); rr.Code != http.StatusServiceUnavailable || b["error"] != "server_lookup_failed" {
		t.Fatalf("status %d, body %v; want 503 server_lookup_failed", rr.Code, b)
	}
}

func TestProxyBuiltinIgnoresRowOfSameName(t *testing.T) {
	rows := map[string]apps.ServerConfig{"llm": {Auth: "none"}}
	st, svc, r, asked := newResolvedStack(t, rows, nil)
	appID := newAppArtifact(t, st, []map[string]string{{"server": "llm", "tool": "complete"}})
	rr := proxyPost(t, r, mustToken(t, svc, "alice@example.com", appID), appID, "llm", "complete", nil)
	// No completer is wired, so the built-in answers 501; a row would have
	// forwarded upstream and answered 200.
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status %d, body %s; want the built-in's 501", rr.Code, rr.Body.String())
	}
	if len(*asked) != 0 {
		t.Fatalf("resolver consulted for a built-in: %v", *asked)
	}
}

// The operator allowlist narrows what a manifest may call: an app author
// cannot widen a connector past what the admin granted.
func TestProxyOperatorToolAllowlist(t *testing.T) {
	rows := map[string]apps.ServerConfig{
		"narrow": {Auth: "none", ToolAllowlist: []string{"read_thing"}},
		"open":   {Auth: "none"},
	}
	st, svc, r, _ := newResolvedStack(t, rows, nil)
	appID := newAppArtifact(t, st, []map[string]string{
		{"server": "narrow", "tool": "read_thing"}, {"server": "narrow", "tool": "delete_thing"},
		{"server": "open", "tool": "delete_thing"},
	})
	tok := mustToken(t, svc, "alice@example.com", appID)

	if rr := proxyPost(t, r, tok, appID, "narrow", "read_thing", nil); rr.Code != http.StatusOK {
		t.Fatalf("allowlisted tool: status %d, body %s", rr.Code, rr.Body.String())
	}
	rr := proxyPost(t, r, tok, appID, "narrow", "delete_thing", nil)
	if b := errorBody(t, rr); rr.Code != http.StatusForbidden || b["error"] != "not_allowlisted" {
		t.Fatalf("tool outside operator allowlist: status %d, body %v; want 403 not_allowlisted", rr.Code, b)
	}
	if rr := proxyPost(t, r, tok, appID, "open", "delete_thing", nil); rr.Code != http.StatusOK {
		t.Fatalf("empty allowlist should admit every tool: status %d, body %s", rr.Code, rr.Body.String())
	}
}

func TestCallableMatchesProxyRouting(t *testing.T) {
	rows := map[string]apps.ServerConfig{
		"on":     {Auth: "none"},
		"off":    {Auth: "none", Disabled: true},
		"narrow": {Auth: "none", ToolAllowlist: []string{"a"}},
	}
	_, svc, _, _ := newResolvedStack(t, rows, nil)
	for _, c := range []struct {
		server, tool string
		want         bool
	}{
		{"on", "x", true}, {"off", "x", false}, {"gone", "x", false},
		{"narrow", "a", true}, {"narrow", "b", false},
		// No completer is wired here, so the proxy answers llm with 501.
		{"llm", "complete", false},
	} {
		got, err := svc.Callable(context.Background(), c.server, c.tool)
		if err != nil || got != c.want {
			t.Errorf("Callable(%s, %s) = %v, %v; want %v", c.server, c.tool, got, err, c.want)
		}
	}
}
