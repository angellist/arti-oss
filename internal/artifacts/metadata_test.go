package artifacts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/llm"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Weekly Report":         "weekly-report",
		"  Hello, World!  ":     "hello-world",
		"already-kebab":         "already-kebab",
		"UPPER_snake.case":      "upper-snake-case",
		"--leading--trailing--": "leading-trailing",
		"":                      "",
		"!!!":                   "",
		strings.Repeat("a", 80): strings.Repeat("a", 50),
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanizeFilename(t *testing.T) {
	cases := map[string]string{
		"weekly_report-v2.md": "Weekly Report V2",
		"notes.txt":           "Notes",
		"path/to/My File.pdf": "My File",
		"":                    "Untitled",
		".gitignore":          "Gitignore",
		"data.tar.gz":         "Data Tar",
	}
	for in, want := range cases {
		if got := humanizeFilename(in); got != want {
			t.Errorf("humanizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// stubCompleter returns a canned response (or error) and records the model
// it was asked for, so we can assert haiku is used.
type stubCompleter struct {
	reply     string
	err       *llm.Error
	gotModel  string
	gotPrompt string
	called    bool
}

func (s *stubCompleter) Complete(_ context.Context, _, _ string, in llm.CompleteInput) (string, *llm.Error) {
	s.called = true
	s.gotModel = in.Model
	s.gotPrompt = in.Prompt
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

func postSuggest(t *testing.T, svc *Service, body SuggestMetadataRequest) SuggestMetadataResponse {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/artifacts/suggest-metadata", strings.NewReader(string(b)))
	req = req.WithContext(auth.WithIdentity(req.Context(), "tian@example.com"))
	rec := httptest.NewRecorder()
	svc.httpSuggestMetadata(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out SuggestMetadataResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return out
}

// Textual content with a sample asks haiku and returns its parsed output.
func TestSuggestMetadata_TextUsesLLM(t *testing.T) {
	svc := NewService(nil, "http://localhost", nil, nil)
	stub := &stubCompleter{reply: `{"title":"Q2 Board Notes","slug":"q2 board notes","labels":["board","notes"]}`}
	svc.SetMetadataCompleter(stub)

	out := postSuggest(t, svc, SuggestMetadataRequest{
		Filename: "notes.md", ContentType: "text/markdown", ArtifactType: "TEXT",
		Sample: "# Q2 board meeting\nDiscussed runway and hiring.",
	})
	if !stub.called {
		t.Fatal("expected LLM to be called for textual content")
	}
	if stub.gotModel != "haiku" {
		t.Errorf("model = %q, want haiku", stub.gotModel)
	}
	if out.Title != "Q2 Board Notes" {
		t.Errorf("title = %q", out.Title)
	}
	// Model returned a slug with spaces; the server must normalize it.
	if out.Slug != "q2-board-notes" {
		t.Errorf("slug = %q, want q2-board-notes", out.Slug)
	}
	if len(out.Labels) != 2 {
		t.Errorf("labels = %v", out.Labels)
	}
}

// Binary content never hits the LLM — it derives from the filename.
func TestSuggestMetadata_BinarySkipsLLM(t *testing.T) {
	svc := NewService(nil, "http://localhost", nil, nil)
	stub := &stubCompleter{reply: `{"title":"nope"}`}
	svc.SetMetadataCompleter(stub)

	out := postSuggest(t, svc, SuggestMetadataRequest{
		Filename: "kyc_doc.pdf", ContentType: "application/pdf", ArtifactType: "ATTACHMENT",
		Sample: "", // client sends no sample for binary
	})
	if stub.called {
		t.Fatal("LLM should not be called for binary content")
	}
	if out.Title != "Kyc Doc" {
		t.Errorf("title = %q, want 'Kyc Doc'", out.Title)
	}
	if out.Slug != "kyc-doc" {
		t.Errorf("slug = %q, want kyc-doc", out.Slug)
	}
	if out.Labels == nil {
		t.Error("labels should be [] not null")
	}
}

// An LLM error degrades to the filename suggestion (never 500s).
func TestSuggestMetadata_LLMErrorDegrades(t *testing.T) {
	svc := NewService(nil, "http://localhost", nil, nil)
	stub := &stubCompleter{err: &llm.Error{Status: 429, Code: "budget_exceeded", Msg: "over"}}
	svc.SetMetadataCompleter(stub)

	out := postSuggest(t, svc, SuggestMetadataRequest{
		Filename: "release-notes.md", ContentType: "text/markdown", ArtifactType: "TEXT",
		Sample: "stuff",
	})
	if out.Title != "Release Notes" || out.Slug != "release-notes" {
		t.Errorf("expected filename fallback, got title=%q slug=%q", out.Title, out.Slug)
	}
}

// The sample is truncated by rune, not byte: a long non-ASCII sample must not
// be split mid-rune (which would feed invalid UTF-8 to the LLM and silently
// degrade auto-fill for non-English docs).
func TestSuggestMetadata_TruncatesSampleByRune(t *testing.T) {
	svc := NewService(nil, "http://localhost", nil, nil)
	stub := &stubCompleter{reply: `{"title":"t","slug":"s","labels":[]}`}
	svc.SetMetadataCompleter(stub)

	// 1500 three-byte runes — well past the 1000-rune cap (and 4500 bytes).
	postSuggest(t, svc, SuggestMetadataRequest{
		Filename: "doc.md", ContentType: "text/markdown", ArtifactType: "TEXT",
		Sample: strings.Repeat("界", 1500),
	})
	if !stub.called {
		t.Fatal("expected LLM call for textual content")
	}
	if !utf8.ValidString(stub.gotPrompt) {
		t.Fatal("prompt is not valid UTF-8 — sample was split mid-rune")
	}
	if got := strings.Count(stub.gotPrompt, "界"); got != metadataSampleLimit {
		t.Errorf("sample kept %d runes, want %d", got, metadataSampleLimit)
	}
}

// With no completer wired (ANTHROPIC_API_KEY unset) the endpoint still works.
func TestSuggestMetadata_NoCompleter(t *testing.T) {
	svc := NewService(nil, "http://localhost", nil, nil)
	out := postSuggest(t, svc, SuggestMetadataRequest{
		Filename: "my-doc.txt", ContentType: "text/plain", ArtifactType: "TEXT",
		Sample: "hello",
	})
	if out.Title != "My Doc" || out.Slug != "my-doc" {
		t.Errorf("title=%q slug=%q", out.Title, out.Slug)
	}
}
