//go:build integration

package embed_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/embed"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("ARTI_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable"
	}
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestPGPendingTokenStore(t *testing.T) {
	st := embed.NewPGPendingTokenStore(pool(t))
	ctx := context.Background()

	// Miss on an unknown key.
	if tok, err := st.Take(ctx, "s", "missing"); err != nil || tok != "" {
		t.Fatalf("miss: got (%q,%v)", tok, err)
	}

	// Put then Take once; second Take is empty (single-use).
	if err := st.Put(ctx, "panel", "n1", "TOK-1", "e@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	if tok, err := st.Take(ctx, "panel", "n1"); err != nil || tok != "TOK-1" {
		t.Fatalf("take: got (%q,%v)", tok, err)
	}
	if tok, _ := st.Take(ctx, "panel", "n1"); tok != "" {
		t.Fatal("second take must be empty (single-use)")
	}

	// Put overwrites a prior pending token for the same key.
	_ = st.Put(ctx, "panel", "n2", "OLD", "e@example.com", time.Minute)
	_ = st.Put(ctx, "panel", "n2", "NEW", "e@example.com", time.Minute)
	if tok, _ := st.Take(ctx, "panel", "n2"); tok != "NEW" {
		t.Fatalf("put must overwrite; got %q", tok)
	}

	// Expired rows are not returned.
	_ = st.Put(ctx, "panel", "n3", "EXP", "e@example.com", -time.Second)
	if tok, _ := st.Take(ctx, "panel", "n3"); tok != "" {
		t.Fatal("expired token must not be returned")
	}
}
