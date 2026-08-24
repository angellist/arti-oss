package opensearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestSkipFullText(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"exact", []string{"manifest", "index:skip-fulltext"}, true},
		// Labels are free-form user input; a capital letter must not silently
		// re-enable indexing of a body someone asked to keep out.
		{"case-insensitive", []string{"Index:Skip-FullText"}, true},
		{"whitespace tolerated", []string{"  index:skip-fulltext  "}, true},
		{"absent", []string{"manifest", "auto-gen"}, false},
		{"no labels", nil, false},
		// Must not fire on a near-miss: these are different labels.
		{"prefix only", []string{"index"}, false},
		{"substring", []string{"no-index:skip-fulltext-ish"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SkipFullText(c.labels); got != c.want {
				t.Errorf("SkipFullText(%q) = %v, want %v", c.labels, got, c.want)
			}
		})
	}
}

// indexCapture spins a fake OpenSearch that records the indexed document.
func indexCapture(t *testing.T) (*Client, *map[string]any) {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "created"})
	}))
	t.Cleanup(srv.Close)
	return New(Config{Endpoint: srv.URL}, nil), &got
}

// The label must withhold ONLY the body. Everything the catalog searches on —
// title, description, labels, slug, creator — has to survive, or "skip
// full-text" would silently mean "unfindable".
func TestIndexArtifact_SkipFullTextLabelDropsOnlyContent(t *testing.T) {
	c, got := indexCapture(t)
	ix := NewIndexer(c, nil, nil)

	desc := "Machine state for the archive->arti skill publisher."
	slug := "couch-skill-publish-state"
	row := sqlc.Artifact{
		ArtifactType: pgstore.TypePackage, // PACKAGE path: extractPackageText, no store needed
		Title:        "couch skill publish state",
		Description:  &desc,
		NamedSlug:    &slug,
		ContentType:  "application/json",
		Creator:      "alice@example.com",
		Labels:       []string{"manifest", "auto-gen", SkipFullTextLabel},
	}
	ix.IndexArtifact(context.Background(), row)

	if _, present := (*got)["content_text"]; present {
		t.Errorf("content_text was indexed despite %s: %v", SkipFullTextLabel, (*got)["content_text"])
	}
	// omitempty means "absent", not "empty string" — assert the metadata is
	// still all there, so the doc stays findable by name.
	for field, want := range map[string]any{
		"title":        "couch skill publish state",
		"description":  desc,
		"named_slug":   slug,
		"content_type": "application/json",
		"creator":      "alice@example.com",
	} {
		if (*got)[field] != want {
			t.Errorf("%s = %v, want %v", field, (*got)[field], want)
		}
	}
	labels, _ := (*got)["labels"].([]any)
	if len(labels) != 3 {
		t.Errorf("labels = %v, want the 3 original labels preserved", (*got)["labels"])
	}
}

// Without the label, a PACKAGE still gets its file listing indexed — proving
// the test above measures the label and not a broken code path.
func TestIndexArtifact_WithoutLabelStillIndexesContent(t *testing.T) {
	c, got := indexCapture(t)
	ix := NewIndexer(c, nil, nil)

	row := sqlc.Artifact{
		ArtifactType: pgstore.TypePackage,
		Title:        "a normal package",
		ContentType:  "application/zip",
		Creator:      "alice@example.com",
		Labels:       []string{"manifest", "auto-gen"},
		Metadata:     []byte(`{"package":{"entries":[{"path":"course-of-action-playbooks"},{"path":"SKILL.md"}]}}`),
	}
	ix.IndexArtifact(context.Background(), row)

	ct, _ := (*got)["content_text"].(string)
	if ct == "" {
		t.Fatal("content_text empty without the skip label; the label test would pass vacuously")
	}
}
