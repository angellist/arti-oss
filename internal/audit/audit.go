// Package audit records one access event per request: who, through which
// credential and surface, touched which documents, and what the outcome was.
// Handlers annotate the per-request Entry; the request logger turns it into an
// Event and hands it to every Sink.
package audit

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Target is one document a request addressed or resolved.
type Target struct {
	ArtifactID string `json:"artifact_id,omitempty"`
	Slug       string `json:"slug,omitempty"`
	Version    *int32 `json:"version,omitempty"`
	// Decision is "allowed" or "denied" when an access check ran on the row;
	// empty when the request only named the document.
	Decision string `json:"decision,omitempty"`
}

// ToolCall is one MCP tool invocation inside a request.
type ToolCall struct {
	Name    string `json:"name"`
	Ident   string `json:"ident,omitempty"`
	Outcome string `json:"outcome"`
}

// Event is the record written to every sink.
type Event struct {
	Time      time.Time  `json:"ts"`
	RequestID string     `json:"request_id,omitempty"`
	Actor     string     `json:"actor,omitempty"`
	Cred      string     `json:"cred,omitempty"`
	Surface   string     `json:"surface"`
	IP        string     `json:"ip,omitempty"`
	UserAgent string     `json:"user_agent,omitempty"`
	Method    string     `json:"method"`
	Route     string     `json:"route,omitempty"`
	Path      string     `json:"path"`
	Status    int        `json:"status"`
	Outcome   string     `json:"outcome"`
	Targets   []Target   `json:"targets,omitempty"`
	Tools     []ToolCall `json:"tools,omitempty"`
	ShareID   string     `json:"share_id,omitempty"`
	Viewer    string     `json:"viewer,omitempty"`
	App       string     `json:"app,omitempty"`
}

// Entry collects annotations for one request. Methods are safe on a nil
// receiver, so code paths outside an HTTP request (tests, jobs) need no guard.
type Entry struct {
	mu      sync.Mutex
	targets []Target
	tools   []ToolCall
	shareID string
	viewer  string
	app     string
}

type entryKey struct{}

// WithEntry returns ctx carrying a fresh Entry.
func WithEntry(ctx context.Context) (context.Context, *Entry) {
	e := &Entry{}
	return context.WithValue(ctx, entryKey{}, e), e
}

// From returns the request's Entry, or nil.
func From(ctx context.Context) *Entry {
	e, _ := ctx.Value(entryKey{}).(*Entry)
	return e
}

// AddTarget records a document, merging with an existing target for the same
// artifact id (or slug when no id is known) so repeated checks stay one row.
func (e *Entry) AddTarget(t Target) {
	if e == nil || (t.ArtifactID == "" && t.Slug == "") {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.targets {
		cur := &e.targets[i]
		sameSlug := t.Slug != "" && strings.EqualFold(cur.Slug, t.Slug) && sameVersion(cur.Version, t.Version)
		if (t.ArtifactID != "" && cur.ArtifactID == t.ArtifactID) ||
			((t.ArtifactID == "" || cur.ArtifactID == "") && sameSlug) {
			if cur.ArtifactID == "" {
				cur.ArtifactID = t.ArtifactID
			}
			if cur.Slug == "" {
				cur.Slug = t.Slug
			}
			if cur.Version == nil {
				cur.Version = t.Version
			}
			// A denial anywhere in the request wins over an allow.
			if t.Decision == "denied" || cur.Decision == "" {
				if t.Decision != "" {
					cur.Decision = t.Decision
				}
			}
			return
		}
	}
	e.targets = append(e.targets, t)
}

func sameVersion(a, b *int32) bool {
	return a == nil || b == nil || *a == *b
}

// AddTool records one MCP tool call.
func (e *Entry) AddTool(tc ToolCall) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.tools = append(e.tools, tc)
	e.mu.Unlock()
}

// SetShare records the share link a request was served through.
func (e *Entry) SetShare(id string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.shareID = id
	e.mu.Unlock()
}

// SetViewer records the viewer identity on surfaces that carry their own
// credential (embeds, apps) rather than arti's auth middleware.
func (e *Entry) SetViewer(email string) {
	if e == nil || email == "" {
		return
	}
	e.mu.Lock()
	e.viewer = email
	e.mu.Unlock()
}

// SetApp records the APP artifact a request ran on behalf of.
func (e *Entry) SetApp(id string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.app = id
	e.mu.Unlock()
}

// Fill copies the collected annotations onto ev.
func (e *Entry) Fill(ev *Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ev.Targets = append([]Target(nil), e.targets...)
	ev.Tools = append([]ToolCall(nil), e.tools...)
	ev.ShareID = e.shareID
	ev.Viewer = e.viewer
	ev.App = e.app
}

// Sink receives finished events. Write must not block the request.
type Sink interface {
	Write(Event)
}

// Sinks fans an event out to several sinks.
type Sinks []Sink

func (s Sinks) Write(ev Event) {
	for _, k := range s {
		k.Write(ev)
	}
}

// LogSink writes each event as one structured "audit" log line.
type LogSink struct{ L *slog.Logger }

func (s LogSink) Write(ev Event) {
	attrs := []any{
		"surface", ev.Surface, "m", ev.Method, "route", ev.Route, "p", ev.Path,
		"s", ev.Status, "outcome", ev.Outcome,
	}
	if ev.RequestID != "" {
		attrs = append(attrs, "request_id", ev.RequestID)
	}
	if ev.Actor != "" {
		attrs = append(attrs, "email", ev.Actor)
	}
	if ev.Cred != "" {
		attrs = append(attrs, "cred", ev.Cred)
	}
	if ev.IP != "" {
		attrs = append(attrs, "ip", ev.IP)
	}
	if ev.Viewer != "" {
		attrs = append(attrs, "viewer", ev.Viewer)
	}
	if ev.ShareID != "" {
		attrs = append(attrs, "share_id", ev.ShareID)
	}
	if ev.App != "" {
		attrs = append(attrs, "app", ev.App)
	}
	if len(ev.Targets) > 0 {
		attrs = append(attrs, "targets", ev.Targets)
	}
	if len(ev.Tools) > 0 {
		attrs = append(attrs, "tools", ev.Tools)
	}
	s.L.Info("audit", attrs...)
}

// Outcome classifies an HTTP status.
func Outcome(status int) string {
	switch {
	case status == 401 || status == 403 || status == 404:
		return "denied"
	case status == 499:
		return "client_closed"
	case status >= 500:
		return "error"
	case status >= 400:
		return "rejected"
	default:
		return "allowed"
	}
}

// Surface names the channel a request came through. Path-owned surfaces win;
// otherwise the credential kind decides.
func Surface(path, cred string) string {
	switch {
	case path == "/mcp" || strings.HasPrefix(path, "/mcp/"):
		return "mcp"
	case strings.HasPrefix(path, "/share/"):
		return "share_link"
	case strings.HasPrefix(path, "/embed/") || strings.HasPrefix(path, "/api/embed/"):
		return "embed"
	case strings.HasPrefix(path, "/app/") || strings.HasPrefix(path, "/api/apps/"):
		return "app"
	case strings.Contains(path, "/files-token/"):
		return "file_token"
	case strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/oauth/"):
		return "auth"
	}
	kind, _, _ := strings.Cut(cred, ":")
	switch kind {
	case "session":
		return "web"
	case "apikey":
		return "api_key"
	case "device":
		return "cli"
	case "token":
		return "bearer_token"
	case "service":
		return "service"
	case "":
		return "anonymous"
	}
	return kind
}
