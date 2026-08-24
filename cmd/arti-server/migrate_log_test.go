package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// goose's default logger is Go's stdlib log, which writes plain text to
// stderr. Datadog guesses a plain-text line's status from the stream it
// arrived on, so every routine migration line — "OK 0001_baseline.sql
// (1.7ms)", "no migrations to run", "successfully migrated database to
// version: 21" — was ingested as an ERROR. Those four patterns were 204 of
// the 564 ERROR events arti produced in a week, all of them successes.
// cmd_serve already routes slog through a JSON handler for this reason; the
// migrate init-container has to do the same for goose's own output.
func TestGooseLoggerEmitsStructuredInfo(t *testing.T) {
	var buf bytes.Buffer
	l := gooseLogger{l: slog.New(slog.NewJSONHandler(&buf, nil))}

	goose.SetLogger(l)
	t.Cleanup(func() { goose.SetLogger(&noopGooseLogger{}) })

	l.Printf("OK   %s (%.2fms)\n", "0001_baseline.sql", 1.17)

	var rec struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("migration output is not one JSON line (%v): %q", err, buf.String())
	}
	if rec.Level != "INFO" {
		t.Errorf("level = %q, want INFO — a successful migration is not an error", rec.Level)
	}
	if !strings.Contains(rec.Msg, "0001_baseline.sql") {
		t.Errorf("migration detail lost, msg = %q", rec.Msg)
	}
	// A trailing newline inside the message would split the event in two at
	// ingest, which is the whole failure being fixed.
	if strings.Contains(rec.Msg, "\n") {
		t.Errorf("msg carries a newline, will split into two events: %q", rec.Msg)
	}
}

// One Printf must produce exactly one line, however goose formats it.
func TestGooseLoggerEmitsOneLinePerCall(t *testing.T) {
	var buf bytes.Buffer
	l := gooseLogger{l: slog.New(slog.NewJSONHandler(&buf, nil))}

	l.Printf("goose: no migrations to run. current version: %d\n", 21)
	l.Printf("OK   %s\n", "0021_comments_enabled.sql")

	if got := strings.Count(strings.TrimRight(buf.String(), "\n"), "\n") + 1; got != 2 {
		t.Errorf("2 Printf calls produced %d lines: %q", got, buf.String())
	}
}

// noopGooseLogger restores goose's package state after the test above without
// reaching for the stdlib logger it is the point of this change to avoid.
type noopGooseLogger struct{}

func (*noopGooseLogger) Printf(string, ...any) {}
func (*noopGooseLogger) Fatalf(string, ...any) { os.Exit(1) }
