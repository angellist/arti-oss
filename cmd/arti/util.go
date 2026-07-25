package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

func stderr() *os.File { return os.Stderr }

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// splitIdentPath splits "uuid/path/inside.md" or "slug/path/inside.md" into
// the leading identifier and the trailing entry path. Returns ("ident", "")
// when no slash.
func splitIdentPath(s string) (ident, path string) {
	i := strings.Index(s, "/")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

// guessMIME from a file extension. Mirrors pkgzip.ContentTypeOf for parity.
//
// Returns text/plain as the LAST-RESORT fallback only for files whose
// extension we don't recognize but that we expect to be textual. Binary
// extensions (images, audio, video, pdf, …) get their proper MIME so
// the caller can route them to ATTACHMENT instead of silently storing
// the bytes under text/plain.
func guessMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".txt", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".sh":
		return "text/x-shellscript"
	case ".py":
		return "text/x-python"
	case ".go":
		return "text/x-go"
	case ".csv":
		return "text/csv"
	case ".xml":
		return "application/xml"
	case ".zip":
		return "application/zip"
	case ".pdf":
		return "application/pdf"
	// Image formats — sniffed so the caller can route to ATTACHMENT.
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".bmp":
		return "image/bmp"
	case ".tiff", ".tif":
		return "image/tiff"
	// Audio / video
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	// Generic binary
	case ".bin", ".exe", ".dmg", ".pkg":
		return "application/octet-stream"
	}
	return "text/plain"
}

// isTextualCT reports whether a MIME type holds text, so it may be a TEXT
// artifact. Mirrors the server's isTextualContentType; charset/params ignored.
func isTextualCT(ct string) bool {
	base := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	if strings.HasPrefix(base, "text/") {
		return true
	}
	switch base {
	case "application/json", "application/yaml", "application/javascript":
		return true
	}
	return false
}

// markdownLine matches a line that carries a strong, unambiguous markdown
// signal: an ATX heading (`# `), an unordered (`- `/`* `/`+ `) or ordered
// (`1. `) list item, or a blockquote (`> `). The trailing space matters — it's
// what distinguishes `- item` from a `-rw-r--r--` permission string in an
// `ls -l` dump, which is exactly the false positive we want to avoid.
var markdownLine = regexp.MustCompile(`(?m)^\s{0,3}(#{1,6} |[-*+] |\d+\. |> )`)

// markdownInline matches inline markdown: a fenced code block or a `[text](url)`
// link. Bold/italic (`**`) is intentionally excluded — it shows up too often in
// plain console output to be a reliable signal.
var markdownInline = regexp.MustCompile("```|\\[[^\\]]+\\]\\([^)]+\\)")

// sniffStdinMIME picks a content type for piped/stdin input that carries no
// filename. We default to text/plain (renders in a <pre>, preserving line
// breaks) and only upgrade to text/markdown when the content shows a clear
// markdown structure. This keeps `cat notes.md | arti add` rendering as
// markdown while `ls -l | arti add` stays readable plain text.
func sniffStdinMIME(b []byte) string {
	s := string(b)
	if markdownLine.MatchString(s) || markdownInline.MatchString(s) {
		return "text/markdown"
	}
	return "text/plain"
}

func bail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
