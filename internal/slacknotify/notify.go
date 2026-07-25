// Package slacknotify sends Slack DM notifications for artifact comment
// events. Recipients are resolved from email to Slack user ID via
// users.lookupByEmail (cached), then DM'd via chat.postMessage. It is a
// fire-and-forget side effect: the caller invokes Notify in a goroutine after
// the comment write has committed, so a Slack failure can never fail the write.
package slacknotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Action is the comment event that triggered a notification.
type Action int

const (
	ActionNewComment Action = iota // a new comment / thread
	ActionReply                    // a reply on an existing thread
	ActionResolve                  // a thread marked resolved
	ActionReopen                   // a resolved thread reopened
)

func (a Action) verb() string {
	switch a {
	case ActionNewComment:
		return "commented"
	case ActionReply:
		return "replied"
	case ActionResolve:
		return "resolved a comment"
	case ActionReopen:
		return "reopened a comment"
	default:
		return "updated a comment"
	}
}

// notifiesParticipants reports whether the action DMs prior thread
// participants in addition to the owner. Reopen notifies the owner only.
func (a Action) notifiesParticipants() bool { return a != ActionReopen }

// Event is a fully-resolved comment event ready to notify on. The caller
// (comments.Service, which has DB access) populates every field; the notifier
// only routes recipients, renders the message, and sends.
type Event struct {
	Action        Action
	ArtifactURL   string // canonical artifact URL, incl. #comment-<id> fragment
	ArtifactTitle string
	ActorName     string   // display name of whoever triggered the event
	Actor         string   // actor email — always excluded from recipients
	Owner         string   // artifact owner (creator) email
	Participants  []string // distinct prior commenter emails on the thread

	AnchorKind  string // "doc" | "text" | "pin"
	AnchorQuote string // text-anchor quote, raw (untruncated, unescaped)
	PinNumber   int    // 1-based pin number; 0 = none / not applicable

	Body string // the new comment body; empty for resolve/reopen
}

// Notifier resolves recipients to Slack users and DMs them.
type Notifier struct {
	client slackClient
	cache  *idCache
	log    *slog.Logger
}

// New builds a Notifier for the given bot user OAuth token. It returns nil
// when the token is empty: callers treat a nil *Notifier as "notifications
// disabled", and Notify on a nil receiver is a safe no-op.
func New(botToken string, logger *slog.Logger) *Notifier {
	if strings.TrimSpace(botToken) == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{
		client: newHTTPClient(botToken),
		cache:  newIDCache(12*time.Hour, 1*time.Hour),
		log:    logger,
	}
}

// Notify DMs every recipient for the event. Per-recipient lookup/post failures
// are logged and skipped — one bad recipient never blocks the others, and a
// nil *Notifier returns immediately.
func (n *Notifier) Notify(ctx context.Context, ev Event) {
	if n == nil {
		return
	}
	msg := render(ev) // identical for every recipient
	for _, email := range recipients(ev) {
		userID, ok := n.resolve(ctx, email)
		if !ok {
			continue
		}
		if err := n.client.PostDM(ctx, userID, msg); err != nil {
			n.log.Warn("slacknotify: post DM failed", "email", email, "err", err)
		}
	}
}

// resolve maps an email to a Slack user ID via the cache. A cached or fresh
// "no Slack user" answer returns ok=false (and is negative-cached). A transport
// error is logged and returns ok=false WITHOUT caching, so a later event retries.
func (n *Notifier) resolve(ctx context.Context, email string) (string, bool) {
	if id, found, ok := n.cache.get(email); ok {
		return id, found
	}
	id, err := n.client.LookupUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, errNoSlackUser) {
			n.cache.put(email, "")
			return "", false
		}
		n.log.Warn("slacknotify: lookup failed", "email", email, "err", err)
		return "", false
	}
	n.cache.put(email, id)
	return id, true
}

// recipients computes the de-duplicated recipient set for an event: always the
// owner; prior participants for non-reopen actions; never the actor. Matching
// is case-insensitive and returned emails are lower-cased and sorted (stable
// send order + deterministic tests).
func recipients(ev Event) []string {
	actor := normEmail(ev.Actor)
	seen := map[string]bool{}
	var out []string
	add := func(email string) {
		e := normEmail(email)
		if e == "" || e == actor || seen[e] {
			return
		}
		seen[e] = true
		out = append(out, e)
	}
	add(ev.Owner)
	if ev.Action.notifiesParticipants() {
		for _, p := range ev.Participants {
			add(p)
		}
	}
	sort.Strings(out)
	return out
}

func normEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// render builds the Slack mrkdwn message for an event.
func render(ev Event) string {
	title := strings.TrimSpace(ev.ArtifactTitle)
	if title == "" {
		title = "an artifact"
	}
	link := slackEscape(title)
	if ev.ArtifactURL != "" {
		link = fmt.Sprintf("<%s|%s>", ev.ArtifactURL, slackEscape(title))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "*%s* %s on %s\n%s", slackEscape(ev.ActorName), ev.Action.verb(), link, anchorLabel(ev))
	// The comment text isn't blockquoted (the highlighted passage is — see
	// anchorLabel); it's the person's voice, prefixed with a speech emoji.
	if body := strings.TrimSpace(ev.Body); body != "" {
		b.WriteString("\n💬 " + slackEscape(body))
	}
	return b.String()
}

// anchorLabel renders the anchor context line. A text highlight is shown as an
// italic blockquote of the highlighted passage (it IS a quotation from the
// doc); pin/doc anchors are plain location context.
func anchorLabel(ev Event) string {
	switch ev.AnchorKind {
	case "text":
		q := strings.TrimSpace(ev.AnchorQuote)
		if q == "" {
			return "on a highlight"
		}
		const max = 120
		if r := []rune(q); len(r) > max {
			q = strings.TrimSpace(string(r[:max])) + "…"
		}
		return "> _" + slackEscape(q) + "_"
	case "pin":
		if ev.PinNumber > 0 {
			return fmt.Sprintf("📍 Pin #%d", ev.PinNumber)
		}
		return "📍 on a pin"
	default:
		return "on the document"
	}
}

// slackEscape escapes the three characters Slack treats specially in mrkdwn
// text. URLs inside <url|label> links must not be escaped, so callers pass
// only label/body text here.
func slackEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
