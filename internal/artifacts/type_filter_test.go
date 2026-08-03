package artifacts

import (
	"strings"
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

// A negated `-type:` token must get the SAME resolution as the positive one.
// Positive `type:markdown` became a content_type glob while `-type:markdown`
// was appended to NotArtifactType verbatim — and no artifact_type is ever
// named "markdown", so the exclusion silently matched nothing. This is the
// exact bug ApplyTypeFilter was introduced to kill on the positive side,
// reappearing on the negative side.
func TestApplyNotTypeFilter(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantNotCT    string // "" == nothing appended
		wantNotArtTy string // "" == nothing appended
	}{
		{"real artifact type is excluded as artifact_type", "PACKAGE", "", "PACKAGE"},
		{"markdown is excluded as a content-type glob", "markdown", "text/markdown*", ""},
		{"diagram is excluded as a content-type glob", "diagram", DiagramContentType + "*", ""},
		{"image is excluded as a wildcard subtype", "IMAGE", "image/*", ""},
		{"empty value is ignored", "", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var in pgstore.ListInput
			ApplyNotTypeFilter(&in, tc.in)

			gotCT := strings.Join(in.NotContentType, ",")
			gotAT := strings.Join(in.NotArtifactType, ",")
			if gotCT != tc.wantNotCT {
				t.Errorf("NotContentType = %q, want %q", gotCT, tc.wantNotCT)
			}
			if gotAT != tc.wantNotArtTy {
				t.Errorf("NotArtifactType = %q, want %q", gotAT, tc.wantNotArtTy)
			}
		})
	}
}

// End-to-end through the search-token parser: the token a user actually types.
func TestParseQuery_NegatedPseudoType(t *testing.T) {
	var in pgstore.ListInput
	filters, negated, _ := parseQuery("-type:markdown")
	mergeFilters(&in, filters, negated)

	if got := strings.Join(in.NotContentType, ","); got != "text/markdown*" {
		t.Errorf("NotContentType = %q, want text/markdown*", got)
	}
	if len(in.NotArtifactType) != 0 {
		t.Errorf("NotArtifactType = %v, want empty (markdown is not an artifact_type)", in.NotArtifactType)
	}
}
