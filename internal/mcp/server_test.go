//go:build integration

package mcp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/mcp"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("ARTI_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func uniqueSlug(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// rpcCall posts a JSON-RPC request to the MCP handler as `caller` and returns
// the raw response recorder. The MCP server reads identity from the request
// context (auth.RequireAuth injects it in prod), so we fake it here.
func rpcCall(t *testing.T, h http.Handler, caller string, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(payload)))
	req = req.WithContext(auth.WithIdentity(req.Context(), caller))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	return rr
}

// tools/list must advertise update_artifact, otherwise an MCP client never
// learns the tool exists (this is the parity gap the tool closes).
func TestToolsListIncludesUpdateArtifact(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	h := mcp.NewServer(svc, nil).Handler()

	rr := rpcCall(t, h, "alice@example.com", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if !strings.Contains(rr.Body.String(), `"update_artifact"`) {
		t.Fatalf("tools/list missing update_artifact: %s", rr.Body.String())
	}
}

// A tools/call to update_artifact (by slug) edits metadata in place via the
// shared Service path — proving the MCP wiring + auth context reach it.
func TestUpdateArtifactToolEditsBySlug(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	h := mcp.NewServer(svc, nil).Handler()

	slug := uniqueSlug("mcp-update")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "Before",
		ContentType:  "text/markdown",
		Content:      []byte("body"),
		Creator:      "alice@example.com",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"ident":  slug,
		"title":  "After (via MCP)",
		"labels": []string{"mcp", "edited"},
	})
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "update_artifact", "arguments": json.RawMessage(args)},
	})

	rr := rpcCall(t, h, "alice@example.com", string(payload))

	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc resp: %v (%s)", err, rr.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %s", resp.Error.Message)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("empty content: %s", rr.Body.String())
	}
	var info artifacts.ArtifactInfo
	if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &info); err != nil {
		t.Fatalf("decode artifact info: %v", err)
	}
	if info.Title != "After (via MCP)" {
		t.Errorf("title = %q, want %q", info.Title, "After (via MCP)")
	}
	if len(info.Labels) != 2 || info.Labels[0] != "mcp" || info.Labels[1] != "edited" {
		t.Errorf("labels = %v, want [mcp edited]", info.Labels)
	}

	// Persisted, single version (no versioning on a metadata edit).
	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatalf("get back: %v", err)
	}
	if row.Title != "After (via MCP)" {
		t.Errorf("stored title = %q, want %q", row.Title, "After (via MCP)")
	}
	if row.Version == nil || *row.Version != 1 {
		t.Errorf("version = %v, want 1", row.Version)
	}
}

// update_artifact carries allowed_write through the MCP layer; the store
// unions it into allowed_access (⊆ invariant) and persists it.
func TestUpdateArtifactToolSetsAllowedWrite(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	h := mcp.NewServer(svc, nil).Handler()

	slug := uniqueSlug("mcp-write")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, NamedSlug: &slug, Title: "w",
		ContentType: "text/markdown", Content: []byte("body"),
		Creator: "alice@example.com", AllowedAccess: []string{"*"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"ident":         slug,
		"allowed_write": []string{"bob@example.com"},
	})
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "update_artifact", "arguments": json.RawMessage(args)},
	})
	rr := rpcCall(t, h, "alice@example.com", string(payload))
	if strings.Contains(rr.Body.String(), `"error"`) {
		t.Fatalf("rpc error: %s", rr.Body.String())
	}

	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatalf("get back: %v", err)
	}
	if len(row.AllowedWrite) != 1 || row.AllowedWrite[0] != "bob@example.com" {
		t.Errorf("allowed_write = %v, want [bob@example.com]", row.AllowedWrite)
	}
	// ⊆ invariant: bob must have been unioned into allowed_access.
	var hasBob bool
	for _, p := range row.AllowedAccess {
		if p == "bob@example.com" {
			hasBob = true
		}
	}
	if !hasBob {
		t.Errorf("allowed_access %v must include the write grant bob@example.com", row.AllowedAccess)
	}
}

// callToolResult invokes a tool via tools/call and returns the parsed JSON-RPC
// result object (the tool's content envelope plus its size annotations).
func callToolResult(t *testing.T, h http.Handler, caller, name string, args map[string]any) map[string]any {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	rr := rpcCall(t, h, caller, string(payload))
	var resp struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc resp: %v (%s)", err, rr.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %s", resp.Error.Message)
	}
	return resp.Result
}

// firstText pulls the single content text block out of a tool result.
func firstText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content in result: %v", result)
	}
	return content[0].(map[string]any)["text"].(string)
}

func seedText(t *testing.T, st *pgstore.Store, slug, body string) {
	t.Helper()
	if _, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "size " + slug,
		ContentType:  "text/plain",
		Content:      []byte(body),
		Creator:      "alice@example.com",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// A full read_artifact reports the artifact's size + sha so an agent that
// fetched the content also learns how big it was, and truncated is false.
func TestReadArtifactReportsSize(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	h := mcp.NewServer(artifacts.NewService(st, "http://localhost", nil, nil), nil).Handler()

	slug := uniqueSlug("read-size")
	seedText(t, st, slug, "hello world") // 11 bytes

	res := callToolResult(t, h, "alice@example.com", "read_artifact", map[string]any{"ident": slug})
	if txt := firstText(t, res); txt != "hello world" {
		t.Errorf("text = %q, want %q", txt, "hello world")
	}
	if res["size_bytes"] != float64(11) {
		t.Errorf("size_bytes = %v, want 11", res["size_bytes"])
	}
	if res["returned_bytes"] != float64(11) {
		t.Errorf("returned_bytes = %v, want 11", res["returned_bytes"])
	}
	if res["truncated"] != false {
		t.Errorf("truncated = %v, want false", res["truncated"])
	}
	if s, _ := res["sha256"].(string); s == "" {
		t.Errorf("sha256 missing on full read")
	}
}

// max_bytes returns only a prefix and flags truncated=true, while size_bytes
// still reports the FULL size — so an agent can peek a large doc, see how much
// it skipped, and decide whether to fetch the rest.
func TestReadArtifactMaxBytesPrefix(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	h := mcp.NewServer(artifacts.NewService(st, "http://localhost", nil, nil), nil).Handler()

	slug := uniqueSlug("read-cap")
	seedText(t, st, slug, "hello world") // 11 bytes

	res := callToolResult(t, h, "alice@example.com", "read_artifact",
		map[string]any{"ident": slug, "max_bytes": 5})
	if txt := firstText(t, res); txt != "hello" {
		t.Errorf("prefix = %q, want %q", txt, "hello")
	}
	if res["truncated"] != true {
		t.Errorf("truncated = %v, want true", res["truncated"])
	}
	if res["returned_bytes"] != float64(5) {
		t.Errorf("returned_bytes = %v, want 5", res["returned_bytes"])
	}
	if res["size_bytes"] != float64(11) {
		t.Errorf("size_bytes = %v, want 11 (full size, not the prefix)", res["size_bytes"])
	}
}

// CallToolInProcess runs a tool with the caller taken from ctx (not a JSON-RPC
// body) and returns the same content envelope a remote MCP call yields. This is
// the path the apps proxy uses to read arti artifacts in-process as the viewer
// (no OBO hop / consent popup), so it must produce the identical result.
func TestCallToolInProcessReadAsViewer(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	srv := mcp.NewServer(artifacts.NewService(st, "http://localhost", nil, nil), nil)

	slug := uniqueSlug("inproc-read")
	seedText(t, st, slug, "hello world")

	out, err := srv.CallToolInProcess(
		auth.WithIdentity(context.Background(), "alice@example.com"),
		"read_artifact", json.RawMessage(`{"ident":"`+slug+`"}`))
	if err != nil {
		t.Fatalf("CallToolInProcess: %v", err)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Fatalf("result missing content: %s", out)
	}
}

// The in-process path still enforces per-caller access: alice cannot read bob's
// creator-only artifact, so the short-circuit returns an error (which the proxy
// maps to 404) rather than leaking content.
func TestCallToolInProcessEnforcesAccess(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	srv := mcp.NewServer(artifacts.NewService(st, "http://localhost", nil, nil), nil)

	slug := uniqueSlug("inproc-noaccess")
	ctx := context.Background()
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		NamedSlug:     &slug,
		Title:         "secret " + slug,
		ContentType:   "text/plain",
		Content:       []byte("top secret"),
		Creator:       "bob@example.com",
		AllowedAccess: []string{}, // empty == creator-only
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := srv.CallToolInProcess(
		auth.WithIdentity(ctx, "alice@example.com"),
		"read_artifact", json.RawMessage(`{"ident":"`+slug+`"}`)); err == nil {
		t.Fatalf("expected access error reading bob's creator-only artifact as alice")
	}
}

// search_artifacts honors order_by/order_dir (pass-through to the store's
// allowlisted sort) so an app can ask for newest-first or by title — the
// capability that lets a template fetch "the latest" data artifact.
func TestSearchOrderByTitle(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	h := mcp.NewServer(artifacts.NewService(st, "http://localhost", nil, nil), nil).Handler()

	tok := uniqueSlug("ordtok")
	put := func(title string) {
		slug := uniqueSlug("ord")
		if _, err := st.Put(context.Background(), pgstore.PutInput{
			ArtifactType: pgstore.TypeText,
			NamedSlug:    &slug,
			Title:        title,
			ContentType:  "text/plain",
			Content:      []byte("x"),
			Creator:      "alice@example.com",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	put(tok + " banana")
	put(tok + " apple")

	titles := func(args map[string]any) []string {
		res := callToolResult(t, h, "alice@example.com", "search_artifacts", args)
		var parsed struct {
			Artifacts []struct {
				Title string `json:"title"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal([]byte(firstText(t, res)), &parsed); err != nil {
			t.Fatalf("parse search result: %v", err)
		}
		out := make([]string, len(parsed.Artifacts))
		for i, a := range parsed.Artifacts {
			out[i] = a.Title
		}
		return out
	}

	asc := titles(map[string]any{"q": tok, "order_by": "title", "order_dir": "asc"})
	if len(asc) != 2 || asc[0] != tok+" apple" || asc[1] != tok+" banana" {
		t.Fatalf("asc order = %v, want [%q %q]", asc, tok+" apple", tok+" banana")
	}
	desc := titles(map[string]any{"q": tok, "order_by": "title", "order_dir": "desc"})
	if len(desc) != 2 || desc[0] != tok+" banana" {
		t.Fatalf("desc order = %v, want banana first", desc)
	}
}

// search_artifacts works with NO q when filtering by label (the dataset-
// enumeration case the data-app example relies on), and still honors order_by.
func TestSearchByLabelOnlyOrdered(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	h := mcp.NewServer(artifacts.NewService(st, "http://localhost", nil, nil), nil).Handler()

	label := uniqueSlug("dataset")
	put := func(title string) {
		slug := uniqueSlug("ds")
		if _, err := st.Put(context.Background(), pgstore.PutInput{
			ArtifactType: pgstore.TypeText,
			NamedSlug:    &slug,
			Title:        title,
			ContentType:  "text/plain",
			Content:      []byte("x"),
			Creator:      "alice@example.com",
			Labels:       []string{label},
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	put("zebra")
	put("apple")

	res := callToolResult(t, h, "alice@example.com", "search_artifacts",
		map[string]any{"labels": []string{label}, "order_by": "title", "order_dir": "asc"})
	var parsed struct {
		Artifacts []struct {
			Title string `json:"title"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(firstText(t, res)), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Artifacts) != 2 || parsed.Artifacts[0].Title != "apple" || parsed.Artifacts[1].Title != "zebra" {
		t.Fatalf("got %+v, want [apple zebra]", parsed.Artifacts)
	}
}

// An empty scope string is not a real filter, so it must not let a no-q,
// no-label search through (that would return everything unfiltered).
func TestSearchEmptyScopeIsNotAFilter(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	srv := mcp.NewServer(artifacts.NewService(st, "http://localhost", nil, nil), nil)
	if _, err := srv.CallToolInProcess(
		auth.WithIdentity(context.Background(), "alice@example.com"),
		"search_artifacts", json.RawMessage(`{"q":"","scope":""}`)); err == nil {
		t.Fatalf("expected error: empty scope must not satisfy the filter requirement")
	}
}
