package opensearch

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxContentBytes caps the text body indexed per artifact. Larger bodies
// are truncated at a UTF-8 boundary to keep index size reasonable.
const MaxContentBytes = 256 * 1024 // 256 KiB

// ExtractText strips HTML tags and normalizes whitespace from content,
// returning plain text suitable for full-text indexing. Handles both
// raw text (markdown, plain) and HTML. Truncated to MaxContentBytes.
func ExtractText(body []byte, contentType string) string {
	if len(body) == 0 {
		return ""
	}
	s := string(body)

	if isHTML(contentType) {
		s = stripHTML(s)
	}

	s = collapseWhitespace(s)
	s = strings.TrimSpace(s)

	if len(s) > MaxContentBytes {
		s = truncateUTF8(s, MaxContentBytes)
	}
	return s
}

func isHTML(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/html") || strings.Contains(ct, "xhtml")
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	return htmlTagRe.ReplaceAllString(s, " ")
}

var multiSpaceRe = regexp.MustCompile(`\s{2,}`)

func collapseWhitespace(s string) string {
	return multiSpaceRe.ReplaceAllString(s, " ")
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk back from maxBytes to find a valid UTF-8 boundary.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
