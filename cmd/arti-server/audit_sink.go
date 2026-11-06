package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"

	"github.com/angellist/arti-oss/internal/audit"
	"github.com/angellist/arti-oss/internal/config"
)

// s3AuditUploader writes audit batches with SSE-S3, which the archive bucket's
// policy requires. In production the credential chain resolves to the pod
// role, which is the only writer the archive bucket accepts.
type s3AuditUploader struct {
	cli    *minio.Client
	bucket string
}

// Put is create-only (If-None-Match: *), which the bucket policy enforces. A
// 412 means a retried upload already landed, so it counts as success.
func (u s3AuditUploader) Put(ctx context.Context, key string, body []byte) error {
	opts := minio.PutObjectOptions{
		ContentType:          "application/x-ndjson",
		ContentEncoding:      "gzip",
		ServerSideEncryption: encrypt.NewSSE(),
	}
	opts.SetMatchETagExcept("*")
	_, err := u.cli.PutObject(ctx, u.bucket, key, bytes.NewReader(body), int64(len(body)), opts)
	if minio.ToErrorResponse(err).StatusCode == http.StatusPreconditionFailed {
		return nil
	}
	return err
}

// newAuditSinks returns the log sink, plus the S3 archive when a bucket is
// configured. The returned run function drives the archive's flush loop.
func newAuditSinks(cfg *config.Config, logger *slog.Logger) (audit.Sink, func(context.Context), error) {
	logSink := audit.LogSink{L: logger}
	if cfg.Audit.Bucket == "" {
		logger.Warn("audit archive disabled (ARTI_AUDIT_BUCKET unset); audit events go to the log only")
		return logSink, func(context.Context) {}, nil
	}
	endpoint := cfg.Audit.Endpoint
	if endpoint == "" {
		endpoint = cfg.Storage.Endpoint
	}
	region := cfg.Audit.Region
	if region == "" {
		region = cfg.Storage.Region
	}
	cli, err := minio.New(trimScheme(endpoint), &minio.Options{
		Creds: credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{}, &credentials.FileAWSCredentials{}, &credentials.IAM{},
		}),
		Secure: true,
		Region: region,
	})
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	batch := &audit.BatchSink{
		Up:       s3AuditUploader{cli: cli, bucket: cfg.Audit.Bucket},
		Host:     host,
		Interval: cfg.Audit.FlushInterval.Std(),
		Log:      logger,
	}
	logger.Info("audit archive enabled", "bucket", cfg.Audit.Bucket, "region", region)
	return audit.Sinks{logSink, batch}, batch.Run, nil
}
