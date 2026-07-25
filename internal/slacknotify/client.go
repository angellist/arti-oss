package slacknotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// slackClient is the minimal Slack Web API surface the notifier needs. It sits
// behind an interface so tests inject a fake — unit tests never hit the network.
type slackClient interface {
	LookupUserByEmail(ctx context.Context, email string) (userID string, err error)
	PostDM(ctx context.Context, userID, markdown string) error
}

// errNoSlackUser is returned by LookupUserByEmail when Slack has no account for
// the email (the `users_not_found` API error). The notifier negative-caches
// this so the same address isn't re-looked-up on every event.
var errNoSlackUser = errors.New("slacknotify: no slack user for email")

// Retry / rate-limit tuning. Slack's chat.postMessage is ~1 msg/sec per channel
// and returns HTTP 429 + Retry-After when exceeded; transient network/5xx
// errors also happen. We retry a bounded number of times, honoring Retry-After,
// with exponential backoff otherwise — all capped so a fire-and-forget goroutine
// can't hang (the caller's context also bounds total time).
const (
	defaultMaxAttempts = 3
	defaultBaseBackoff = 250 * time.Millisecond
	maxBackoff         = 15 * time.Second
	outboundPerSec     = 5 // global token-bucket rate (smooths bursts)
	outboundBurst      = 5
)

// httpSlackClient talks to the Slack Web API with a bot user OAuth token.
// Required bot scopes: users:read.email (lookup), chat:write + im:write (DM).
type httpSlackClient struct {
	token       string
	baseURL     string // "https://slack.com/api" in prod; overridden in tests
	httpc       *http.Client
	limiter     *tokenBucket  // nil → no rate limiting (tests)
	maxAttempts int           // retry attempts incl. the first
	baseBackoff time.Duration // exponential-backoff base
}

func newHTTPClient(token string) *httpSlackClient {
	return &httpSlackClient{
		token:       token,
		baseURL:     "https://slack.com/api",
		httpc:       &http.Client{Timeout: 10 * time.Second},
		limiter:     newTokenBucket(outboundPerSec, outboundBurst),
		maxAttempts: defaultMaxAttempts,
		baseBackoff: defaultBaseBackoff,
	}
}

func (c *httpSlackClient) LookupUserByEmail(ctx context.Context, email string) (string, error) {
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	// Idempotent read → safe to retry on any transient failure.
	if err := c.call(ctx, "users.lookupByEmail", url.Values{"email": {email}}, &out, true); err != nil {
		return "", err
	}
	if !out.OK {
		if out.Error == "users_not_found" {
			return "", errNoSlackUser
		}
		return "", fmt.Errorf("slack users.lookupByEmail: %s", out.Error)
	}
	return out.User.ID, nil
}

func (c *httpSlackClient) PostDM(ctx context.Context, userID, markdown string) error {
	// channel = a user ID makes Slack open (or reuse) the IM with that user.
	form := url.Values{
		"channel":      {userID},
		"text":         {markdown},
		"mrkdwn":       {"true"},
		"unfurl_links": {"false"},
		"unfurl_media": {"false"},
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	// Posting a DM is NOT idempotent: an ambiguous failure (timeout / dropped
	// connection / 5xx) might mean Slack already delivered it. Pass idempotent
	// =false so we retry ONLY on a definitive 429 rejection, never risking a
	// duplicate DM.
	if err := c.call(ctx, "chat.postMessage", form, &out, false); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack chat.postMessage: %s", out.Error)
	}
	return nil
}

// call performs a Slack Web API request with global rate limiting and bounded,
// Retry-After-aware retry. On HTTP 429 it waits the server-suggested delay; for
// an `idempotent` call it also backs off and retries on transient network/5xx
// errors. A NON-idempotent call (chat.postMessage) is retried ONLY on a 429 —
// an ambiguous network/5xx failure might mean the DM was already delivered, and
// retrying would duplicate it. App-level errors (HTTP 200 with `ok:false`) are
// never retried — they're returned via `out` for the caller to interpret.
func (c *httpSlackClient) call(ctx context.Context, method string, form url.Values, out any, idempotent bool) error {
	var lastErr error
	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		if c.limiter != nil {
			if err := c.limiter.wait(ctx); err != nil {
				return err
			}
		}
		retryable, retryAfter, err := c.doOnce(ctx, method, form, out, idempotent)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == c.maxAttempts-1 {
			break
		}
		delay := retryAfter
		if delay <= 0 {
			delay = c.baseBackoff << attempt // 250ms, 500ms, …
		}
		if delay > maxBackoff {
			delay = maxBackoff
		}
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
	}
	return lastErr
}

// doOnce makes a single request. It reports whether the failure is worth
// retrying (a 429 always is; ambiguous network/5xx failures only for an
// idempotent call) and, for a 429, how long Slack asked us to wait.
func (c *httpSlackClient) doOnce(ctx context.Context, method string, form url.Values, out any, idempotent bool) (retryable bool, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpc.Do(req)
	if err != nil {
		// Ambiguous: the request may have reached Slack. Retry only if idempotent.
		return idempotent, 0, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		// Rate-limited = definitively rejected, so retrying can't duplicate.
		return true, parseRetryAfter(resp.Header), fmt.Errorf("slack %s: 429 rate limited", method)
	case resp.StatusCode >= 500:
		// Server error is ambiguous for a non-idempotent call.
		return idempotent, 0, fmt.Errorf("slack %s: status %d", method, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return false, 0, fmt.Errorf("slack %s: status %d", method, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, 0, err
	}
	return false, 0, nil
}

func parseRetryAfter(h http.Header) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
