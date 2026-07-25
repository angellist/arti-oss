package artifacts

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/llm"
)

// MetadataCompleter is the slice of the llm service used to suggest
// artifact metadata from content. nil when ANTHROPIC_API_KEY is unset —
// the endpoint then degrades to filename-derived suggestions only.
type MetadataCompleter interface {
	Complete(ctx context.Context, viewer, appID string, in llm.CompleteInput) (string, *llm.Error)
}

// SetMetadataCompleter wires the LLM service used by the suggest-metadata
// endpoint. Optional: leaving it nil disables the LLM path.
func (s *Service) SetMetadataCompleter(c MetadataCompleter) { s.meta = c }

// metadataAppID labels suggest-metadata usage in the LLM ledger/budget. It
// is not a real APP artifact — just a stable key so these calls share one
// budget bucket distinct from app-proxy traffic.
const metadataAppID = "arti-web-upload"

// metadataSampleLimit caps how much decoded text we feed haiku. The user
// asked for "first 1k" — enough to title/slug/label a doc, cheap to send.
const metadataSampleLimit = 1000

// SuggestMetadataRequest is the body of POST /api/artifacts/suggest-metadata.
type SuggestMetadataRequest struct {
	Filename     string `json:"filename"`
	ContentType  string `json:"content_type"`
	ArtifactType string `json:"artifact_type"`
	// Sample is up to the first ~1k characters of decoded text content.
	// Empty for binary/zip artifacts (the client doesn't send bytes it
	// can't usefully decode); those fall back to the filename.
	Sample string `json:"sample"`
}

// SuggestMetadataResponse is what the upload modal pre-fills from.
type SuggestMetadataResponse struct {
	Title  string   `json:"title"`
	Slug   string   `json:"slug"`
	Labels []string `json:"labels"`
}

// httpSuggestMetadata returns a suggested title/slug/labels for an
// about-to-be-uploaded artifact. For textual content with a sample it asks
// haiku; for binary/zip (or when the LLM is unavailable or errors) it
// derives a title/slug from the filename so the button always does
// something useful. Never 500s on an LLM failure — it degrades.
func (s *Service) httpSuggestMetadata(w http.ResponseWriter, r *http.Request) {
	var body SuggestMetadataRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad-request", "decode body: "+err.Error())
		return
	}
	email := auth.EmailFromContext(r.Context())

	// Filename-derived suggestion — always available; used as-is for
	// binary/zip and as the fallback whenever the LLM can't help.
	fallback := SuggestMetadataResponse{
		Title:  humanizeFilename(body.Filename),
		Slug:   slugify(baseNoExt(body.Filename)),
		Labels: []string{},
	}

	// Only spend a model call on textual content we actually have a sample
	// for. Everything else gets the deterministic filename suggestion.
	if s.meta == nil || strings.TrimSpace(body.Sample) == "" || !isTextualContentType(body.ContentType) {
		writeJSON(w, http.StatusOK, fallback)
		return
	}

	out, perr := s.suggestViaLLM(r.Context(), email, body)
	if perr != nil {
		// Degrade to the filename suggestion rather than failing the upload
		// flow — but log it so a misbehaving model/budget is debuggable
		// instead of silently swallowed.
		slog.Warn("suggest-metadata: LLM degraded to filename fallback",
			"code", perr.Code, "detail", perr.Msg, "viewer", email, "filename", body.Filename)
		writeJSON(w, http.StatusOK, fallback)
		return
	}
	// Backfill anything the model left blank; always re-slugify its slug so
	// the field is upload-ready regardless of what the model returned.
	if strings.TrimSpace(out.Title) == "" {
		out.Title = fallback.Title
	}
	if sl := slugify(out.Slug); sl != "" {
		out.Slug = sl
	} else {
		out.Slug = fallback.Slug
	}
	if out.Labels == nil {
		out.Labels = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) suggestViaLLM(ctx context.Context, viewer string, body SuggestMetadataRequest) (SuggestMetadataResponse, *llm.Error) {
	// Truncate by rune, not byte: the client sends up to metadataSampleLimit
	// CHARACTERS, which for non-ASCII text (CJK, emoji) is several KB. A byte
	// slice at metadataSampleLimit would split a multi-byte rune and feed
	// invalid UTF-8 to the API (auto-fill would then silently fall back).
	sample := body.Sample
	if utf8.RuneCountInString(sample) > metadataSampleLimit {
		sample = string([]rune(sample)[:metadataSampleLimit])
	}
	const sys = "You generate concise metadata for a document being uploaded to an internal artifact store. " +
		"Reply with ONLY a JSON object of the form " +
		`{"title": string, "slug": string, "labels": string[]}. ` +
		"title: a short human-readable title, at most ~80 chars, no trailing punctuation. " +
		"slug: lowercase kebab-case, ascii letters/digits/hyphens only, at most ~50 chars. " +
		"labels: 1-4 short lowercase topical tags (single words or kebab-case); use [] if unsure. " +
		"No prose, no markdown, no code fences."
	prompt := "Filename: " + body.Filename + "\nContent-Type: " + body.ContentType +
		"\n\nFirst part of the content:\n" + sample
	raw, perr := s.meta.Complete(ctx, viewer, metadataAppID, llm.CompleteInput{
		Prompt:         prompt,
		System:         sys,
		Model:          "haiku",
		MaxTokens:      300,
		ResponseFormat: "json",
	})
	if perr != nil {
		return SuggestMetadataResponse{}, perr
	}
	var out SuggestMetadataResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		return SuggestMetadataResponse{}, &llm.Error{Status: 502, Code: "bad_output", Msg: "model returned non-JSON"}
	}
	return out, nil
}

// ─── filename → title/slug helpers ───────────────────────────────────

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases, replaces runs of non-alphanumerics with a single
// hyphen, trims hyphens, and caps length. Returns "" for empty/garbage
// input so callers can fall back.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = strings.Trim(s[:50], "-")
	}
	return s
}

// baseNoExt returns the filename's base without its extension.
func baseNoExt(filename string) string {
	b := path.Base(strings.TrimSpace(filename))
	if b == "." || b == "/" || b == "" {
		return ""
	}
	if ext := path.Ext(b); ext != "" && ext != b {
		b = strings.TrimSuffix(b, ext)
	}
	return b
}

// humanizeFilename turns "weekly_report-v2.md" into "Weekly Report V2".
// Returns "Untitled" when there's nothing usable.
func humanizeFilename(filename string) string {
	b := baseNoExt(filename)
	b = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(b)
	words := strings.Fields(b)
	if len(words) == 0 {
		return "Untitled"
	}
	for i, w := range words {
		r, sz := utf8.DecodeRuneInString(w)
		words[i] = string(unicode.ToUpper(r)) + w[sz:]
	}
	return strings.Join(words, " ")
}
