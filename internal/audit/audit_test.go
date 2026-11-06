package audit

import (
	"context"
	"testing"
)

func i32(n int32) *int32 { return &n }

func TestAddTargetMergesAndDenialWins(t *testing.T) {
	_, e := WithEntry(context.Background())
	e.AddTarget(Target{ArtifactID: "a1", Slug: "doc", Version: i32(2), Decision: "allowed"})
	e.AddTarget(Target{ArtifactID: "a1", Decision: "denied"})
	e.AddTarget(Target{Slug: "DOC"})
	e.AddTarget(Target{ArtifactID: "a2", Slug: "doc", Version: i32(1), Decision: "allowed"})

	var ev Event
	e.Fill(&ev)
	if len(ev.Targets) != 2 {
		t.Fatalf("targets = %+v, want 2 rows", ev.Targets)
	}
	if got := ev.Targets[0]; got.Decision != "denied" || got.Slug != "doc" || *got.Version != 2 {
		t.Errorf("first target = %+v, want denied doc v2", got)
	}
}

func TestAddTargetAllowDoesNotOverrideDenial(t *testing.T) {
	_, e := WithEntry(context.Background())
	e.AddTarget(Target{Slug: "doc", Decision: "denied"})
	e.AddTarget(Target{ArtifactID: "a1", Slug: "doc", Decision: "allowed"})
	var ev Event
	e.Fill(&ev)
	if len(ev.Targets) != 1 || ev.Targets[0].Decision != "denied" || ev.Targets[0].ArtifactID != "a1" {
		t.Fatalf("targets = %+v", ev.Targets)
	}
}

func TestNilEntryIsSafe(t *testing.T) {
	e := From(context.Background())
	e.AddTarget(Target{Slug: "x"})
	e.AddTool(ToolCall{Name: "read_artifact"})
	e.SetShare("s")
	e.SetViewer("v@example.com")
	e.Fill(&Event{})
}

func TestEmptyTargetIgnored(t *testing.T) {
	_, e := WithEntry(context.Background())
	e.AddTarget(Target{Decision: "allowed"})
	var ev Event
	e.Fill(&ev)
	if len(ev.Targets) != 0 {
		t.Fatalf("targets = %+v", ev.Targets)
	}
}

func TestSurface(t *testing.T) {
	cases := []struct{ path, cred, want string }{
		{"/mcp", "token", "mcp"},
		{"/share/<redacted>", "", "share_link"},
		{"/embed/notion/doc", "", "embed"},
		{"/api/embed/artifacts/a1/comments", "", "embed"},
		{"/app/my-app", "session", "app"},
		{"/api/apps/mcp", "session", "app"},
		{"/api/artifacts/x/files-token/t/a.css", "", "file_token"},
		{"/api/artifacts", "session", "web"},
		{"/api/artifacts", "apikey:123", "api_key"},
		{"/api/artifacts", "device:f", "cli"},
		{"/api/artifacts", "token", "bearer_token"},
		{"/api/artifacts", "service", "service"},
		{"/api/artifacts", "", "anonymous"},
	}
	for _, c := range cases {
		if got := Surface(c.path, c.cred); got != c.want {
			t.Errorf("Surface(%q, %q) = %q, want %q", c.path, c.cred, got, c.want)
		}
	}
}

func TestOutcome(t *testing.T) {
	cases := map[int]string{200: "allowed", 204: "allowed", 302: "allowed", 400: "rejected",
		401: "denied", 403: "denied", 404: "denied", 429: "rejected", 499: "client_closed", 500: "error"}
	for status, want := range cases {
		if got := Outcome(status); got != want {
			t.Errorf("Outcome(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestFillCarriesViewerAndApp(t *testing.T) {
	_, e := WithEntry(context.Background())
	e.SetViewer("v@example.com")
	e.SetApp("app-1")
	e.SetViewer("")
	var ev Event
	e.Fill(&ev)
	if ev.Viewer != "v@example.com" || ev.App != "app-1" {
		t.Fatalf("event = %+v", ev)
	}
}
