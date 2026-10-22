//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

type actingRig struct {
	svc       *artifacts.Service
	st        *pgstore.Store
	principal string
}

// newActingRig sets up a service principal holding ASSERT_ACTOR, and an
// allowlist that admits example.com people.
func newActingRig(t *testing.T) actingRig {
	t.Helper()
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	principal := strings.ToLower(uniqueSlug("couch")) + "@sa.arti.invalid"
	if _, err := st.AddUser(ctx, principal, pgstore.UserKindService, "test principal", "admin@example.com"); err != nil {
		t.Fatalf("add principal: %v", err)
	}
	role := "acting-" + uniqueSlug("r")
	if _, err := st.CreateRole(ctx, role, "test", []string{string(rbac.AssertActor)}); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := st.AssignRole(ctx, rbac.PrincipalUser, principal, role, "test"); err != nil {
		t.Fatalf("assign role: %v", err)
	}
	prev := auth.AllowedEmails()
	t.Cleanup(func() { auth.SetAllowedEmails(prev) })
	auth.SetAllowedEmails([]string{"example.com"})
	return actingRig{svc: artifacts.NewService(st, "http://localhost", nil, nil), st: st, principal: principal}
}

func (r actingRig) publishFor(t *testing.T, person, slug string, access []string) (artifacts.ArtifactInfo, error) {
	t.Helper()
	ctx := auth.WithTestActingFor(context.Background(), person)
	req := artifacts.CreateRequest{Title: "note", ContentType: "text/markdown", Content: "body"}
	if slug != "" {
		req.NamedSlug = &slug
	}
	if access != nil {
		req.AllowedAccess = &access
	}
	return r.svc.Create(ctx, req, r.principal)
}

// The person is credited as creator; the principal is recorded, owns the slug
// it opened, and can read the version back although only the person was named.
func TestActingForCreditsThePersonAndKeepsThePrincipalInCharge(t *testing.T) {
	r := newActingRig(t)
	ctx := context.Background()
	slug := uniqueSlug("mem")
	info, err := r.publishFor(t, "alice@example.com", slug, []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("acting-for create: %v", err)
	}
	if info.Creator != "alice@example.com" {
		t.Fatalf("creator = %q, want the person", info.Creator)
	}
	got, err := r.svc.Get(ctx, mustUUID(t, info.ArtifactID), r.principal)
	if err != nil {
		t.Fatalf("the principal must read what it wrote: %v", err)
	}
	if got.Owner == nil || *got.Owner != r.principal {
		t.Fatalf("owner = %v, want the principal", got.Owner)
	}
	// A second person's version on the same slug: the principal maintains it.
	if _, err := r.publishFor(t, "bob@example.com", slug, []string{"bob@example.com"}); err != nil {
		t.Fatalf("second person's version: %v", err)
	}
}

// Credit is not authority: the person can read their version but not edit or
// archive it, so they cannot forge a record couch keeps.
func TestCreditedPersonCannotEditOrArchiveTheVersion(t *testing.T) {
	r := newActingRig(t)
	ctx := context.Background()
	info, err := r.publishFor(t, "alice@example.com", uniqueSlug("log"), []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("acting-for create: %v", err)
	}
	id := mustUUID(t, info.ArtifactID)
	if _, err := r.svc.Get(ctx, id, "alice@example.com"); err != nil {
		t.Fatalf("the credited person must read the version: %v", err)
	}
	title := "forged"
	if _, err := r.svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &title}, "alice@example.com"); err == nil {
		t.Fatal("the credited person edited a version the principal wrote")
	}
	if ok, err := r.svc.CanArchiveID(ctx, "alice@example.com", id); err != nil || ok {
		t.Fatalf("CanArchiveID = %v, %v; the credited person may not archive the version", ok, err)
	}
	if creator, err := r.svc.CreatorOf(ctx, id); err != nil || creator != "" {
		t.Fatalf("CreatorOf = %q, %v; want no creator authority for an asserted version", creator, err)
	}
}

// Only a service principal with ASSERT_ACTOR may make the claim, and it must
// name an allowed person who is not a service principal.
func TestActingForRefusals(t *testing.T) {
	r := newActingRig(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, caller, person string
	}{
		{"a person may not claim", "carol@example.com", "alice@example.com"},
		{"a person outside the allowlist", r.principal, "mallory@elsewhere.test"},
		{"a service principal as the person", r.principal, r.principal},
	} {
		slug := uniqueSlug("refuse")
		_, err := r.svc.Create(auth.WithTestActingFor(ctx, tc.person), artifacts.CreateRequest{
			NamedSlug: &slug, Title: "x", ContentType: "text/markdown", Content: "x",
		}, tc.caller)
		if err == nil {
			t.Fatalf("%s: the write succeeded", tc.name)
		}
	}
	// An admin person holds ASSERT_ACTOR through ADMIN, and is still refused:
	// only a service principal may make the claim.
	adminPerson := "boss-" + strings.ToLower(uniqueSlug("a")) + "@example.com"
	if err := r.st.AssignRole(ctx, rbac.PrincipalUser, adminPerson, rbac.RoleAdmin, "test"); err != nil {
		t.Fatalf("assign admin: %v", err)
	}
	adminSlug := uniqueSlug("admin-claim")
	if _, err := r.svc.Create(auth.WithTestActingFor(ctx, "alice@example.com"), artifacts.CreateRequest{
		NamedSlug: &adminSlug, Title: "x", ContentType: "text/markdown", Content: "x",
	}, adminPerson); err == nil {
		t.Fatal("an admin person made the acting-for claim")
	}
	other := strings.ToLower(uniqueSlug("nogrant")) + "@sa.arti.invalid"
	if _, err := r.st.AddUser(ctx, other, pgstore.UserKindService, "", "admin@example.com"); err != nil {
		t.Fatalf("add principal: %v", err)
	}
	slug := uniqueSlug("nogrant")
	if _, err := r.svc.Create(auth.WithTestActingFor(ctx, "alice@example.com"), artifacts.CreateRequest{
		NamedSlug: &slug, Title: "x", ContentType: "text/markdown", Content: "x",
	}, other); err == nil {
		t.Fatal("a service principal without ASSERT_ACTOR made the claim")
	}
}

// The person's roles never authorize the principal: acting for an admin does
// not let the principal version a document only that admin may write.
func TestActingForNeverBorrowsThePersonsRoles(t *testing.T) {
	r := newActingRig(t)
	ctx := context.Background()
	admin := "root-" + strings.ToLower(uniqueSlug("a")) + "@example.com"
	if err := r.st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatalf("assign admin: %v", err)
	}
	slug := uniqueSlug("admins-doc")
	none := []string{}
	if _, err := r.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "x", ContentType: "text/markdown", Content: "x", AllowedWrite: &none,
	}, admin); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := r.publishFor(t, admin, slug, nil)
	if !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("principal versioning an admin-only doc while acting for the admin: err = %v, want ErrNotFound", err)
	}
}

// A slugless upload the principal stores for a person keeps the readers it was
// given plus the principal, so couch can read it back without admin rights.
func TestActingForAttachmentIsReadableByThePrincipal(t *testing.T) {
	r := newActingRig(t)
	ctx := auth.WithTestActingFor(context.Background(), "alice@example.com")
	access := []string{"alice@example.com"}
	info, err := r.svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: pgstore.TypeAttachment, Title: "chart.png", ContentType: "image/png",
		ContentBase64: "iVBORw0KGgo=", AllowedAccess: &access,
	}, r.principal)
	if err != nil {
		t.Fatalf("acting-for attachment: %v", err)
	}
	if _, err := r.svc.Get(context.Background(), mustUUID(t, info.ArtifactID), r.principal); err != nil {
		t.Fatalf("principal reading the attachment back: %v", err)
	}
	if _, err := r.svc.Get(context.Background(), mustUUID(t, info.ArtifactID), "bob@example.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("a stranger read the attachment: err = %v", err)
	}
}

// A slug whose versions are all archived is reopened on a person's behalf only
// by its owner.
func TestActingForDoesNotReopenSomeoneElsesArchivedSlug(t *testing.T) {
	r := newActingRig(t)
	ctx := context.Background()
	slug := uniqueSlug("archived")
	if _, err := r.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "x", ContentType: "text/markdown", Content: "x",
	}, "dave@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := r.svc.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := r.publishFor(t, "alice@example.com", slug, nil); err == nil {
		t.Fatal("the principal reopened someone else's archived slug")
	}
}

// On a document the principal may write but does not own, an acting-for version
// leaves the document's ACL alone: adding the principal to its readers would be
// an ACL change only the owner may make.
func TestActingForOnSomeoneElsesDocLeavesItsACL(t *testing.T) {
	r := newActingRig(t)
	ctx := context.Background()
	slug := uniqueSlug("shared")
	group := "writers-" + strings.ToLower(slug)
	// The principal writes through a group, so it is not named among the readers.
	if _, err := r.st.CreateGroup(ctx, group, group, []string{r.principal}, "dave@example.com"); err != nil {
		t.Fatalf("create group: %v", err)
	}
	access := []string{"dave@example.com", "erin@example.com"}
	writers := []string{"group:" + group}
	if _, err := r.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "x", ContentType: "text/markdown", Content: "x",
		AllowedAccess: &access, AllowedWrite: &writers,
	}, "dave@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info, err := r.publishFor(t, "alice@example.com", slug, nil)
	if err != nil {
		t.Fatalf("acting-for version on a writable doc: %v", err)
	}
	got, err := r.svc.Get(ctx, mustUUID(t, info.ArtifactID), "dave@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.AllowedWrite) != 1 || got.Owner == nil || *got.Owner != "dave@example.com" {
		t.Fatalf("write=%v owner=%v, want the document's ACL and owner unchanged", got.AllowedWrite, got.Owner)
	}
	for _, a := range got.AllowedAccess {
		if strings.EqualFold(a, r.principal) || a == "*" {
			t.Fatalf("access changed to %v without the owner", got.AllowedAccess)
		}
	}
}

// A slug the principal opened for a person is the principal's, so it may share
// it; the credited person may not.
func TestActingForSlugIsShareableByThePrincipal(t *testing.T) {
	r := newActingRig(t)
	r.svc.SetShareConfig(artifacts.ShareConfig{Enabled: true, MaxTTL: 720 * time.Hour})
	ctx := context.Background()
	info, err := r.publishFor(t, "alice@example.com", uniqueSlug("shared-note"), []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("acting-for create: %v", err)
	}
	if _, err := r.svc.MintShare(ctx, info.ArtifactID, r.principal,
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); err != nil {
		t.Fatalf("the owning principal minting a share link: %v", err)
	}
	if _, err := r.svc.MintShare(ctx, info.ArtifactID, "alice@example.com",
		artifacts.MintShareRequest{Scope: "version", TTL: "1h"}); !artifacts.IsForbidden(err) {
		t.Fatalf("the credited person minting a share link: err = %v, want forbidden", err)
	}
}
