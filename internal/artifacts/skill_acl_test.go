//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func skillSvc(t *testing.T) *artifacts.Service {
	t.Helper()
	return artifacts.NewService(pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{}), "http://localhost", nil, nil)
}

func publish(t *testing.T, svc *artifacts.Service, slug, caller string, labels []string, write *[]string) artifacts.ArtifactInfo {
	t.Helper()
	info, err := svc.Create(context.Background(), artifacts.CreateRequest{
		NamedSlug: &slug, Title: slug, ContentType: "text/markdown", Content: "body",
		Labels: labels, AllowedWrite: write,
	}, caller)
	if err != nil {
		t.Fatalf("publish %s as %s: %v", slug, caller, err)
	}
	return info
}

// A skill is a prompt other agents load, so a new one is writable by its owner
// alone unless the publisher names writers; any other doc keeps writers-follow-
// readers. Both spellings of the label count.
func TestSkillSlugDefaultsToOwnerOnlyWrites(t *testing.T) {
	svc := skillSvc(t)
	for _, label := range []string{"skill", "kind:skill"} {
		got := publish(t, svc, uniqueSlug("skill-default"), "alice@example.com", []string{label}, nil)
		if got.AllowedWrite == nil || len(got.AllowedWrite) != 0 {
			t.Fatalf("%s: allowed_write = %v, want [] (owner only)", label, got.AllowedWrite)
		}
	}
	plain := publish(t, svc, uniqueSlug("plain-default"), "alice@example.com", []string{"notes"}, nil)
	if plain.AllowedWrite != nil {
		t.Fatalf("non-skill allowed_write = %v, want nil (writers follow readers)", plain.AllowedWrite)
	}
	named := []string{"bob@example.com"}
	explicit := publish(t, svc, uniqueSlug("skill-named"), "alice@example.com", []string{"skill"}, &named)
	if len(explicit.AllowedWrite) != 1 || explicit.AllowedWrite[0] != "bob@example.com" {
		t.Fatalf("an explicit write list must be kept; got %v", explicit.AllowedWrite)
	}
}

// Skill writes follow the document's write list. No role is needed: a non-admin
// owner and a named writer version it, a reader cannot.
func TestSkillWritesFollowTheDocumentACL(t *testing.T) {
	svc := skillSvc(t)
	ctx := context.Background()
	slug := uniqueSlug("skill-acl")
	writers := []string{"bob@example.com"}
	publish(t, svc, slug, "alice@example.com", []string{"skill"}, &writers)

	publish(t, svc, slug, "alice@example.com", nil, nil)
	publish(t, svc, slug, "bob@example.com", nil, nil)
	_, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: slug, ContentType: "text/markdown", Content: "hijack",
	}, "carol@example.com")
	if !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("a reader versioning a skill: err = %v, want ErrNotFound", err)
	}
}

// Archiving a skill's latest version rolls the skill back, so it needs current
// write access as well as the usual creator/owner authority. A creator who lost
// write access cannot archive their old skill version; on a plain doc they still
// can.
func TestArchivingASkillNeedsCurrentWriteAccess(t *testing.T) {
	svc := skillSvc(t)
	ctx := context.Background()
	for _, tc := range []struct {
		label     string
		wantAllow bool
	}{
		{"skill", false},
		{"notes", true},
	} {
		slug := uniqueSlug("archive-" + tc.label)
		writers := []string{"bob@example.com"}
		publish(t, svc, slug, "alice@example.com", []string{tc.label}, &writers)
		bobs := publish(t, svc, slug, "bob@example.com", nil, nil)
		// Alice, the owner, removes Bob from the writers.
		none := []string{}
		if _, err := svc.UpdateMetadata(ctx, mustUUID(t, bobs.ArtifactID), artifacts.UpdateMetadataRequest{AllowedWrite: &none}, "alice@example.com"); err != nil {
			t.Fatalf("%s: owner narrows writers: %v", tc.label, err)
		}
		allowed, err := svc.CanArchiveID(ctx, "bob@example.com", mustUUID(t, bobs.ArtifactID))
		if err != nil {
			t.Fatalf("%s: CanArchiveID: %v", tc.label, err)
		}
		if allowed != tc.wantAllow {
			t.Fatalf("%s: bob archiving his own version after losing write: allowed = %v, want %v", tc.label, allowed, tc.wantAllow)
		}
		if ok, err := svc.CanArchiveID(ctx, "alice@example.com", mustUUID(t, bobs.ArtifactID)); err != nil || !ok {
			t.Fatalf("%s: the owner archiving a version someone else wrote: allowed = %v err = %v", tc.label, ok, err)
		}
	}
}

// The owner archives a slug whose versions others wrote, and still sees the
// archived versions afterwards so it can restore them.
func TestOwnerArchivesTheSlugAndSeesArchivedVersions(t *testing.T) {
	svc := skillSvc(t)
	ctx := context.Background()
	slug := uniqueSlug("owner-archive")
	writers := []string{"bob@example.com"}
	publish(t, svc, slug, "alice@example.com", []string{"notes"}, &writers)
	bobs := publish(t, svc, slug, "bob@example.com", nil, nil)

	allowed, exists, err := svc.CanArchiveSlug(ctx, "alice@example.com", slug)
	if err != nil || !exists || !allowed {
		t.Fatalf("owner archiving a slug with someone else's version: allowed=%v exists=%v err=%v", allowed, exists, err)
	}
	if allowed, _, _ := svc.CanArchiveSlug(ctx, "carol@example.com", slug); allowed {
		t.Fatal("a stranger may not archive the slug")
	}
	if _, err := svc.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := svc.Get(ctx, mustUUID(t, bobs.ArtifactID), "alice@example.com"); err != nil {
		t.Fatalf("the owner must see an archived version someone else wrote: %v", err)
	}
	if _, err := svc.Get(ctx, mustUUID(t, bobs.ArtifactID), "carol@example.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("a stranger must not see archived versions: err = %v", err)
	}
	if ok, err := svc.CanArchiveID(ctx, "alice@example.com", mustUUID(t, bobs.ArtifactID)); err != nil || !ok {
		t.Fatalf("the owner may unarchive: allowed = %v err = %v", ok, err)
	}
}

// Editing a skill's metadata takes the same authority as any doc's: its owner,
// with no role.
func TestSkillMetadataNeedsNoRole(t *testing.T) {
	svc := skillSvc(t)
	info := publish(t, svc, uniqueSlug("skill-meta"), "alice@example.com", []string{"skill"}, nil)
	title := "renamed"
	if _, err := svc.UpdateMetadata(context.Background(), mustUUID(t, info.ArtifactID), artifacts.UpdateMetadataRequest{Title: &title}, "alice@example.com"); err != nil {
		t.Fatalf("owner retitling their skill: %v", err)
	}
}

// The viewer's Archive button reads can_archive, which must be the server's own
// decision: true for the owner on a version someone else wrote.
func TestCanArchiveFlagMatchesTheServerRule(t *testing.T) {
	svc := skillSvc(t)
	slug := uniqueSlug("can-archive-flag")
	writers := []string{"bob@example.com"}
	publish(t, svc, slug, "alice@example.com", []string{"notes"}, &writers)
	bobs := publish(t, svc, slug, "bob@example.com", nil, nil)
	got, err := svc.Get(context.Background(), mustUUID(t, bobs.ArtifactID), "alice@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CanArchive == nil || !*got.CanArchive {
		t.Fatalf("can_archive for the owner = %v, want true", got.CanArchive)
	}
}

// Append opens a slug too, so a skill created by an append is owner-only as well.
func TestSkillCreatedByAppendDefaultsToOwnerOnlyWrites(t *testing.T) {
	svc := skillSvc(t)
	slug := uniqueSlug("skill-append")
	info, _, err := svc.Append(context.Background(), slug, artifacts.AppendRequest{
		Content: "body", ContentType: "text/markdown", Title: &slug, Labels: []string{"skill"},
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("append-create: %v", err)
	}
	if info.AllowedWrite == nil || len(info.AllowedWrite) != 0 {
		t.Fatalf("allowed_write = %v, want [] (owner only)", info.AllowedWrite)
	}
}
