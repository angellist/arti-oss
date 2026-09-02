package artifacts

import (
	"errors"
	"testing"

	"github.com/angellist/arti-oss/gen/sqlc"
)

func v(n int32) *int32 { return &n }

func prevText(version int32) sqlc.Artifact {
	return sqlc.Artifact{
		ArtifactType: "TEXT",
		ContentType:  "text/markdown",
		Version:      v(version),
		Creator:      "someone@example.com",
	}
}

func TestVersionGuard_PassesAnUnchangedRepublish(t *testing.T) {
	req := CreateRequest{NamedSlug: strptr("notes"), ContentType: "text/markdown", ExpectedLatestVersion: v(7)}
	if err := versionGuard(req, "TEXT", prevText(7)); err != nil {
		t.Fatalf("same type, same base version: %v", err)
	}
}

// A content type only differing in its parameters is the same kind of
// document; charset drift must not read as a type change.
func TestVersionGuard_IgnoresContentTypeParameters(t *testing.T) {
	req := CreateRequest{NamedSlug: strptr("notes"), ContentType: "Text/Markdown; charset=utf-8"}
	if err := versionGuard(req, "TEXT", prevText(7)); err != nil {
		t.Fatalf("charset-only difference should not be a type change: %v", err)
	}
}

func TestVersionGuard_RejectsAStaleBase(t *testing.T) {
	req := CreateRequest{NamedSlug: strptr("notes"), ContentType: "text/markdown", ExpectedLatestVersion: v(7)}
	err := versionGuard(req, "TEXT", prevText(9))
	var stale staleBaseVersion
	if !errors.As(err, &stale) {
		t.Fatalf("want staleBaseVersion, got %v", err)
	}
	if stale.actual != 9 || stale.expected != 7 {
		t.Fatalf("want v7 → v9 in the error, got %+v", stale)
	}
}

// The base check runs first: when someone else has published, saying WHAT
// changed is guesswork until the caller has re-read the slug.
func TestVersionGuard_ReportsStalenessBeforeATypeChange(t *testing.T) {
	req := CreateRequest{NamedSlug: strptr("notes"), ContentType: "application/zip", ExpectedLatestVersion: v(7)}
	var stale staleBaseVersion
	if err := versionGuard(req, "PACKAGE", prevText(9)); !errors.As(err, &stale) {
		t.Fatalf("want staleBaseVersion, got %v", err)
	}
}

func TestVersionGuard_RejectsAnArtifactTypeChange(t *testing.T) {
	req := CreateRequest{NamedSlug: strptr("notes"), ContentType: "application/zip"}
	var tc typeChange
	err := versionGuard(req, "PACKAGE", prevText(7))
	if !errors.As(err, &tc) {
		t.Fatalf("want typeChange, got %v", err)
	}
	if tc.from != "TEXT (text/markdown)" || tc.to != "PACKAGE (application/zip)" {
		t.Fatalf("error should name both sides, got %+v", tc)
	}
}

// The one that shipped a broken page: an HTML document republished as
// markdown renders as source, and nothing said so.
func TestVersionGuard_RejectsAContentTypeChangeWithinTEXT(t *testing.T) {
	prev := prevText(3)
	prev.ContentType = "text/html"
	req := CreateRequest{NamedSlug: strptr("mock"), ContentType: "text/markdown"}
	var tc typeChange
	if err := versionGuard(req, "TEXT", prev); !errors.As(err, &tc) {
		t.Fatalf("want typeChange for text/html → text/markdown, got %v", err)
	}
}

func TestVersionGuard_AllowsATypeChangeWhenAsked(t *testing.T) {
	req := CreateRequest{NamedSlug: strptr("notes"), ContentType: "application/zip", AllowTypeChange: true}
	if err := versionGuard(req, "PACKAGE", prevText(7)); err != nil {
		t.Fatalf("allow_type_change should permit the change: %v", err)
	}
}

// allow_type_change is scoped to the type, not to the race: it must not also
// wave through a publish built on a version that no longer exists.
func TestVersionGuard_AllowTypeChangeDoesNotWaiveTheBaseCheck(t *testing.T) {
	req := CreateRequest{
		NamedSlug:             strptr("notes"),
		ContentType:           "application/zip",
		AllowTypeChange:       true,
		ExpectedLatestVersion: v(7),
	}
	var stale staleBaseVersion
	if err := versionGuard(req, "PACKAGE", prevText(9)); !errors.As(err, &stale) {
		t.Fatalf("want staleBaseVersion, got %v", err)
	}
}

func strptr(s string) *string { return &s }
