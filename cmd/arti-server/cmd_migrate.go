package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/angellist/arti-oss/internal/config"
)

// MigrateCmd applies `goose up` against the configured database.
type MigrateCmd struct {
	Dir string `default:"db/migrations"`
}

// gooseLogger routes goose's progress output through slog, so the migrate
// init-container emits the same one-JSON-line-per-event stream as the server.
//
// goose's default logger is Go's stdlib log: plain text on stderr. Datadog
// parses a JSON line's status from the JSON and otherwise guesses it from the
// stream, so stderr plain text becomes ERROR — which classified every routine
// migration line ("OK 0001_baseline.sql (1.7ms)", "no migrations to run",
// "successfully migrated database to version: 21") as an error. Those were 204
// of the 564 ERROR events arti produced in a week, every one of them a
// success, and they buried the one migration that did fail. Same reasoning as
// the slog.SetDefault call in cmd_serve.go.
type gooseLogger struct{ l *slog.Logger }

// Printf handles goose's progress output. goose ends its format strings with a
// newline for the stdlib logger; slog does its own line framing, so trim it or
// the event splits in two at ingest.
func (g gooseLogger) Printf(format string, v ...any) {
	g.l.Info(strings.TrimRight(fmt.Sprintf(format, v...), "\n"))
}

// Fatalf keeps goose's contract that a fatal migration error ends the process
// — the init-container must fail so the rollout holds on the old replicas.
func (g gooseLogger) Fatalf(format string, v ...any) {
	g.l.Error(strings.TrimRight(fmt.Sprintf(format, v...), "\n"))
	os.Exit(1)
}

func (c *MigrateCmd) Run(_ *kong.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	goose.SetLogger(gooseLogger{l: logger})

	cfg, err := config.Load(config.RequireDatabase)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", cfg.Database.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(os.DirFS("."))
	return goose.Up(db, c.Dir)
}
