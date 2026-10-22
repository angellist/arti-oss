package pgstore

import "testing"

func slugPtr(s string) *string { return &s }

// The chat-upload case: couch posts a file with no slug and no ACL, and it
// comes out slugless and creator-only. A list inherited from a named slug is
// clamped the same way; only a list the caller itself passed is kept.
func TestAttachmentInvariants_ClampASluglessUpload(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   PutInput
		want []string
	}{
		{"omitted", PutInput{ArtifactType: TypeAttachment}, []string{}},
		{"inherited", PutInput{ArtifactType: TypeAttachment, AllowedAccess: []string{"*"}, InheritAccess: true, InheritWrite: true}, []string{}},
		{"explicit", PutInput{ArtifactType: TypeAttachment, AllowedAccess: []string{"alice@example.com", "couch@sa.arti.invalid"}}, []string{"alice@example.com", "couch@sa.arti.invalid"}},
	} {
		got := applyAttachmentInvariants(tc.in)
		if got.NamedSlug != nil {
			t.Fatalf("%s: want no slug, got %q", tc.name, *got.NamedSlug)
		}
		if len(got.AllowedAccess) != len(tc.want) || got.AllowedWrite == nil || len(got.AllowedWrite) != 0 {
			t.Fatalf("%s: access=%v write=%v, want access=%v and creator-only writes", tc.name, got.AllowedAccess, got.AllowedWrite, tc.want)
		}
	}
}

// A slug alone is not enough: a service credential naming one still gets the
// clamp (the service layer refuses that request before reaching here, and this
// is the backstop if a new caller ever doesn't).
func TestAttachmentInvariants_ClampASlugItWasNotToldToKeep(t *testing.T) {
	got := applyAttachmentInvariants(PutInput{
		ArtifactType:  TypeAttachment,
		NamedSlug:     slugPtr("kyc-pack"),
		AllowedAccess: []string{"*"},
	})
	if got.NamedSlug != nil {
		t.Fatalf("want the slug dropped, got %q", *got.NamedSlug)
	}
}

// A person publishing a binary under a slug they named is publishing a
// document: it keeps the slug (so it versions) and its access is whatever the
// document's is, not creator-only.
func TestAttachmentInvariants_KeepAnInteractiveSluggedUpload(t *testing.T) {
	got := applyAttachmentInvariants(PutInput{
		ArtifactType:       TypeAttachment,
		NamedSlug:          slugPtr("quarterly-report"),
		KeepAttachmentSlug: true,
		AllowedAccess:      []string{"*"},
	})
	if got.NamedSlug == nil || *got.NamedSlug != "quarterly-report" {
		t.Fatalf("want the slug kept, got %v", got.NamedSlug)
	}
	if len(got.AllowedAccess) != 1 || got.AllowedAccess[0] != "*" {
		t.Fatalf("want the document's access untouched, got %v", got.AllowedAccess)
	}
}

// The flag can't conjure a slug: with none named there is nothing to keep, and
// the clamp still applies.
func TestAttachmentInvariants_KeepFlagWithoutASlugStillClamps(t *testing.T) {
	got := applyAttachmentInvariants(PutInput{
		ArtifactType:       TypeAttachment,
		KeepAttachmentSlug: true,
		AllowedAccess:      []string{"*"},
		InheritAccess:      true,
	})
	if got.NamedSlug != nil || len(got.AllowedAccess) != 0 {
		t.Fatalf("want the clamp, got slug=%v access=%v", got.NamedSlug, got.AllowedAccess)
	}
}

func TestAttachmentInvariants_LeaveOtherTypesAlone(t *testing.T) {
	in := PutInput{ArtifactType: TypeText, NamedSlug: slugPtr("notes"), AllowedAccess: []string{"*"}}
	got := applyAttachmentInvariants(in)
	if got.NamedSlug == nil || len(got.AllowedAccess) != 1 {
		t.Fatalf("TEXT must pass through untouched, got %+v", got)
	}
}
