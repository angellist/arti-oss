package blob

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// RunContractSuite exercises the [Store] contract against a backend instance.
// All subtests use unique key prefixes so the suite can run repeatedly against
// a shared backend (e.g. a long-lived MinIO container) without collision.
func RunContractSuite(t *testing.T, s Store) {
	t.Helper()
	t.Run("PutGet", func(t *testing.T) { contractPutGet(t, s) })
	t.Run("Idempotent", func(t *testing.T) { contractIdempotent(t, s) })
	t.Run("StatNotFound", func(t *testing.T) { contractStatNotFound(t, s) })
	t.Run("GetNotFound", func(t *testing.T) { contractGetNotFound(t, s) })
	t.Run("Delete", func(t *testing.T) { contractDelete(t, s) })
	t.Run("List", func(t *testing.T) { contractList(t, s) })
	t.Run("LargeBlob", func(t *testing.T) { contractLargeBlob(t, s) })
	t.Run("ContentTypeRoundTrip", func(t *testing.T) { contractContentType(t, s) })
}

func contractPutGet(t *testing.T, s Store) {
	ctx := t.Context()
	key := "ct/putget/" + uniq() + ".txt"
	body := []byte("hello couch")
	res, err := s.Put(ctx, key, bytes.NewReader(body), PutOpts{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if res.Size != int64(len(body)) {
		t.Errorf("size: got %d want %d", res.Size, len(body))
	}
	if res.SHA256 == "" {
		t.Error("SHA256 missing from PutResult")
	}

	rc, info, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body roundtrip failed: got %q want %q", got, body)
	}
	if info.SHA256 != res.SHA256 {
		t.Errorf("SHA mismatch: stat=%q put=%q", info.SHA256, res.SHA256)
	}
}

func contractIdempotent(t *testing.T, s Store) {
	ctx := t.Context()
	key := "ct/idem/" + uniq()
	body := []byte("repeat me")
	first, err := s.Put(ctx, key, bytes.NewReader(body), PutOpts{})
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	second, err := s.Put(ctx, key, bytes.NewReader(body), PutOpts{})
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if first.SHA256 != second.SHA256 {
		t.Errorf("SHA differs across identical Puts: %q vs %q", first.SHA256, second.SHA256)
	}
}

func contractStatNotFound(t *testing.T, s Store) {
	_, err := s.Stat(t.Context(), "ct/missing/"+uniq())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat missing: got %v, want ErrNotFound", err)
	}
}

func contractGetNotFound(t *testing.T, s Store) {
	_, _, err := s.Get(t.Context(), "ct/missing/"+uniq())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: got %v, want ErrNotFound", err)
	}
}

func contractDelete(t *testing.T, s Store) {
	ctx := t.Context()
	key := "ct/delete/" + uniq()
	if _, err := s.Put(ctx, key, bytes.NewReader([]byte("byebye")), PutOpts{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat after delete: got %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Errorf("Delete missing: got %v, want nil", err)
	}
}

func contractList(t *testing.T, s Store) {
	ctx := t.Context()
	prefix := "ct/list/" + uniq() + "/"
	keys := []string{prefix + "a", prefix + "b", prefix + "c"}
	for _, k := range keys {
		if _, err := s.Put(ctx, k, strings.NewReader(k), PutOpts{}); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	got, err := s.List(ctx, prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("List len: got %d want %d (got=%v)", len(got), len(keys), keys)
	}
	for i, want := range keys {
		if got[i].Key != want {
			t.Errorf("List[%d]: got %q want %q", i, got[i].Key, want)
		}
	}
}

func contractLargeBlob(t *testing.T, s Store) {
	ctx := t.Context()
	key := "ct/large/" + uniq()
	body := make([]byte, 1<<20) // 1 MiB
	if _, err := rand.Read(body); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := s.Put(ctx, key, bytes.NewReader(body), PutOpts{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, info, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("large body mismatch (size=%d want=%d)", len(got), len(body))
	}
	if info.Size != int64(len(body)) {
		t.Errorf("Size: got %d want %d", info.Size, len(body))
	}
}

func contractContentType(t *testing.T, s Store) {
	ctx := t.Context()
	key := "ct/ctype/" + uniq()
	want := "application/json"
	if _, err := s.Put(ctx, key, strings.NewReader(`{}`), PutOpts{ContentType: want}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.ContentType != want {
		t.Errorf("ContentType: got %q want %q", info.ContentType, want)
	}
}

func uniq() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
