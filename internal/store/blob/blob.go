// Package blob is the blob-store abstraction. Artifact metadata lives in
// Postgres; Artifact content lives behind a [Store]. Implementations: in-memory
// (tests), S3 (AWS or MinIO via minio-go).
//
// The interface is deliberately small. Anything more elaborate (multipart,
// presigned URLs, lifecycle) is added as a backend-specific extension behind a
// type assertion, never expanded here.
package blob

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned by [Store] methods when the requested key is absent.
var ErrNotFound = errors.New("blob: not found")

// PutOpts is the optional configuration for a Put.
type PutOpts struct {
	// ContentType is sent as Content-Type to S3 / used as a hint by callers.
	ContentType string
	// Metadata is user-provided key/value pairs stored alongside the object.
	// Implementations may normalize keys (S3 lowercases header names).
	Metadata map[string]string
}

// PutResult describes a successful Put.
type PutResult struct {
	Key    string
	Size   int64
	SHA256 string
}

// ObjectInfo is the metadata returned by Stat / Get / List.
type ObjectInfo struct {
	Key          string
	Size         int64
	SHA256       string
	ContentType  string
	LastModified time.Time
}

// Store is the kernel-facing blob interface.
//
// Implementations MUST:
//   - compute SHA-256 of the bytes during Put and surface it in PutResult and ObjectInfo;
//   - return [ErrNotFound] from Get/Stat/Delete when the key is absent;
//   - be safe for concurrent use.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, opts PutOpts) (PutResult, error)
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}
