//go:build integration

package artifacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/comments"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

type orgReadRig struct {
	svc            *artifacts.Service
	comments       *comments.Service
	reader, other  string
	person         string
	shared, narrow artifacts.ArtifactInfo
}

// newOrgReadRig sets up two service principals, only `reader` holding
// READ_ORG_SHARED, and two docs by alice: one shared with *@example.com, one
// with alice alone.
func newOrgReadRig(t *testing.T) orgReadRig {
	t.Helper()
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	st.SetOrgDomains(auth.AllowedEmails)
	prev := auth.AllowedEmails()
	t.Cleanup(func() { auth.SetAllowedEmails(prev) })
	auth.SetAllowedEmails([]string{"example.com", "other.org", "solo@third.org"})

	r := orgReadRig{
		svc:      artifacts.NewService(st, "http://localhost", nil, nil),
		comments: comments.NewService(pool, st, auth.NewJWTSigner([]byte("test-secret"))),
		reader:   strings.ToLower(uniqueSlug("reader")) + "@sa.arti.invalid",
		other:    strings.ToLower(uniqueSlug("other")) + "@sa.arti.invalid",
		person:   strings.ToLower(uniqueSlug("bob")) + "@other.org",
	}
	for _, p := range []string{r.reader, r.other} {
		if _, err := st.AddUser(ctx, p, pgstore.UserKindService, "test principal", "admin@example.com"); err != nil {
			t.Fatalf("add principal: %v", err)
		}
	}
	role := "org-reader-" + uniqueSlug("r")
	if _, err := st.CreateRole(ctx, role, "test", []string{string(rbac.ReadOrgShared)}); err != nil {
		t.Fatalf("create role: %v", err)
	}
	for _, p := range []string{r.reader, r.person} {
		if err := st.AssignRole(ctx, rbac.PrincipalUser, p, role, "test"); err != nil {
			t.Fatalf("assign role: %v", err)
		}
	}
	create := func(slug string, access []string) artifacts.ArtifactInfo {
		info, err := r.svc.Create(ctx, artifacts.CreateRequest{
			NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "body",
			AllowedAccess: &access,
		}, "alice@example.com")
		if err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
		return info
	}
	r.shared = create(uniqueSlug("org"), []string{"*@example.com"})
	r.narrow = create(uniqueSlug("solo"), []string{"alice@example.com"})
	return r
}

func (r orgReadRig) listSlugs(t *testing.T, caller string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	tokens, err := r.svc.ReadTokens(ctx, caller)
	if err != nil {
		t.Fatalf("read tokens: %v", err)
	}
	res, err := r.svc.List(ctx, pgstore.ListInput{Limit: 200, CallerEmail: caller, CallerGroups: tokens, Creator: strPtr("alice@example.com")})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := map[string]bool{}
	for _, a := range res.Artifacts {
		if a.NamedSlug != nil {
			out[*a.NamedSlug] = true
		}
	}
	return out
}

// A service principal's .invalid address matches no domain pattern, so without
// READ_ORG_SHARED it cannot see what every employee can.
func TestOrgSharedReadSeesOrgWideDocsOnly(t *testing.T) {
	r := newOrgReadRig(t)
	ctx := context.Background()
	shared, narrow := *r.shared.NamedSlug, *r.narrow.NamedSlug

	if _, err := r.svc.GetBySlug(ctx, shared, nil, r.reader); err != nil {
		t.Fatalf("holder must read an org-wide doc by slug: %v", err)
	}
	if _, err := r.svc.Get(ctx, mustUUID(t, r.shared.ArtifactID), r.reader); err != nil {
		t.Fatalf("holder must read an org-wide doc by id: %v", err)
	}
	if got := r.listSlugs(t, r.reader); !got[shared] || got[narrow] {
		t.Fatalf("holder list = %v; want %s and not %s", got, shared, narrow)
	}
	if _, err := r.svc.GetBySlug(ctx, narrow, nil, r.reader); err == nil {
		t.Fatal("holder read a doc shared with one person")
	}

	if _, err := r.svc.GetBySlug(ctx, shared, nil, r.other); err == nil {
		t.Fatal("a principal without READ_ORG_SHARED read an org-wide doc")
	}
	if got := r.listSlugs(t, r.other); got[shared] {
		t.Fatal("a principal without READ_ORG_SHARED listed an org-wide doc")
	}

	tokens, err := r.svc.ReadTokens(ctx, r.reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if strings.Contains(tok, "third.org") {
			t.Fatalf("a full-address allowlist entry became a domain token: %v", tokens)
		}
	}
}

// The doc's write list follows its read list, so a read grant that leaked into
// the write check would let couch-sa rewrite every org-wide doc.
func TestOrgSharedReadGrantsNoWrite(t *testing.T) {
	r := newOrgReadRig(t)
	ctx := context.Background()
	id := mustUUID(t, r.shared.ArtifactID)

	info, err := r.svc.Get(ctx, id, r.reader)
	if err != nil {
		t.Fatal(err)
	}
	if info.CanWrite == nil || *info.CanWrite {
		t.Fatalf("CanWrite = %v, want false", info.CanWrite)
	}
	slug := *r.shared.NamedSlug
	if _, err := r.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "forged", ContentType: "text/markdown", Content: "x",
	}, r.reader); err == nil {
		t.Fatal("holder wrote a new version of an org-wide doc")
	}
	if _, _, err := r.svc.Append(ctx, slug, artifacts.AppendRequest{Content: "x"}, r.reader); err == nil {
		t.Fatal("holder appended to an org-wide doc")
	}
	title := "forged"
	if _, err := r.svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &title}, r.reader); err == nil {
		t.Fatal("holder edited an org-wide doc's metadata")
	}
	if ok, err := r.svc.CanArchiveID(ctx, r.reader, id); err != nil || ok {
		t.Fatalf("CanArchiveID = %v, %v; want false", ok, err)
	}
	if _, err := r.comments.AddForCaller(ctx, id, r.reader, comments.AddInput{Body: "x"}); err == nil {
		t.Fatal("holder posted a comment on an org-wide doc")
	}
}

// For a person the permission would read across companies: bob@other.org would
// see what example.com shared with itself.
func TestOrgSharedReadGivesAPersonNothing(t *testing.T) {
	r := newOrgReadRig(t)
	if _, err := r.svc.GetBySlug(context.Background(), *r.shared.NamedSlug, nil, r.person); err == nil {
		t.Fatal("a person holding READ_ORG_SHARED read another company's org-wide doc")
	}
	if got := r.listSlugs(t, r.person); got[*r.shared.NamedSlug] {
		t.Fatal("a person holding READ_ORG_SHARED listed another company's org-wide doc")
	}
}
