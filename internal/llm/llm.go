// Package llm is arti's built-in, non-agentic single-Claude-completion service —
// the `llm.complete` tool behind the apps proxy (Auth:"service"). It makes ONE
// Anthropic Messages call with arti's service key, never passes `tools`, runs no
// agent loop, and shapes the reply for the MCP bridge. It implements apps'
// Completer interface.
//
// Provider: Claude only. Models: caller-selectable opus/sonnet/haiku (allowlist;
// default sonnet); aliases accepted for Claude-artifact compatibility. Output is
// capped (maxTokensCap) and usage is recorded to the ledger (Store) for the
// budget gate.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const (
	defaultMaxTokens int64 = 2048
	maxTokensCap     int64 = 10000 // hard ceiling — single non-streaming call
)

// Friendly aliases → canonical IDs, so a Claude.ai artifact passing a short name
// (or a human) still resolves. Canonical IDs are the source of truth in `allowed`.
var aliases = map[string]string{
	"opus":   "claude-opus-4-8",
	"sonnet": "claude-sonnet-4-6",
	"haiku":  "claude-haiku-4-5",
}

// canonModel maps a friendly alias (case-insensitive) to its canonical id and
// returns anything else unchanged. Applied to caller- AND operator-supplied
// names (default model, allowlist, request model) so they all compare as
// canonical ids — otherwise an operator setting ARTI_LLM_DEFAULT_MODEL=sonnet
// or allowlisting "sonnet" would send/reject a non-canonical id.
func canonModel(m string) string {
	if c, ok := aliases[strings.ToLower(strings.TrimSpace(m))]; ok {
		return c
	}
	return m
}

// Message is one turn of conversation (history is supported; the call is still
// single-shot and tool-free).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompleteInput is the `complete` tool's arguments.
type CompleteInput struct {
	Messages       []Message `json:"messages,omitempty"`
	Prompt         string    `json:"prompt,omitempty"` // convenience → one user message
	System         string    `json:"system,omitempty"`
	Model          string    `json:"model,omitempty"` // alias or canonical; "" → default
	MaxTokens      int64     `json:"max_tokens,omitempty"`
	ResponseFormat string    `json:"response_format,omitempty"` // "json" → enforce raw JSON
}

// Usage is recorded to the ledger after each call.
type Usage struct {
	Viewer       string
	AppID        string
	Model        string
	InputTokens  int64
	OutputTokens int64
	RequestID    string
	OK           bool
}

// Error is a structured failure the proxy maps to an HTTP status + JSON body the
// page's shim can detect and fall back on (rather than an opaque throw).
type Error struct {
	Status     int    // 400 | 429 | 502 | 503
	Code       string // bad_input | rate_limited | overloaded | upstream | budget_exceeded | budget_unavailable
	Msg        string
	RetryAfter int // seconds; 0 if N/A
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Store persists the usage ledger and answers the budget pre-check. Optional:
// a nil store disables both (used in unit/smoke tests).
type Store interface {
	// OverBudget reports whether (viewer, appID) has exceeded a configured cap.
	OverBudget(ctx context.Context, viewer, appID string) (bool, int, error) // (over, retryAfterSec, err)
	// Record appends one usage row (best-effort; called after each billable call).
	Record(ctx context.Context, u Usage) error
}

// Config builds a Service.
type Config struct {
	APIKey       string
	BaseURL      string   // optional; tests point this at an httptest server
	DefaultModel string   // canonical; "" → claude-sonnet-4-6
	Allowed      []string // canonical IDs; empty → opus/sonnet/haiku
	Store        Store    // optional ledger/budget
	MaxRetries   *int     // nil → SDK default (2); tests set 0
}

// Service makes the completion call.
type Service struct {
	client       anthropic.Client
	defaultModel string
	allowed      map[string]bool
	store        Store
}

func NewService(c Config) (*Service, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("llm: missing API key")
	}
	opts := []option.RequestOption{option.WithAPIKey(c.APIKey)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	if c.MaxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*c.MaxRetries))
	}
	def := c.DefaultModel
	if def == "" {
		def = "claude-sonnet-4-6"
	}
	def = canonModel(def) // an operator may pass an alias
	allowed := map[string]bool{}
	list := c.Allowed
	if len(list) == 0 {
		list = []string{"claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"}
	}
	for _, m := range list {
		allowed[canonModel(m)] = true // store canonical so aliased requests match
	}
	allowed[def] = true // the default is always callable
	return &Service{client: anthropic.NewClient(opts...), defaultModel: def, allowed: allowed, store: c.Store}, nil
}

func (s *Service) resolveModel(m string) (string, *Error) {
	if m == "" {
		return s.defaultModel, nil
	}
	m = canonModel(m)
	if !s.allowed[m] {
		return "", &Error{Status: 400, Code: "bad_input", Msg: "model not allowed: " + m}
	}
	return m, nil
}

// Complete runs one tool-free Claude call and returns its text. viewer/appID are
// used only for the budget gate + ledger (not for auth — the service key is
// arti's). On failure it returns a structured *Error.
func (s *Service) Complete(ctx context.Context, viewer, appID string, in CompleteInput) (string, *Error) {
	model, e := s.resolveModel(in.Model)
	if e != nil {
		return "", e
	}
	if s.store != nil {
		over, retry, err := s.store.OverBudget(ctx, viewer, appID)
		if err != nil {
			// Fail CLOSED, loudly. A budget gate that can't read the ledger must
			// not wave a paid upstream call through — and the failure must be
			// visible, not swallowed.
			slog.Error("llm: budget check failed; blocking call", "err", err, "viewer", viewer, "app", appID)
			return "", &Error{Status: 503, Code: "budget_unavailable", Msg: "budget check unavailable"}
		}
		if over {
			return "", &Error{Status: 429, Code: "budget_exceeded", Msg: "usage budget exceeded", RetryAfter: retry}
		}
	}

	maxTok := in.MaxTokens
	if maxTok <= 0 {
		maxTok = defaultMaxTokens
	}
	if maxTok > maxTokensCap {
		maxTok = maxTokensCap
	}

	var msgs []anthropic.MessageParam
	if len(in.Messages) > 0 {
		for _, m := range in.Messages {
			blk := anthropic.NewTextBlock(m.Content)
			if strings.EqualFold(m.Role, "assistant") {
				msgs = append(msgs, anthropic.NewAssistantMessage(blk))
			} else {
				msgs = append(msgs, anthropic.NewUserMessage(blk))
			}
		}
	} else if strings.TrimSpace(in.Prompt) != "" {
		msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(in.Prompt)))
	} else {
		return "", &Error{Status: 400, Code: "bad_input", Msg: "provide messages or prompt"}
	}

	sys := in.System
	if strings.EqualFold(in.ResponseFormat, "json") {
		j := "Output ONLY raw JSON: begin your reply with { and end with }. Do not use markdown code fences (no ```), and no prose before or after. (Callers should still parse defensively.)"
		if sys == "" {
			sys = j
		} else {
			sys = sys + "\n\n" + j
		}
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTok,
		Messages:  msgs,
	}
	if sys != "" {
		params.System = []anthropic.TextBlockParam{{Text: sys}}
	}

	msg, err := s.client.Messages.New(ctx, params)
	if err != nil {
		if s.store != nil {
			_ = s.store.Record(ctx, Usage{Viewer: viewer, AppID: appID, Model: model, OK: false})
		}
		return "", mapErr(err)
	}

	var sb strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	if s.store != nil {
		_ = s.store.Record(ctx, Usage{
			Viewer: viewer, AppID: appID, Model: model,
			InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens,
			RequestID: msg.ID, OK: true,
		})
	}
	return sb.String(), nil
}

// RunCompletion adapts Complete to the apps.Completer shape: raw MCP arguments
// in; MCP result JSON (`{content:[{type:text,text}]}`) out, or (httpStatus,
// errBody) on failure. This lets *Service satisfy apps.Completer structurally —
// neither package imports the other.
func (s *Service) RunCompletion(ctx context.Context, viewer, appID string, args map[string]any) (json.RawMessage, int, json.RawMessage) {
	var in CompleteInput
	if b, err := json.Marshal(args); err == nil {
		_ = json.Unmarshal(b, &in)
	}
	text, e := s.Complete(ctx, viewer, appID, in)
	if e != nil {
		body, _ := json.Marshal(map[string]any{"error": e.Code, "detail": e.Msg, "retry_after": e.RetryAfter})
		return nil, e.Status, body
	}
	result, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
	return result, 0, nil
}

func mapErr(err error) *Error {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case 400:
			return &Error{Status: 400, Code: "bad_input", Msg: ae.Error()}
		case 429:
			return &Error{Status: 429, Code: "rate_limited", Msg: ae.Error(), RetryAfter: 30}
		case 529:
			return &Error{Status: 503, Code: "overloaded", Msg: ae.Error()}
		}
		return &Error{Status: 502, Code: "upstream", Msg: ae.Error()}
	}
	return &Error{Status: 502, Code: "upstream", Msg: err.Error()}
}
