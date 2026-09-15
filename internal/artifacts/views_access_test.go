//go:build integration

package artifacts_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestViewStats_RecentIsOwnerOnly(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	access := []string{"*"}
	info, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: func() *string { s := uniqueSlug("views-access"); return &s }(),
		Title:     "views", ContentType: "text/plain", Content: "body",
		AllowedAccess: &access,
	}, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	id, err := uuid.Parse(info.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordViewForID(ctx, id, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	owner, err := svc.ViewStats(ctx, id, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !owner.CanSeeViewers || owner.Recent == nil || len(owner.Recent) != 1 {
		t.Fatalf("owner stats = %+v, want recent viewer row", owner)
	}
	reader, err := svc.ViewStats(ctx, id, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if reader.CanSeeViewers || reader.Recent != nil {
		t.Fatalf("reader stats = %+v, want nil unauthorized recent", reader)
	}
	if reader.Total != 1 || reader.UniqueViewers != 1 {
		t.Fatalf("reader aggregate = %+v", reader)
	}
}
