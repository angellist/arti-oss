package slacknotify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recipients() encodes the routing rule: owner always; participants on every
// action except reopen; the actor never; owner de-duped if also a participant.
// These cases are the business contract — they must fail if that rule changes.
func TestRecipients(t *testing.T) {
	const (
		owner = "owner@x.com"
		actor = "actor@x.com"
		p1    = "p1@x.com"
		p2    = "p2@x.com"
	)
	cases := []struct {
		name string
		ev   Event
		want []string
	}{
		{
			name: "new comment by owner: nobody (actor==owner, no other participants)",
			ev:   Event{Action: ActionNewComment, Owner: owner, Actor: owner, Participants: []string{owner}},
			want: nil,
		},
		{
			name: "new comment by outsider: owner only",
			ev:   Event{Action: ActionNewComment, Owner: owner, Actor: actor, Participants: []string{actor}},
			want: []string{owner},
		},
		{
			name: "reply notifies owner + other participants, not the actor",
			ev:   Event{Action: ActionReply, Owner: owner, Actor: p1, Participants: []string{owner, p1, p2}},
			want: []string{owner, p2},
		},
		{
			name: "resolve notifies owner + participants",
			ev:   Event{Action: ActionResolve, Owner: owner, Actor: actor, Participants: []string{p1, p2}},
			want: []string{owner, p1, p2},
		},
		{
			name: "reopen notifies owner ONLY (participants excluded)",
			ev:   Event{Action: ActionReopen, Owner: owner, Actor: actor, Participants: []string{p1, p2}},
			want: []string{owner},
		},
		{
			name: "reopen by owner: nobody (owner is the actor)",
			ev:   Event{Action: ActionReopen, Owner: owner, Actor: owner, Participants: []string{p1}},
			want: nil,
		},
		{
			name: "owner who is also a participant is notified once",
			ev:   Event{Action: ActionReply, Owner: owner, Actor: p1, Participants: []string{owner, owner, p1}},
			want: []string{owner},
		},
		{
			name: "case-insensitive actor exclusion + de-dup",
			ev:   Event{Action: ActionReply, Owner: "Owner@X.com", Actor: "ACTOR@x.com", Participants: []string{"Actor@x.com", "P1@x.com"}},
			want: []string{"owner@x.com", "p1@x.com"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recipients(tc.ev)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("recipients() = %v, want %v", got, want)
			}
		})
	}
}

func TestRender(t *testing.T) {
	base := Event{
		ArtifactURL:   "https://arti/s/doc#comment-abc",
		ArtifactTitle: "My Doc",
		ActorName:     "Tian",
	}

	t.Run("text anchor is an italic blockquote; body is emoji-prefixed, not quoted", func(t *testing.T) {
		ev := base
		ev.Action = ActionReply
		ev.AnchorKind = "text"
		ev.AnchorQuote = "the quoted text"
		ev.Body = "line one\nline two"
		got := render(ev)
		want := "*Tian* replied on <https://arti/s/doc#comment-abc|My Doc>\n" +
			"> _the quoted text_\n💬 line one\nline two"
		if got != want {
			t.Fatalf("render mismatch:\n got: %q\nwant: %q", got, want)
		}
	})

	t.Run("pin anchor shows the pin number; body is emoji-prefixed", func(t *testing.T) {
		ev := base
		ev.Action = ActionNewComment
		ev.AnchorKind = "pin"
		ev.PinNumber = 3
		ev.Body = "look here"
		if got := render(ev); !strings.Contains(got, "📍 Pin #3") || !strings.Contains(got, "💬 look here") {
			t.Fatalf("pin render missing parts: %q", got)
		}
	})

	t.Run("resolve has no body blockquote", func(t *testing.T) {
		ev := base
		ev.Action = ActionResolve
		ev.AnchorKind = "doc"
		ev.Body = "" // resolve/reopen carry no new comment text
		got := render(ev)
		if strings.Contains(got, "💬") {
			t.Fatalf("resolve should have no comment body, got: %q", got)
		}
		if !strings.Contains(got, "resolved a comment") || !strings.Contains(got, "on the document") {
			t.Fatalf("resolve render missing parts: %q", got)
		}
	})

	t.Run("escapes mrkdwn-special chars in label text but not the URL", func(t *testing.T) {
		ev := base
		ev.Action = ActionReply
		ev.ArtifactTitle = "A < B & C"
		ev.AnchorKind = "doc"
		ev.Body = "x < y"
		got := render(ev)
		if !strings.Contains(got, "A &lt; B &amp; C") || !strings.Contains(got, "💬 x &lt; y") {
			t.Fatalf("escaping failed: %q", got)
		}
		if !strings.Contains(got, "https://arti/s/doc#comment-abc") {
			t.Fatalf("URL should be present unescaped: %q", got)
		}
	})

	t.Run("long quote is truncated", func(t *testing.T) {
		ev := base
		ev.Action = ActionReply
		ev.AnchorKind = "text"
		ev.AnchorQuote = strings.Repeat("a", 200)
		if got := render(ev); !strings.Contains(got, "…") {
			t.Fatalf("expected truncated quote with ellipsis, got: %q", got)
		}
	})
}

// fakeClient records DM targets and can simulate lookup outcomes per email.
type fakeClient struct {
	lookups   int
	posts     int
	postedTo  []string
	ids       map[string]string // email → user ID
	notFound  map[string]bool   // email → simulate users_not_found
	lookupErr map[string]error  // email → simulate a transport error
}

func (f *fakeClient) LookupUserByEmail(_ context.Context, email string) (string, error) {
	f.lookups++
	if f.lookupErr[email] != nil {
		return "", f.lookupErr[email]
	}
	if f.notFound[email] {
		return "", errNoSlackUser
	}
	return f.ids[email], nil
}

func (f *fakeClient) PostDM(_ context.Context, userID, _ string) error {
	f.posts++
	f.postedTo = append(f.postedTo, userID)
	return nil
}

func newTestNotifier(c slackClient) *Notifier {
	return &Notifier{client: c, cache: newIDCache(time.Hour, time.Hour), log: discardLogger()}
}

func TestNotify_DMsResolvedRecipientsOnly(t *testing.T) {
	fc := &fakeClient{
		ids:      map[string]string{"owner@x.com": "U_OWNER", "p1@x.com": "U_P1"},
		notFound: map[string]bool{"p2@x.com": true}, // p2 has no Slack account
	}
	n := newTestNotifier(fc)
	n.Notify(context.Background(), Event{
		Action: ActionReply, Owner: "owner@x.com", Actor: "actor@x.com",
		Participants: []string{"p1@x.com", "p2@x.com"},
	})
	sort.Strings(fc.postedTo)
	if !reflect.DeepEqual(fc.postedTo, []string{"U_OWNER", "U_P1"}) {
		t.Fatalf("posted to %v, want owner + p1 (p2 has no Slack user)", fc.postedTo)
	}
}

func TestNotify_NilNotifierIsNoop(t *testing.T) {
	var n *Notifier
	// Must not panic.
	n.Notify(context.Background(), Event{Action: ActionReply, Owner: "o@x.com", Actor: "a@x.com"})
}

func TestResolve_NegativeCacheAvoidsRelookup(t *testing.T) {
	fc := &fakeClient{notFound: map[string]bool{"ghost@x.com": true}}
	n := newTestNotifier(fc)
	for i := 0; i < 3; i++ {
		if _, ok := n.resolve(context.Background(), "ghost@x.com"); ok {
			t.Fatal("ghost should never resolve")
		}
	}
	if fc.lookups != 1 {
		t.Fatalf("expected 1 lookup (negative-cached), got %d", fc.lookups)
	}
}

func TestResolve_TransportErrorNotCached(t *testing.T) {
	fc := &fakeClient{lookupErr: map[string]error{"x@x.com": errors.New("boom")}}
	n := newTestNotifier(fc)
	for i := 0; i < 3; i++ {
		_, _ = n.resolve(context.Background(), "x@x.com")
	}
	if fc.lookups != 3 {
		t.Fatalf("transport errors must not be cached; expected 3 lookups, got %d", fc.lookups)
	}
}

func TestCache_HitMissExpiry(t *testing.T) {
	c := newIDCache(time.Hour, 30*time.Minute)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	if _, _, ok := c.get("a@x.com"); ok {
		t.Fatal("empty cache should miss")
	}
	c.put("a@x.com", "U1")
	if id, found, ok := c.get("A@X.com"); !ok || !found || id != "U1" {
		t.Fatalf("want hit U1 (case-insensitive), got id=%q found=%v ok=%v", id, found, ok)
	}
	c.put("ghost@x.com", "") // negative
	if id, found, ok := c.get("ghost@x.com"); !ok || found || id != "" {
		t.Fatalf("want cached miss, got id=%q found=%v ok=%v", id, found, ok)
	}
	now = now.Add(45 * time.Minute) // past miss TTL, before hit TTL
	if _, _, ok := c.get("ghost@x.com"); ok {
		t.Fatal("negative entry should have expired")
	}
	if _, _, ok := c.get("a@x.com"); !ok {
		t.Fatal("hit entry should still be live")
	}
}
