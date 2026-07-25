package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/alecthomas/kong"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/config"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/opensearch"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ReindexCmd backfills the OpenSearch index from Postgres. Idempotent —
// safe to re-run at any time. Run as `arti-server reindex`. Configuration
// comes from internal/config; the batch size is REINDEX_BATCH_SIZE
// (search.reindex_batch_size).
type ReindexCmd struct{}

func (*ReindexCmd) Run(_ *kong.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load(config.RequireDatabase, config.RequireStorage, config.RequireSearch)
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	s3, err := blob.NewS3(blob.S3Config{
		Endpoint:  trimScheme(cfg.Storage.Endpoint),
		AccessKey: cfg.Storage.AccessKey,
		SecretKey: cfg.Storage.SecretKey,
		Bucket:    cfg.Storage.Bucket,
		Region:    cfg.Storage.Region,
		UseSSL:    cfg.Storage.UseSSL,
	})
	if err != nil {
		return fmt.Errorf("init s3: %w", err)
	}

	pgstoreInst := pgstore.New(pool, s3, pgstore.Config{})

	osCfg := opensearch.Config{
		Endpoint: cfg.Search.Endpoint,
		Username: cfg.Search.Username,
		Password: cfg.Search.Password,
		Region:   cfg.Search.Region,
	}
	osClient := opensearch.New(osCfg, logger)
	if osClient == nil {
		return fmt.Errorf("opensearch endpoint is empty")
	}

	if err := osClient.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("ensure index: %w", err)
	}

	indexer := opensearch.NewIndexer(osClient, pgstoreInst, logger)

	// Page through all artifacts using List with offset pagination.
	// Include archived so the index mirrors the full DB state.
	// NOTE (cursor-bot): offset pagination over a created_at DESC list can skip
	// or duplicate rows if writes shift the ordering mid-run, leaving an
	// incomplete index. Acceptable here — reindex is an idempotent ops command
	// (re-running reconciles), best run during low write activity.
	// TODO(arti#87): switch to keyset pagination (created_at ASC + artifact_id)
	// so concurrent inserts can't drop rows.
	total := 0
	offset := int32(0)
	batchSize := int32(cfg.Search.ReindexBatchSize)
	start := time.Now()

	for {
		res, lerr := pgstoreInst.List(ctx, pgstore.ListInput{
			Limit:           batchSize,
			Offset:          offset,
			IncludeArchived: true,
		})
		if lerr != nil {
			return fmt.Errorf("list artifacts at offset %d: %w", offset, lerr)
		}
		if len(res.Rows) == 0 {
			break
		}

		for _, row := range res.Rows {
			indexer.IndexArtifact(ctx, row)
			total++
		}

		logger.Info("reindex progress",
			"indexed", total,
			"db_total", res.Total,
			"elapsed", time.Since(start).Round(time.Second))

		if int32(len(res.Rows)) < batchSize {
			break
		}
		offset += batchSize
	}

	logger.Info("reindex: documents indexed, sweeping is_latest flags",
		"total_indexed", total,
		"elapsed", time.Since(start).Round(time.Second))

	// Second pass: fix is_latest flags per slug. rowToDoc defaults to
	// is_latest=true, but only the highest non-deleted version should
	// be true. Collect distinct slugs, then update each.
	slugSet := make(map[string]struct{})
	offset = 0
	for {
		res, lerr := pgstoreInst.List(ctx, pgstore.ListInput{
			Limit:           batchSize,
			Offset:          offset,
			IncludeArchived: true,
		})
		if lerr != nil {
			return fmt.Errorf("list slugs at offset %d: %w", offset, lerr)
		}
		if len(res.Rows) == 0 {
			break
		}
		for _, row := range res.Rows {
			if row.NamedSlug != nil && *row.NamedSlug != "" {
				slugSet[*row.NamedSlug] = struct{}{}
			}
		}
		if int32(len(res.Rows)) < batchSize {
			break
		}
		offset += batchSize
	}

	slugCount := 0
	for slug := range slugSet {
		indexer.UpdateLatestFlags(ctx, slug)
		slugCount++
	}
	logger.Info("reindex complete",
		"total_indexed", total,
		"slugs_swept", slugCount,
		"elapsed", time.Since(start).Round(time.Second))
	return nil
}
