package artifacts

import (
	"testing"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ApplyTypeFilter is shared by three entry points (the ?type= query param, the
// `type:` search token, and the MCP/CLI list tool). Before it existed the
// translation lived inline in the query-param path only, so `type=MARKDOWN`
// returned rows in the web UI and zero rows everywhere else.
func TestApplyTypeFilter(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantCT      string // "" == not set
		wantArtType string // "" == not set
	}{
		{"real artifact type passes through", "TEXT", "", "TEXT"},
		{"package passes through", "PACKAGE", "", "PACKAGE"},
		{"markdown resolves to a content-type glob", "MARKDOWN", "text/markdown*", ""},
		{"html resolves to a content-type glob", "HTML", "text/html*", ""},
		{"diagram resolves to the diagram content type", "DIAGRAM", DiagramContentType + "*", ""},
		{"json resolves to a content-type glob", "JSON", "application/json*", ""},
		{"pdf resolves to a content-type glob", "PDF", "application/pdf*", ""},
		{"image resolves to a wildcard subtype", "IMAGE", "image/*", ""},
		// The rail sends uppercase, but a human typing `type:markdown` into the
		// search box means the same thing.
		{"pseudo-types are case-insensitive", "markdown", "text/markdown*", ""},
		{"mixed case pseudo-type", "Diagram", DiagramContentType + "*", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var in pgstore.ListInput
			ApplyTypeFilter(&in, tc.in)

			if tc.wantCT == "" {
				if in.ContentType != nil {
					t.Errorf("ContentType = %q, want unset", *in.ContentType)
				}
			} else if in.ContentType == nil {
				t.Errorf("ContentType unset, want %q", tc.wantCT)
			} else if *in.ContentType != tc.wantCT {
				t.Errorf("ContentType = %q, want %q", *in.ContentType, tc.wantCT)
			}

			if tc.wantArtType == "" {
				if in.ArtifactType != nil {
					t.Errorf("ArtifactType = %q, want unset", *in.ArtifactType)
				}
			} else if in.ArtifactType == nil {
				t.Errorf("ArtifactType unset, want %q", tc.wantArtType)
			} else if *in.ArtifactType != tc.wantArtType {
				t.Errorf("ArtifactType = %q, want %q", *in.ArtifactType, tc.wantArtType)
			}
		})
	}
}

// An empty value must leave the input completely alone: "no type filter" is not
// the same as "filter for the empty type", which would match nothing.
func TestApplyTypeFilterEmptyIsNoop(t *testing.T) {
	var in pgstore.ListInput
	ApplyTypeFilter(&in, "")
	if in.ContentType != nil || in.ArtifactType != nil {
		t.Errorf("empty type set a filter: ct=%v at=%v", in.ContentType, in.ArtifactType)
	}
}

// A pseudo-type must not ALSO set ArtifactType: "DIAGRAM" is not an
// artifact_type, and setting both would AND them into a query matching nothing.
func TestApplyTypeFilterPseudoLeavesArtifactTypeUnset(t *testing.T) {
	var in pgstore.ListInput
	ApplyTypeFilter(&in, "DIAGRAM")
	if in.ArtifactType != nil {
		t.Errorf("ArtifactType = %q, want unset for a pseudo-type", *in.ArtifactType)
	}
}
