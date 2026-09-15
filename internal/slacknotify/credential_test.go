package slacknotify

import (
	"context"
	"strings"
	"testing"
	"time"
)

type capturingClient struct{ posted []string }

func (c *capturingClient) LookupUserByEmail(context.Context, string) (string, error) {
	return "U123", nil
}
func (c *capturingClient) PostDM(_ context.Context, _ string, markdown string) error {
	c.posted = append(c.posted, markdown)
	return nil
}

func notifierWith(c *capturingClient) *Notifier {
	return &Notifier{client: c, cache: newIDCache(time.Hour, time.Minute), log: discardLogger(),
		// Tests exercise the messages; the admin switch has its own tests.
		allow: func(context.Context, string, string) bool { return true }}
}

// The user agent in this alert is chosen by whoever holds the credential —
// including the thief the alert exists to expose. Slack mrkdwn would turn
// `<https://evil|Revoke here>` into a clickable link inside a security warning
// sent by arti.
func TestNewSourceAlertEscapesTheUserAgent(t *testing.T) {
	c := &capturingClient{}
	notifierWith(c).NotifyNewCredentialSource(context.Background(), "owner@example.com", NewCredentialSource{
		CredName:  "ci-upload",
		IP:        "34.72.0.9",
		UserAgent: `<https://evil.example.com|Revoke here>`,
	})

	if len(c.posted) != 1 {
		t.Fatalf("posted %d messages, want 1", len(c.posted))
	}
	msg := c.posted[0]
	if strings.Contains(msg, "<https://evil.example.com|") {
		t.Errorf("the caller's link survived into the DM: %s", msg)
	}
	if !strings.Contains(msg, "&lt;https://evil.example.com") {
		t.Errorf("expected the angle brackets escaped, got: %s", msg)
	}
}

// The key's name is typed by whoever minted it, and the mint DM is the message
// that tells an owner a key they did not create now exists.
func TestMintAlertEscapesTheKeyName(t *testing.T) {
	c := &capturingClient{}
	notifierWith(c).NotifyKeyMinted(context.Background(), "owner@example.com", MintedKey{
		Name:      `<https://evil.example.com|ignore this>`,
		KeyPrefix: "arti_upload_AbCdE",
		Scopes:    []string{"upload"},
		ExpiresAt: time.Now(),
	})

	if len(c.posted) != 1 {
		t.Fatalf("posted %d messages, want 1", len(c.posted))
	}
	if strings.Contains(c.posted[0], "<https://evil.example.com|") {
		t.Errorf("the key name's link survived into the DM: %s", c.posted[0])
	}
}

// A notifier with no gate installed sends nothing. The default has to be
// silence: a deployment that has not decided must not start DMing people, and
// on 2026-09-04 the absence of a switch meant a rollback was the only way to
// stop a notification that was firing wrongly.
func TestUngatedNotifierSendsNothing(t *testing.T) {
	c := &capturingClient{}
	n := &Notifier{client: c, cache: newIDCache(time.Hour, time.Minute), log: discardLogger()}

	n.NotifyKeyMinted(context.Background(), "owner@example.com", MintedKey{Name: "k", ExpiresAt: time.Now()})
	n.NotifyNewCredentialSource(context.Background(), "owner@example.com", NewCredentialSource{CredName: "k"})
	n.Notify(context.Background(), Event{Owner: "owner@example.com", Action: ActionNewComment})

	if len(c.posted) != 0 {
		t.Fatalf("posted %d messages with no gate installed, want none", len(c.posted))
	}
}

// Each category is gated on its own, so an admin can leave comments on while
// credential notifications stay off.
func TestGateAppliesPerCategory(t *testing.T) {
	c := &capturingClient{}
	n := &Notifier{client: c, cache: newIDCache(time.Hour, time.Minute), log: discardLogger(),
		allow: func(_ context.Context, _ string, category string) bool { return category == CategoryCredentialMint }}

	n.NotifyNewCredentialSource(context.Background(), "owner@example.com", NewCredentialSource{CredName: "k"})
	if len(c.posted) != 0 {
		t.Fatalf("a disabled category sent %d messages", len(c.posted))
	}
	n.NotifyKeyMinted(context.Background(), "owner@example.com", MintedKey{Name: "k", ExpiresAt: time.Now()})
	if len(c.posted) != 1 {
		t.Fatalf("the enabled category sent %d messages, want 1", len(c.posted))
	}
}
