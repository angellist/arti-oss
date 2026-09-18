//go:build integration

package pgstore

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Three matchers decide the same question: matchGlob for single-row reads,
// blockLike for the catalog and facet SQL, blockedSlugTx for the write path
// inside the slug lock. A divergence lets the catalog list a document the
// single-row reads have made absent, or lets a write land under a blocked
// slug. They are checked against one table of cases for that reason.
func TestBlockMatchers_Agree(t *testing.T) {
	url := os.Getenv("ARTI_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	cases := []struct {
		pattern string
		key     string
		want    bool
	}{
		{"exact-doc", "exact-doc", true},
		{"exact-doc", "exact-docs", false},
		{"exact-doc", "EXACT-DOC", true},
		{"fam-*", "fam-one", true},
		{"fam-*", "fam-", true},
		{"fam-*", "famous", false},
		{"*-secret", "q1-secret", true},
		{"*", "anything", true},
		// A literal SQL wildcard in a pattern is a literal, not a wildcard.
		{"a%b", "axb", false},
		{"a%b", "a%b", true},
		{"a_b", "axb", false},
		{"a_b", "a_b", true},
		// `?` is one character, in every matcher.
		{"secret-?", "secret-1", true},
		{"secret-?", "secret-12", false},
		{"secret-?", "secret-", false},
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // never commit: the table is shared

	for _, c := range cases {
		if _, err := tx.Exec(ctx,
			`INSERT INTO artifact_blocks (pattern, created_by) VALUES ($1, 'test')
			 ON CONFLICT (pattern) DO NOTHING`, c.pattern); err != nil {
			t.Fatal(err)
		}
		got, err := blockedSlugTx(ctx, tx, c.key)
		if err != nil {
			t.Fatalf("blockedSlugTx(%q, %q): %v", c.pattern, c.key, err)
		}
		if got != c.want {
			t.Errorf("SQL: pattern %q vs key %q = %v, want %v", c.pattern, c.key, got, c.want)
		}
		if inGo := matchGlob(strings.ToLower(c.pattern), strings.ToLower(c.key)); inGo != c.want {
			t.Errorf("Go: pattern %q vs key %q = %v, want %v", c.pattern, c.key, inGo, c.want)
		}
		var viaLike bool
		if err := tx.QueryRow(ctx, `SELECT $1::text ILIKE $2::text`, c.key, blockLike(c.pattern)).Scan(&viaLike); err != nil {
			t.Fatalf("blockLike(%q) vs key %q: %v", c.pattern, c.key, err)
		}
		if viaLike != c.want {
			t.Errorf("catalog SQL: pattern %q vs key %q = %v, want %v", c.pattern, c.key, viaLike, c.want)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM artifact_blocks WHERE pattern = $1`, c.pattern); err != nil {
			t.Fatal(err)
		}
	}
}
