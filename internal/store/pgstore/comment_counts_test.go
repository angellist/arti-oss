//go:build integration

package pgstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestCommentCounts(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})

	mkArtifact := func() uuid.UUID {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText,
			Title:        unique("counts"),
			ContentType:  "text/plain",
			Content:      []byte("body"),
			Creator:      "t@x.com",
		})
		if err != nil {
			t.Fatal(err)
		}
		return pgstore.UUIDFromPG(row.ArtifactID)
	}
	thread := func(artifactID uuid.UUID, status string) uuid.UUID {
		id := uuid.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO comment_threads (thread_id, artifact_id, status, created_by) VALUES ($1,$2,$3,'t@x.com')`,
			id, artifactID, status); err != nil {
			t.Fatal(err)
		}
		return id
	}
	comment := func(threadID uuid.UUID, deleted bool) {
		del := "NULL"
		if deleted {
			del = "now()"
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO comments (comment_id, thread_id, author, body, deleted_at)
			 VALUES ($1,$2,'t@x.com','hi',`+del+`)`,
			uuid.New(), threadID); err != nil {
			t.Fatal(err)
		}
	}

	// discussed: an open thread with 2 live comments + 1 deleted one, plus a
	// resolved thread with 1 live comment → 3 live comments, 1 open thread.
	discussed := mkArtifact()
	open := thread(discussed, "open")
	comment(open, false)
	comment(open, false)
	comment(open, true) // deleted — must not be counted
	resolved := thread(discussed, "resolved")
	comment(resolved, false)

	// emptyThread: a thread whose only comment was deleted. It is still an open
	// thread on the page, so it counts as one — with zero comments.
	emptyThread := mkArtifact()
	et := thread(emptyThread, "open")
	comment(et, true)

	// quiet: no threads at all. Must be absent from the map (callers read a
	// miss as zero) rather than error.
	quiet := mkArtifact()

	got, err := st.CommentCounts(ctx, []uuid.UUID{discussed, emptyThread, quiet})
	if err != nil {
		t.Fatal(err)
	}

	if c := got[discussed]; c.Comments != 3 || c.OpenThreads != 1 {
		t.Errorf("discussed: got %+v, want {Comments:3 OpenThreads:1}", c)
	}
	if c := got[emptyThread]; c.Comments != 0 || c.OpenThreads != 1 {
		t.Errorf("emptyThread: got %+v, want {Comments:0 OpenThreads:1}", c)
	}
	if _, ok := got[quiet]; ok {
		t.Errorf("artifact with no threads should be absent from the map, got %+v", got[quiet])
	}
	if len(got) != 2 {
		t.Errorf("unexpected extra ids in result: %+v", got)
	}
}

func TestCommentCounts_EmptyInput(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	got, err := st.CommentCounts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %+v", got)
	}
}
