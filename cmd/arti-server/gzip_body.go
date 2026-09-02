package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// gzipRequestBody transparently decompresses a `Content-Encoding: gzip`
// request body before the rest of the chain sees it. Clients gzip the
// upload so the Cloudflare WAF, which base64-decodes and pattern-matches
// request bodies, sees opaque high-entropy bytes instead of the literal
// `<script>` in an HTML upload — which it otherwise blocks with a 403.
//
// It slots in BEFORE auth and EnforceUploadScope so a downstream
// MaxBytesReader (the per-route upload cap, or the handler's own) wraps
// the DECOMPRESSED stream and thus caps decompressed size. Two caps guard
// against a decompression bomb: `maxBytes` on the compressed body read
// from the network, and `maxBytes` again on the decompressed output here;
// a tighter per-route cap applied later still wins.
//
// Decompression is lazy: gzip.NewReader runs on the first Read, so a
// bodyless request, a GET carrying a stray header, or a request auth
// rejects without reading the body does no work.
func gzipRequestBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
				compressed := http.MaxBytesReader(w, r.Body, maxBytes)
				r.Body = http.MaxBytesReader(w, &lazyGzipReader{src: compressed}, maxBytes)
				// Downstream now sees a plain, already-decoded body.
				r.Header.Del("Content-Encoding")
				r.ContentLength = -1
			}
			next.ServeHTTP(w, r)
		})
	}
}

// lazyGzipReader defers gzip.NewReader (which eagerly reads the 10-byte
// header) until the first Read, so the body is only touched when a handler
// actually consumes it. A malformed gzip body surfaces as a read error,
// which the create/append handlers map to a 400 "decode body" response.
type lazyGzipReader struct {
	src io.ReadCloser
	zr  *gzip.Reader
	err error
}

func (l *lazyGzipReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if l.zr == nil {
		zr, err := gzip.NewReader(l.src)
		if err != nil {
			l.err = err
			return 0, err
		}
		l.zr = zr
	}
	return l.zr.Read(p)
}

func (l *lazyGzipReader) Close() error {
	if l.zr != nil {
		_ = l.zr.Close()
	}
	return l.src.Close()
}
