package main

import (
	"database/sql"
	"os"

	"github.com/alecthomas/kong"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/angellist/arti-oss/internal/config"
)

// MigrateCmd applies `goose up` against the configured database.
type MigrateCmd struct {
	Dir string `default:"db/migrations"`
}

func (c *MigrateCmd) Run(_ *kong.Context) error {
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
