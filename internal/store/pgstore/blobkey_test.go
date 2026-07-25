package pgstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Attachments must be slugless + creator-only no matter what the caller (or
// the server's slug-inherit path) supplies — otherwise an attachment created
// under a catalog slug could inherit allowed_access=["*"] and leak.
func TestApplyAttachmentInvariants(t *testing.T) {
	slug := "some-catalog-slug"
	got := applyAttachmentInvariants(PutInput{
		ArtifactType:  TypeAttachment,
		NamedSlug:     &slug,
		AllowedAccess: []string{"*"}, // inherited / caller-supplied wide access
	})
	if got.NamedSlug != nil {
		t.Errorf("attachment NamedSlug = %v, want nil (slugless)", *got.NamedSlug)
	}
	if len(got.AllowedAccess) != 0 {
		t.Errorf("attachment AllowedAccess = %v, want [] (creator-only)", got.AllowedAccess)
	}

	// Non-attachments are untouched.
	text := applyAttachmentInvariants(PutInput{
		ArtifactType:  TypeText,
		NamedSlug:     &slug,
		AllowedAccess: []string{"*"},
	})
	if text.NamedSlug == nil || *text.NamedSlug != slug {
		t.Errorf("TEXT NamedSlug = %v, want %q (unchanged)", text.NamedSlug, slug)
	}
	if len(text.AllowedAccess) != 1 || text.AllowedAccess[0] != "*" {
		t.Errorf("TEXT AllowedAccess = %v, want [*] (unchanged)", text.AllowedAccess)
	}
}

// blobKey routes each artifact type to its own S3 prefix. Attachments land
// under attachments/ with no .zip suffix (that's PACKAGE-only).
func TestBlobKey_RoutesByType(t *testing.T) {
	s := &Store{}
	s.cfg.defaults()
	id := uuid.MustParse("12345678-90ab-cdef-1234-567890abcdef")

	cases := []struct {
		kind       string
		wantPrefix string
		zip        bool
	}{
		{TypeText, "artifacts/", false},
		{TypePackage, "packages/", true},
		{TypeAttachment, "attachments/", false},
	}
	for _, c := range cases {
		got := s.blobKey(c.kind, id)
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("%s: key %q missing prefix %q", c.kind, got, c.wantPrefix)
		}
		if hasZip := strings.HasSuffix(got, ".zip"); hasZip != c.zip {
			t.Errorf("%s: key %q .zip suffix = %v, want %v", c.kind, got, hasZip, c.zip)
		}
	}
}
