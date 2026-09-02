package comments

import (
	"context"
	"regexp"
	"strings"

	"github.com/angellist/arti-oss/gen/sqlc"
)

// mentions.go — "@someone@example.com" inside a comment body.
//
// A mention is plain text in the body, not a separate field: the client writes
// the address the person typed and the SERVER decides what it means. That is
// deliberate — the notification is the only thing a mention does, and letting
// the client hand us a recipient list would be a second authority source for
// "who may be told what this document says" (the client's list and the
// document's ACL could then disagree). Parsing the stored body instead means
// the DM set is always derivable from what is actually written in the thread.

// maxMentions caps how many addresses one comment can notify. A comment is a
// sentence, not a mailing list; without a cap a pasted address block turns one
// write into an unbounded fan-out of Slack lookups and DMs.
const maxMentions = 10

// mentionRe matches an @-prefixed address. The leading `@` must be preceded by
// something that is not part of an address (start of string, whitespace, or
// punctuation) — checked by the caller, since Go's regexp has no lookbehind —
// so the second `@` of a bare "alice@example.com" is not read as a mention of
// "example.com".
//
// The trailing `[A-Za-z]{2,}` deliberately does not swallow a sentence-final
// period: "ping @alice@example.com." mentions alice, not "example.com.".
var mentionRe = regexp.MustCompile(`@([A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,})`)

// parseMentions extracts the distinct addresses @-mentioned in a comment body,
// lower-cased, in the order they appear, capped at maxMentions.
func parseMentions(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatchIndex(body, -1) {
		at := m[0] // index of the leading '@'
		if at > 0 && !isMentionBoundary(body[at-1]) {
			continue
		}
		email := strings.ToLower(body[m[2]:m[3]])
		if seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, email)
		if len(out) == maxMentions {
			break
		}
	}
	return out
}

// isMentionBoundary reports whether b can legitimately precede a mention's `@`.
// Anything that could be part of an address's local part cannot.
func isMentionBoundary(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return false
	}
	switch b {
	case '.', '_', '%', '+', '-', '@':
		return false
	}
	return true
}

// splitMentions divides a body's mentions into those who may READ the artifact
// (so they can be DM'd — the DM quotes the document's title, the highlighted
// passage and the comment text) and those who may not.
//
// Nobody is granted anything here: an address that cannot read the artifact is
// simply not notified, and is reported back so the owner's own DM can say a
// mention went nowhere. Granting read access is a separate, explicit action the
// owner takes through POST /api/artifacts/{id}/access/grant-read.
//
// The actor is dropped from both lists — self-mentions notify nobody, the same
// way the actor is excluded from every other recipient set.
func (s *Service) splitMentions(ctx context.Context, row sqlc.Artifact, actor, body string) (reachable, unreachable []string, err error) {
	for _, email := range parseMentions(body) {
		if strings.EqualFold(email, actor) {
			continue
		}
		ok, err := s.canReadRow(ctx, row, email)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			reachable = append(reachable, email)
		} else {
			unreachable = append(unreachable, email)
		}
	}
	return reachable, unreachable, nil
}
