package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config configures an [S3] store. Endpoint format: "host:port", no scheme.
type S3Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// S3 is a [Store] backed by S3-compatible object storage. Works against AWS S3
// and MinIO interchangeably via minio-go.
type S3 struct {
	cli    *minio.Client
	bucket string
}

// NewS3 constructs an S3 store. The bucket must already exist; bootstrap is a
// deployment concern.
//
// Credential mode is picked from the config:
//   - AccessKey set → static credentials (MinIO local-dev, IAM users).
//   - AccessKey empty → chain creds (env → web identity / IRSA → EC2 metadata).
//     This is how the pod-identity-based AWS S3 access works in prod / staging.
func NewS3(cfg S3Config) (*S3, error) {
	var creds *credentials.Credentials
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.FileAWSCredentials{},
			&credentials.IAM{},
		})
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  creds,
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	return &S3{cli: cli, bucket: cfg.Bucket}, nil
}

// Put buffers the reader to compute SHA-256 + size, then uploads with the
// digest stored as user metadata.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, opts PutOpts) (PutResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return PutResult{}, err
	}
	sum := sha256.Sum256(data)
	shaHex := hex.EncodeToString(sum[:])

	meta := make(map[string]string, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta["sha256"] = shaHex

	if _, err := s.cli.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType:  opts.ContentType,
		UserMetadata: meta,
	}); err != nil {
		return PutResult{}, err
	}
	return PutResult{Key: key, Size: int64(len(data)), SHA256: shaHex}, nil
}

// Get returns the object reader and metadata. Caller must Close the reader.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	obj, err := s.cli.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, mapErr(err)
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, ObjectInfo{}, mapErr(err)
	}
	return obj, oiFromMinio(info), nil
}

// Stat returns metadata about a single object.
func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := s.cli.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, mapErr(err)
	}
	return oiFromMinio(info), nil
}

// Delete removes an object. Deleting a missing key is not an error.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := s.cli.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return mapErr(err)
	}
	return nil
}

// List enumerates objects under prefix. Returned ObjectInfos may have a zero
// SHA256 because S3 ListObjects does not include user metadata; callers needing
// the digest must Stat the keys.
func (s *S3) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	for o := range s.cli.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, mapErr(o.Err)
		}
		out = append(out, ObjectInfo{
			Key:          o.Key,
			Size:         o.Size,
			ContentType:  o.ContentType,
			LastModified: o.LastModified,
		})
	}
	sortObjectInfoByKey(out)
	return out, nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if r := minio.ToErrorResponse(err); r.StatusCode == 404 || r.Code == "NoSuchKey" {
		return ErrNotFound
	}
	return err
}

// oiFromMinio reads SHA-256 out of user metadata. minio-go normalizes keys,
// exposing them under multiple casings depending on whether the source was a
// Stat or a Get; check both.
func oiFromMinio(o minio.ObjectInfo) ObjectInfo {
	sha := userMeta(o, "sha256")
	return ObjectInfo{
		Key:          o.Key,
		Size:         o.Size,
		SHA256:       sha,
		ContentType:  o.ContentType,
		LastModified: o.LastModified,
	}
}

// userMeta scans both UserMetadata (populated by StatObject) and Metadata
// (populated by GetObject + Stat()) for a user-set header, case-insensitively.
// minio-go normalizes keys differently between the two paths, so we treat both
// as opaque maps and match any key whose suffix is either the literal user
// key or "x-amz-meta-<user key>".
func userMeta(o minio.ObjectInfo, key string) string {
	want := strings.ToLower(key)
	wantPrefixed := "x-amz-meta-" + want
	if v := scanLowerKey(o.UserMetadata, want, wantPrefixed); v != "" {
		return v
	}
	for k, vs := range o.Metadata {
		lk := strings.ToLower(k)
		if (lk == want || lk == wantPrefixed) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

func scanLowerKey(m map[string]string, want, wantPrefixed string) string {
	for k, v := range m {
		lk := strings.ToLower(k)
		if (lk == want || lk == wantPrefixed) && v != "" {
			return v
		}
	}
	return ""
}
