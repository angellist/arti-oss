package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// captureBody is a handler that reads r.Body fully and records the result.
func captureBody(got *[]byte, readErr *error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		*got = b
		*readErr = err
		w.WriteHeader(http.StatusOK)
	}
}

func TestGzipRequestBody_Decompresses(t *testing.T) {
	payload := []byte(`{"title":"x","content":"<script>alert(1)</script>"}`)
	var got []byte
	var readErr error
	h := gzipRequestBody(1 << 20)(captureBody(&got, &readErr))

	req := httptest.NewRequest(http.MethodPost, "/api/artifacts", bytes.NewReader(gz(t, payload)))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if readErr != nil {
		t.Fatalf("body read: %v", readErr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decoded body = %q, want %q", got, payload)
	}
	if req.Header.Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding not stripped: %q", req.Header.Get("Content-Encoding"))
	}
	if req.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want -1", req.ContentLength)
	}
}

func TestGzipRequestBody_MixedCaseHeader(t *testing.T) {
	payload := []byte("hello")
	var got []byte
	var readErr error
	h := gzipRequestBody(1 << 20)(captureBody(&got, &readErr))

	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(gz(t, payload)))
	req.Header.Set("Content-Encoding", " GZIP ")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("mixed-case header not honored: got %q err %v", got, readErr)
	}
}

func TestGzipRequestBody_PassThroughWhenNotGzip(t *testing.T) {
	payload := []byte("plain body, not gzipped")
	var got []byte
	var readErr error
	h := gzipRequestBody(1 << 20)(captureBody(&got, &readErr))

	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(payload))
	// No Content-Encoding header.
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("plain body altered: got %q err %v", got, readErr)
	}
}

func TestGzipRequestBody_BadGzipIsReadError(t *testing.T) {
	var got []byte
	var readErr error
	h := gzipRequestBody(1 << 20)(captureBody(&got, &readErr))

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("this is not gzip"))
	req.Header.Set("Content-Encoding", "gzip")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("expected a read error for a malformed gzip body, got nil")
	}
}

func TestGzipRequestBody_DecompressionBombCapped(t *testing.T) {
	// 4 MiB of zeros compresses tiny but must not be allowed to expand past
	// the cap. With a 1 MiB cap the handler's read must fail.
	bomb := make([]byte, 4<<20)
	var got []byte
	var readErr error
	h := gzipRequestBody(1 << 20)(captureBody(&got, &readErr))

	req := httptest.NewRequest(http.MethodPost, "/api/artifacts", bytes.NewReader(gz(t, bomb)))
	req.Header.Set("Content-Encoding", "gzip")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("expected decompressed body to be capped, got nil error")
	}
	if !strings.Contains(readErr.Error(), "request body too large") {
		t.Fatalf("expected a too-large error, got %v", readErr)
	}
	if int64(len(got)) > (1 << 20) {
		t.Fatalf("read %d decompressed bytes, cap was 1 MiB", len(got))
	}
}

func TestGzipRequestBody_LazyNoBodyTouchWhenUnread(t *testing.T) {
	// A handler that never reads the body must not trigger gzip errors,
	// even if the body is not valid gzip — mirrors an auth-rejected request.
	called := false
	h := gzipRequestBody(1 << 20)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusUnauthorized)
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("garbage"))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusUnauthorized {
		t.Fatalf("handler not reached cleanly: called=%v code=%d", called, rec.Code)
	}
}
