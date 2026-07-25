package slacknotify

import (
	"strings"
	"sync"
	"time"
)

// idCache memoizes email→Slack-user-ID lookups. Slack's users.lookupByEmail
// is rate-limited, so we cache both hits (the user ID) and misses (a "no Slack
// user for this email" answer) — otherwise a non-Slack address would be
// re-looked-up on every single comment event. Entries expire so someone who
// later joins Slack, or whose email changes, is eventually re-resolved.
// In-memory only; rebuilt cheaply after a restart.
type idCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	hitTTL  time.Duration
	missTTL time.Duration
	now     func() time.Time // injectable for tests
}

type cacheEntry struct {
	userID  string // "" means a cached miss (no Slack user for this email)
	expires time.Time
}

func newIDCache(hitTTL, missTTL time.Duration) *idCache {
	return &idCache{
		entries: map[string]cacheEntry{},
		hitTTL:  hitTTL,
		missTTL: missTTL,
		now:     time.Now,
	}
}

// get reports a live cache entry for email. ok=false means there is no live
// entry and the caller must look the email up. When ok=true, found indicates
// whether a Slack user exists (false = a cached miss); userID is set only when
// found is true.
func (c *idCache) get(email string) (userID string, found, ok bool) {
	key := strings.ToLower(strings.TrimSpace(email))
	c.mu.Lock()
	defer c.mu.Unlock()
	e, present := c.entries[key]
	if !present || c.now().After(e.expires) {
		return "", false, false
	}
	return e.userID, e.userID != "", true
}

// put stores a resolution. An empty userID records a negative (miss) entry
// with the shorter miss TTL.
func (c *idCache) put(email, userID string) {
	ttl := c.hitTTL
	if userID == "" {
		ttl = c.missTTL
	}
	key := strings.ToLower(strings.TrimSpace(email))
	c.mu.Lock()
	c.entries[key] = cacheEntry{userID: userID, expires: c.now().Add(ttl)}
	c.mu.Unlock()
}
