//go:build integration

package pgstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// UpsertIdPGroups normalizes + dedupes group names and lowercases the email;
// CallerGroups then returns idp:<name> tokens for a fresh snapshot, and drops
// them once the snapshot ages past the configured bound (fail closed).
func TestIdPGroups_UpsertResolveAndFreshness(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{IdPGroupsMaxAge: time.Hour})

	email := unique("idp-user") + "@example.com"
	if err := st.UpsertIdPGroups(ctx, email, []string{"Engineering", "engineering", "  ", "Platform"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	toks, err := st.CallerGroups(ctx, email)
	if err != nil {
		t.Fatalf("caller groups: %v", err)
	}
	if !contains(toks, "idp:engineering") || !contains(toks, "idp:platform") {
		t.Fatalf("want idp:engineering + idp:platform (lowercased, deduped), got %v", toks)
	}

	// A zero bound disables idp resolution entirely (fail closed).
	stOff := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	off, err := stOff.CallerGroups(ctx, email)
	if err != nil {
		t.Fatalf("caller groups (disabled): %v", err)
	}
	if contains(off, "idp:engineering") {
		t.Fatalf("zero IdPGroupsMaxAge must disable idp resolution, got %v", off)
	}

	// A tiny freshness bound ages the snapshot out → no idp tokens.
	stStale := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{IdPGroupsMaxAge: time.Nanosecond})
	stale, err := stStale.CallerGroups(ctx, email)
	if err != nil {
		t.Fatalf("caller groups (stale): %v", err)
	}
	if contains(stale, "idp:engineering") {
		t.Fatalf("stale snapshot must yield no idp tokens, got %v", stale)
	}
}
