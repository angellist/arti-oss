//go:build integration

package comments

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// A PACKAGE stores a zip, so searching those bytes for a phrase the served
// page shows can only ever fail. Reporting that as "quote not found" told an
// agent its quote was wrong when the anchoring is what is unsupported.
func TestAddQuoteOnAPackageIsRefusedAsUnsupported(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	art := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := NewService(pool, art, auth.NewJWTSigner([]byte("test-secret")))

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("<p>a phrase the page shows</p>")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	const owner = "owner-pkgquote@example.com"
	row, err := art.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypePackage, Title: "pkg", ContentType: "application/zip",
		Content: buf.Bytes(), Creator: owner, AllowedAccess: []string{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.AddForCaller(ctx, uuidFrom(row.ArtifactID), owner,
		AddInput{Body: "does this seat?", Quote: "a phrase the page shows"})
	if !errors.Is(err, ErrQuoteUnsupported) {
		t.Fatalf("err = %v, want ErrQuoteUnsupported", err)
	}
}
