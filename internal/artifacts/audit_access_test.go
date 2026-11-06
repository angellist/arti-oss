//go:build integration

package artifacts_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/audit"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func auditTargets(e *audit.Entry) []audit.Target {
	var ev audit.Event
	e.Fill(&ev)
	return ev.Targets
}

func TestAuditRecordsReadAndWriteDecisions(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	slug := uniqueSlug("audit-access")
	private := []string{"owner@example.com"}

	ctx, e := audit.WithEntry(context.Background())
	info, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "t", ContentType: "text/plain", Content: "body", AllowedAccess: &private,
	}, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if tg := auditTargets(e); len(tg) != 1 || tg[0].ArtifactID != info.ArtifactID || tg[0].Decision != "allowed" {
		t.Fatalf("create targets = %+v", tg)
	}

	id := uuid.MustParse(info.ArtifactID)
	ctx, e = audit.WithEntry(context.Background())
	if _, err := svc.Get(ctx, id, "stranger@example.com"); err == nil {
		t.Fatal("stranger read a private doc")
	}
	if tg := auditTargets(e); len(tg) != 1 || tg[0].ArtifactID != info.ArtifactID || tg[0].Decision != "denied" {
		t.Fatalf("denied get targets = %+v", tg)
	}

	ctx, e = audit.WithEntry(context.Background())
	if _, err := svc.GetBySlug(ctx, slug, nil, "stranger@example.com"); err == nil {
		t.Fatal("stranger read a private doc by slug")
	}
	if tg := auditTargets(e); len(tg) != 1 || tg[0].Slug != slug || tg[0].Decision != "denied" {
		t.Fatalf("denied slug targets = %+v", tg)
	}

	ctx, e = audit.WithEntry(context.Background())
	if _, err := svc.GetBySlug(ctx, slug, nil, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if tg := auditTargets(e); len(tg) == 0 || tg[0].ArtifactID != info.ArtifactID || tg[0].Decision != "allowed" {
		t.Fatalf("allowed slug targets = %+v", tg)
	}

	ctx, e = audit.WithEntry(context.Background())
	if _, _, err := svc.Append(ctx, slug, artifacts.AppendRequest{Content: "more"}, "stranger@example.com"); err == nil {
		t.Fatal("stranger appended to a private doc")
	}
	if tg := auditTargets(e); len(tg) == 0 || tg[len(tg)-1].Decision != "denied" {
		t.Fatalf("denied append targets = %+v", tg)
	}
}
