//go:build integration

package mcp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/comments"
	"github.com/angellist/arti-oss/internal/mcp"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// blindBlobs serves writes but refuses every read, standing in for a blob
// store that is down.
type blindBlobs struct{ *blob.InMemory }

func (blindBlobs) Get(context.Context, string) (io.ReadCloser, blob.ObjectInfo, error) {
	return nil, blob.ObjectInfo{}, errors.New("blob store is down")
}

// Comment threads live in Postgres, so listing them must not depend on the
// artifact's bytes. Resolving the ident through Content made a blob outage
// look like the document had no comments.
func TestListCommentsSurvivesABlobOutage(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blindBlobs{blob.NewInMemory()}, pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	cs := comments.NewService(pool, st, auth.NewJWTSigner([]byte("test-secret")))
	h := mcp.NewServer(svc, cs).Handler()

	const alice = "alice-blobless@example.com"
	slug := uniqueSlug("mcp-comment-blobless")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, NamedSlug: &slug, Title: "doc",
		// Over InlineMaxBytes, so the bytes live in the blob store rather
		// than inline in Postgres.
		ContentType: "text/markdown", Content: bytes.Repeat([]byte("x"), pgstore.InlineMaxBytes+1),
		Creator: alice, AllowedAccess: []string{"*"},
	}); err != nil {
		t.Fatal(err)
	}

	rr := rpcCall(t, h, alice, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_comments","arguments":{"ident":"`+slug+`"}}}`)
	if body := rr.Body.String(); strings.Contains(body, "blob store is down") {
		t.Fatalf("list_comments read the blob: %s", body)
	}
}
