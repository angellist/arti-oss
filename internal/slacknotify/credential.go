package slacknotify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MintedKey is an API key that has just been created under someone's identity.
type MintedKey struct {
	Name      string
	KeyPrefix string
	Scopes    []string
	ExpiresAt time.Time
}

// NotifyKeyMinted tells an owner that a key now exists under their name. Sent
// on every mint, including their own: the point is that a key can never appear
// silently, which is how one ended up shared inside an agent platform with
// nobody aware it had been created.
func (n *Notifier) NotifyKeyMinted(ctx context.Context, ownerEmail string, k MintedKey) {
	if !n.permits(ctx, ownerEmail, CategoryCredentialMint) {
		return
	}
	n.dm(ctx, ownerEmail, fmt.Sprintf(
		":key: *An API key was created under your account*\n"+
			"`%s` · %s · `%s` · expires %s\n"+
			"If this was not you, revoke it: <%s|Settings → API Keys>",
		slackEscape(k.Name), slackEscape(strings.Join(k.Scopes, ", ")), slackEscape(k.KeyPrefix),
		k.ExpiresAt.Format("2006-01-02"), settingsKeysURL))
}

// NewCredentialSource is an API key being used from a network it has never been
// used from before. Only keys reach here: they are the credential that gets
// copied into somewhere else and used by other people.
type NewCredentialSource struct {
	CredName  string // the owner's name for the key
	Network   string // the address group, e.g. 34.72.0.0/24 or 2601:643:8b00::/64
	IP        string // the exact address, shown but not what made this new
	UserAgent string // shown for recognition; deliberately not part of the key
	Writes    int64
}

// NotifyNewCredentialSource tells a key's owner it answered from a new network.
// Scoped to keys on purpose. It went out for every credential at first, and one
// afternoon of real traffic sent 62 DMs to 15 people — every one of them
// somebody's own laptop tools, because a personal bearer reports as the same
// nameless reference for every client, user agents carry version numbers, and
// IPv6 privacy addressing rotates. None was about a key.
func (n *Notifier) NotifyNewCredentialSource(ctx context.Context, ownerEmail string, ev NewCredentialSource) {
	if !n.permits(ctx, ownerEmail, CategoryCredentialNewSource) {
		return
	}
	msg := fmt.Sprintf(
		":key: *Your API key `%s` was used from a new network*\n"+
			"%s (%s) · `%s`\n",
		slackEscape(ev.CredName), slackEscape(ev.Network), slackEscape(ev.IP), slackEscape(ev.UserAgent))
	if ev.Writes > 0 {
		msg += fmt.Sprintf("%d document write(s) from it so far.\n", ev.Writes)
	}
	msg += fmt.Sprintf("Expected? Nothing to do. If not: <%s|Settings → API Keys>", settingsKeysURL)
	n.dm(ctx, ownerEmail, msg)
}

// settingsKeysURL is where an owner acts on the message. Relative links do not
// work in Slack, and the notifier has no other reason to know the base URL, so
// it is set once at startup by SetBaseURL.
var settingsKeysURL = "/settings/keys"

// SetBaseURL makes the links in credential messages absolute.
func SetBaseURL(base string) {
	if base = strings.TrimRight(strings.TrimSpace(base), "/"); base != "" {
		settingsKeysURL = base + "/settings/keys"
	}
}

func (n *Notifier) dm(ctx context.Context, email, markdown string) {
	if n == nil {
		return
	}
	userID, ok := n.resolve(ctx, email)
	if !ok {
		return
	}
	if err := n.client.PostDM(ctx, userID, markdown); err != nil {
		n.log.Warn("slacknotify: credential DM failed", "email", email, "err", err)
	}
}
