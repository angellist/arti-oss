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
	Owner         string   // artifact owner email — the doc's owner, not the latest version's pusher
	Participants  []string // distinct prior commenter emails on the thread

	// Mentions are addresses @-mentioned in Body that CAN read the artifact.
	// They are notified whether or not they have ever touched the thread —
	// being named is the point — and their DM leads with the mention rather
	// than the action, because "Alice mentioned you" is why they should look.
	Mentions []string
	// Unreachable are addresses @-mentioned in Body that CANNOT read the
	// artifact. They are never notified (the DM quotes the document). They are
	// carried here only so the owner's own DM can say the mention went nowhere
	// — the owner being the one person who can grant the access that would fix
	// it. Nobody else sees this list.
	Unreachable []string

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
	// The message is per-recipient, not shared: a mentioned person is told they
	// were mentioned, and only the owner is told a mention went nowhere.
	mentioned := map[string]bool{}
	for _, m := range ev.Mentions {
		mentioned[normEmail(m)] = true
	}
	owner := normEmail(ev.Owner)
	for _, email := range recipients(ev) {
		userID, ok := n.resolve(ctx, email)
		if !ok {
			continue
		}
		msg := render(ev, mentioned[email], email == owner)
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
	// Mentions are added for every action. In practice only the actions that
	// carry a body can produce any (resolve/reopen send an empty body, which
	// has nothing to mention), so this needs no action guard of its own —
	// and adding one would silently drop a mention if a future action starts
	// carrying text.
	for _, m := range ev.Mentions {
		add(m)
	}
	sort.Strings(out)
	return out
}

func normEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// render builds the Slack mrkdwn message for one recipient. `mentioned` says
// this recipient was named in the body; `isOwner` says they own the document
// (and so are the only one shown mentions that could not be delivered).
func render(ev Event, mentioned, isOwner bool) string {
	title := strings.TrimSpace(ev.ArtifactTitle)
	if title == "" {
		title = "an artifact"
	}
	link := slackEscape(title)
	if ev.ArtifactURL != "" {
		link = fmt.Sprintf("<%s|%s>", ev.ArtifactURL, slackEscape(title))
	}

	// Being named beats what the naming was attached to: someone who was
	// mentioned is told that first, since it is why the DM is worth opening.
	verb := ev.Action.verb()
	if mentioned {
		verb = "mentioned you in a comment"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "*%s* %s on %s\n%s", slackEscape(ev.ActorName), verb, link, anchorLabel(ev))
	// The comment text isn't blockquoted (the highlighted passage is — see
	// anchorLabel); it's the person's voice, prefixed with a speech emoji.
	if body := strings.TrimSpace(ev.Body); body != "" {
		b.WriteString("\n💬 " + slackEscape(body))
	}
	if isOwner {
		if line := unreachableLine(ev); line != "" {
			b.WriteString("\n" + line)
		}
	}
	return b.String()
}

// unreachableLine renders the owner-only footer naming mentions that were not
// delivered because the address cannot read the document. It names the
// addresses rather than counting them: the owner's next move is to grant one of
// them access, and a count doesn't tell them who.
func unreachableLine(ev Event) string {
	if len(ev.Unreachable) == 0 {
		return ""
	}
	who := make([]string, 0, len(ev.Unreachable))
	for _, e := range ev.Unreachable {
		who = append(who, slackEscape(e))
	}
	return fmt.Sprintf("⚠️ %s also mentioned %s, who can't read this doc — not notified.",
		slackEscape(ev.ActorName), strings.Join(who, ", "))
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
