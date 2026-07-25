package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func zero() *int { z := 0; return &z }

// fakeAnthropic stands in for the Messages API. It records the last request body
// and returns either a canned message or a given error status.
type fakeAnthropic struct {
	srv      *httptest.Server
	lastBody map[string]any
	status   int    // 0 → 200 OK
	errType  string // for error responses
}

func newFake(t *testing.T) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &f.lastBody)
		w.Header().Set("Content-Type", "application/json")
		if f.status != 0 {
			w.WriteHeader(f.status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": f.errType, "message": "boom"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_test", "type": "message", "role": "assistant",
			"model":       "claude-sonnet-4-6",
			"content":     []map[string]any{{"type": "text", "text": "hello world"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 11, "output_tokens": 2},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newSvc(t *testing.T, f *fakeAnthropic, store Store) *Service {
	t.Helper()
	s, err := NewService(Config{APIKey: "test", BaseURL: f.srv.URL, MaxRetries: zero(), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestComplete_HappyPath(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f, nil)
	out, e := s.Complete(context.Background(), "alice@x", "app1", CompleteInput{Prompt: "hi"})
	if e != nil {
		t.Fatalf("unexpected error: %v", e)
	}
	if out != "hello world" {
		t.Fatalf("got %q", out)
	}
	if f.lastBody["model"] != "claude-sonnet-4-6" { // default
		t.Fatalf("default model not sonnet: %v", f.lastBody["model"])
	}
}

func TestComplete_ModelAliasAndAllowlist(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f, nil)
	// alias resolves
	if _, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi", Model: "opus"}); e != nil {
		t.Fatalf("alias opus rejected: %v", e)
	}
	if f.lastBody["model"] != "claude-opus-4-8" {
		t.Fatalf("alias opus → %v", f.lastBody["model"])
	}
	// off-allowlist rejected before any call
	_, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi", Model: "gpt-4"})
	if e == nil || e.Code != "bad_input" {
		t.Fatalf("expected bad_input for off-allowlist model, got %v", e)
	}
}

// Operator-supplied model strings (default + allowlist) may be aliases too; they
// must be canonicalized so the id sent upstream is canonical and an aliased
// request still matches the allowlist.
func TestNewService_CanonicalizesOperatorModels(t *testing.T) {
	f := newFake(t)
	s, err := NewService(Config{APIKey: "x", BaseURL: f.srv.URL, MaxRetries: zero(),
		DefaultModel: "sonnet", Allowed: []string{"opus", "haiku"}})
	if err != nil {
		t.Fatal(err)
	}
	// Default alias → canonical id sent upstream.
	if _, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi"}); e != nil {
		t.Fatal(e)
	}
	if f.lastBody["model"] != "claude-sonnet-4-6" {
		t.Fatalf("default alias not canonicalized: %v", f.lastBody["model"])
	}
	// A model allowlisted via its alias is accepted when requested by alias.
	if _, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi", Model: "opus"}); e != nil {
		t.Fatalf("opus (allowlisted as alias) rejected: %v", e)
	}
	if f.lastBody["model"] != "claude-opus-4-8" {
		t.Fatalf("opus → %v", f.lastBody["model"])
	}
}

func TestComplete_MaxTokensCap(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f, nil)
	if _, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi", MaxTokens: 999999}); e != nil {
		t.Fatal(e)
	}
	if mt, _ := f.lastBody["max_tokens"].(float64); int64(mt) != maxTokensCap {
		t.Fatalf("max_tokens not clamped to %d: %v", maxTokensCap, f.lastBody["max_tokens"])
	}
}

func TestComplete_JSONModeAddsSystem(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f, nil)
	if _, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi", ResponseFormat: "json"}); e != nil {
		t.Fatal(e)
	}
	sysRaw, _ := json.Marshal(f.lastBody["system"])
	if !strings.Contains(strings.ToLower(string(sysRaw)), "raw json") {
		t.Fatalf("json-mode instruction missing from system: %s", sysRaw)
	}
}

func TestComplete_NoMessagesOrPrompt(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f, nil)
	_, e := s.Complete(context.Background(), "a", "p", CompleteInput{})
	if e == nil || e.Code != "bad_input" {
		t.Fatalf("expected bad_input, got %v", e)
	}
}

func TestComplete_ErrorMapping(t *testing.T) {
	cases := []struct {
		status  int
		errType string
		code    string
	}{
		{429, "rate_limit_error", "rate_limited"},
		{529, "overloaded_error", "overloaded"},
		{400, "invalid_request_error", "bad_input"},
		{500, "api_error", "upstream"},
	}
	for _, c := range cases {
		f := newFake(t)
		f.status = c.status
		f.errType = c.errType
		s := newSvc(t, f, nil)
		_, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi"})
		if e == nil || e.Code != c.code {
			t.Fatalf("status %d → expected %q, got %v", c.status, c.code, e)
		}
	}
}

// budgetStore reports over-budget and records calls.
type budgetStore struct {
	over    bool
	err     error // non-nil → OverBudget fails (exercises fail-closed)
	records int
}

func (b *budgetStore) OverBudget(_ context.Context, _, _ string) (bool, int, error) {
	return b.over, 42, b.err
}
func (b *budgetStore) Record(_ context.Context, _ Usage) error { b.records++; return nil }

func TestComplete_BudgetGate(t *testing.T) {
	f := newFake(t)
	bs := &budgetStore{over: true}
	s := newSvc(t, f, bs)
	_, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi"})
	if e == nil || e.Code != "budget_exceeded" || e.RetryAfter != 42 {
		t.Fatalf("expected budget_exceeded(42), got %v", e)
	}
	if f.lastBody != nil {
		t.Fatal("over-budget call must NOT reach the model")
	}
	// under budget → call happens + recorded
	bs.over = false
	if _, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi"}); e != nil {
		t.Fatal(e)
	}
	if bs.records != 1 {
		t.Fatalf("expected 1 ledger record, got %d", bs.records)
	}
}

// A budget gate that can't read the ledger must fail CLOSED: block the paid
// call rather than wave it through uncapped.
func TestComplete_BudgetCheckFailsClosed(t *testing.T) {
	f := newFake(t)
	bs := &budgetStore{err: errors.New("db down")}
	s := newSvc(t, f, bs)
	_, e := s.Complete(context.Background(), "a", "p", CompleteInput{Prompt: "hi"})
	if e == nil || e.Code != "budget_unavailable" || e.Status != 503 {
		t.Fatalf("expected budget_unavailable(503), got %v", e)
	}
	if f.lastBody != nil {
		t.Fatal("budget-check failure must NOT reach the model")
	}
}

// Live round-trip against the real Anthropic API. Skipped unless ANTHROPIC_API_KEY
// is set (run: `set -a; . ~/.extra_secrets; set +a; go test ./internal/llm -run Live -v`).
func TestComplete_Live(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live test")
	}
	s, err := NewService(Config{APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	out, e := s.Complete(context.Background(), "tester", "live", CompleteInput{
		Prompt: "Reply with exactly: PONG", MaxTokens: 16,
	})
	if e != nil {
		t.Fatalf("live call failed: %v", e)
	}
	if !strings.Contains(strings.ToUpper(out), "PONG") {
		t.Fatalf("unexpected live reply: %q", out)
	}
	t.Logf("live reply: %q", out)
}
