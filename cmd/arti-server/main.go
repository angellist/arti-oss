package main

import (
	"github.com/alecthomas/kong"
)

func main() {
	ctx := kong.Parse(&CLI{},
		kong.Name("arti-server"),
		kong.Description("arti — artifact service (REST + MCP + OAuth)"),
		kong.UsageOnError(),
	)
	ctx.FatalIfErrorf(ctx.Run(ctx))
}

type CLI struct {
	Serve   ServeCmd   `cmd:"" help:"run the HTTP server"`
	Migrate MigrateCmd `cmd:"" help:"apply DB migrations"`
	Reindex ReindexCmd `cmd:"" help:"backfill the OpenSearch index from Postgres"`
	Doctor  DoctorCmd  `cmd:"" help:"verify the configuration end to end (config, database, object store, issuer)"`
}
