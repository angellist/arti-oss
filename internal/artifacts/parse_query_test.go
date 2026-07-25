package artifacts

import (
	"reflect"
	"testing"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// parseQuery must split a `-` prefix into a separate negated bucket, but only
// for the fields we actually negate (label, scope, creator, type). A leading
// `-` on slug, an unknown field, or a bare word stays in the remainder
// untouched so free-text search still works the way it always has.
func TestParseQuery_Negation(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantFilt   map[string][]string
		wantNeg    map[string][]string
		wantRemain string
	}{
		{
			name:     "positive label unchanged",
			in:       "label:memory",
			wantFilt: map[string][]string{"label": {"memory"}},
			wantNeg:  map[string][]string{},
		},
		{
			name:    "negated label",
			in:      "-label:memory",
			wantNeg: map[string][]string{"label": {"memory"}},
		},
		{
			name:     "positive and negated label coexist",
			in:       "label:weekly -label:memory",
			wantFilt: map[string][]string{"label": {"weekly"}},
			wantNeg:  map[string][]string{"label": {"memory"}},
		},
		{
			name: "negated scope glob, creator, type",
			in:   "-scope:topic:* -creator:bob@x.com -type:TEXT",
			wantNeg: map[string][]string{
				"scope":   {"topic:*"},
				"creator": {"bob@x.com"},
				"type":    {"TEXT"},
			},
		},
		{
			name:       "slug is not negatable — stays free text",
			in:         "-slug:foo",
			wantNeg:    map[string][]string{},
			wantRemain: "-slug:foo",
		},
		{
			name:       "unknown negated field stays free text",
			in:         "-bogus:x",
			wantNeg:    map[string][]string{},
			wantRemain: "-bogus:x",
		},
		{
			name:       "bare dash word is free text",
			in:         "-memory",
			wantNeg:    map[string][]string{},
			wantRemain: "-memory",
		},
		{
			name:       "empty negated value stays free text",
			in:         "-label:",
			wantNeg:    map[string][]string{},
			wantRemain: "-label:",
		},
		{
			name:       "negation mixed with free text",
			in:         "release -label:memory notes",
			wantNeg:    map[string][]string{"label": {"memory"}},
			wantRemain: "release notes",
		},
		{
			name:    "repeated negated label accumulates",
			in:      "-label:a -label:b",
			wantNeg: map[string][]string{"label": {"a", "b"}},
		},
		{
			name:     "positive content_type unchanged",
			in:       "content_type:text/markdown",
			wantFilt: map[string][]string{"content_type": {"text/markdown"}},
			wantNeg:  map[string][]string{},
		},
		{
			name:    "negated content_type",
			in:      "-content_type:text/markdown",
			wantNeg: map[string][]string{"content_type": {"text/markdown"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			filt, neg, rem := parseQuery(c.in)
			wantFilt := c.wantFilt
			if wantFilt == nil {
				wantFilt = map[string][]string{}
			}
			if !reflect.DeepEqual(filt, wantFilt) {
				t.Errorf("filters = %#v, want %#v", filt, wantFilt)
			}
			if !reflect.DeepEqual(neg, c.wantNeg) {
				t.Errorf("negated = %#v, want %#v", neg, c.wantNeg)
			}
			if rem != c.wantRemain {
				t.Errorf("remainder = %q, want %q", rem, c.wantRemain)
			}
		})
	}
}

// mergeFilters must fold the negated buckets into the ListInput's Not* slices
// so buildWhere can emit the exclusions. Slug negation is intentionally
// dropped on the floor (no NotSlug field exists).
func TestMergeFilters_Negation(t *testing.T) {
	var in pgstore.ListInput
	negated := map[string][]string{
		"label":   {"memory", "scratch"},
		"scope":   {"topic:*"},
		"creator": {"bob@x.com"},
		"type":    {"PACKAGE"},
	}
	mergeFilters(&in, map[string][]string{}, negated)

	if !reflect.DeepEqual(in.NotLabels, []string{"memory", "scratch"}) {
		t.Errorf("NotLabels = %#v", in.NotLabels)
	}
	if !reflect.DeepEqual(in.NotScope, []string{"topic:*"}) {
		t.Errorf("NotScope = %#v", in.NotScope)
	}
	if !reflect.DeepEqual(in.NotCreator, []string{"bob@x.com"}) {
		t.Errorf("NotCreator = %#v", in.NotCreator)
	}
	if !reflect.DeepEqual(in.NotArtifactType, []string{"PACKAGE"}) {
		t.Errorf("NotArtifactType = %#v", in.NotArtifactType)
	}
}

// mergeFilters must also fold a positive content_type:… token into
// ListInput.ContentType (single-value, first-wins, like type/creator/scope)
// and a negated one into NotContentType.
func TestMergeFilters_ContentType(t *testing.T) {
	var in pgstore.ListInput
	filters := map[string][]string{"content_type": {"text/markdown"}}
	negated := map[string][]string{"content_type": {"text/html"}}
	mergeFilters(&in, filters, negated)

	if in.ContentType == nil || *in.ContentType != "text/markdown" {
		t.Errorf("ContentType = %v, want text/markdown", in.ContentType)
	}
	if !reflect.DeepEqual(in.NotContentType, []string{"text/html"}) {
		t.Errorf("NotContentType = %#v", in.NotContentType)
	}
}
