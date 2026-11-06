package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	chimid "github.com/go-chi/chi/v5/middleware"

	"github.com/angellist/arti-oss/internal/audit"
)

type captureSink struct{ evs []audit.Event }

func (c *captureSink) Write(ev audit.Event) { c.evs = append(c.evs, ev) }

func auditRouter(sink audit.Sink, register func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Use(chimid.RequestID)
	r.Use(reqLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), sink))
	register(r)
	return r
}

func TestAuditEventRecordsAddressedSlugOnDenial(t *testing.T) {
	sink := &captureSink{}
	h := auditRouter(sink, func(r chi.Router) {
		r.Get("/api/artifacts/by-slug/{slug}/raw", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/artifacts/by-slug/secret-doc/raw", nil))

	if len(sink.evs) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.evs))
	}
	ev := sink.evs[0]
	if ev.Outcome != "denied" || ev.Route != "/api/artifacts/by-slug/{slug}/raw" || ev.RequestID == "" {
		t.Errorf("event = %+v", ev)
	}
	if len(ev.Targets) != 1 || ev.Targets[0].Slug != "secret-doc" {
		t.Errorf("targets = %+v, want the addressed slug", ev.Targets)
	}
}

func TestAuditEventCarriesHandlerAnnotations(t *testing.T) {
	sink := &captureSink{}
	h := auditRouter(sink, func(r chi.Router) {
		r.Post("/mcp", func(w http.ResponseWriter, r *http.Request) {
			audit.From(r.Context()).AddTool(audit.ToolCall{Name: "read_artifact", Ident: "doc", Outcome: "allowed"})
			audit.From(r.Context()).AddTarget(audit.Target{ArtifactID: "a1", Slug: "doc", Decision: "allowed"})
		})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))

	ev := sink.evs[0]
	if ev.Surface != "mcp" || len(ev.Tools) != 1 || ev.Tools[0].Name != "read_artifact" {
		t.Errorf("event = %+v", ev)
	}
	if len(ev.Targets) != 1 || ev.Targets[0].ArtifactID != "a1" {
		t.Errorf("targets = %+v", ev.Targets)
	}
}

func TestAuditEventTreatsUUIDIdentAsArtifactID(t *testing.T) {
	sink := &captureSink{}
	h := auditRouter(sink, func(r chi.Router) {
		r.Get("/app/{ident}/{version}", func(w http.ResponseWriter, r *http.Request) {})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/3fa2c1d0-1b9e-4190-88a4-21140a6320d1/3", nil))

	tg := sink.evs[0].Targets
	if len(tg) != 1 || tg[0].ArtifactID != "3fa2c1d0-1b9e-4190-88a4-21140a6320d1" || tg[0].Slug != "" || tg[0].Version == nil || *tg[0].Version != 3 {
		t.Errorf("targets = %+v", tg)
	}
}

func TestAuditEventNeverRecordsTokens(t *testing.T) {
	sink := &captureSink{}
	h := auditRouter(sink, func(r chi.Router) {
		r.Get("/share/{token}", func(w http.ResponseWriter, r *http.Request) {})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/share/live-secret-token", nil))

	ev := sink.evs[0]
	if bytes.Contains([]byte(ev.Path), []byte("live-secret-token")) || len(ev.Targets) != 0 {
		t.Errorf("token leaked into audit event: %+v", ev)
	}
	if ev.Surface != "share_link" {
		t.Errorf("surface = %q", ev.Surface)
	}
}

func TestAuditSkipsHealthz(t *testing.T) {
	sink := &captureSink{}
	h := auditRouter(sink, func(r chi.Router) {
		r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if len(sink.evs) != 0 {
		t.Fatalf("healthz audited: %+v", sink.evs)
	}
}

func TestAuditEventIgnoresNonDocumentIDs(t *testing.T) {
	sink := &captureSink{}
	h := auditRouter(sink, func(r chi.Router) {
		r.Delete("/api/keys/{id}", func(w http.ResponseWriter, r *http.Request) {})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/keys/3fa2c1d0-1b9e-4190-88a4-21140a6320d1", nil))

	ev := sink.evs[0]
	if len(ev.Targets) != 0 || ev.Route != "/api/keys/{id}" {
		t.Errorf("api key id recorded as a document: %+v", ev)
	}
}
