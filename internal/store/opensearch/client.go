// Package opensearch wraps an OpenSearch HTTP client for arti's artifact
// search index. It handles document indexing, deletion, and full-text /
// vector search queries. When the endpoint is empty the client is nil and
// all callers should fall back to Postgres.
package opensearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// Client talks to an OpenSearch cluster over HTTPS. Requests authenticate
// with AWS SigV4 (the pod's IAM role, mapped to all_access on the domain) by
// default; basic auth is used only when a username is configured (local dev /
// admin). A nil *Client is safe — every exported method no-ops / returns a
// zero result so callers don't need nil checks on every call.
type Client struct {
	endpoint string // https://vpc-arti-xxx.us-west-2.es.amazonaws.com
	username string
	password string
	// SigV4 (set when no basic-auth username is configured and a region is
	// available). creds is the resolved AWS credential provider; nil signer
	// means "send unsigned" (local OpenSearch with the security plugin off).
	signer *v4.Signer
	creds  aws.CredentialsProvider
	region string
	http   *http.Client
	logger *slog.Logger
}

// Config holds the env-driven settings for the OpenSearch integration.
type Config struct {
	Endpoint string // OPENSEARCH_ENDPOINT; empty = disabled
	Username string // OPENSEARCH_USERNAME; set = basic auth (local/admin)
	Password string // OPENSEARCH_PASSWORD
	Region   string // OPENSEARCH_REGION; enables SigV4 when Username is empty
}

// New returns a ready-to-use client, or nil when cfg.Endpoint is empty
// (feature disabled).
func New(cfg Config, logger *slog.Logger) *Client {
	if cfg.Endpoint == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{
		endpoint: cfg.Endpoint,
		username: cfg.Username,
		password: cfg.Password,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
	// No basic-auth username → authenticate with SigV4 using the pod's IAM
	// role (the domain maps it to all_access), so no master credentials need
	// to live in a k8s secret. Local dev runs the security plugin disabled and
	// sets no region: if AWS config can't load, fall through unsigned.
	if cfg.Username == "" && cfg.Region != "" {
		awscfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.Region))
		if err != nil {
			logger.Warn("opensearch: could not load AWS config for SigV4; requests will be unsigned", "err", err)
		} else {
			c.signer = v4.NewSigner()
			c.creds = awscfg.Credentials
			c.region = cfg.Region
		}
	}
	return c
}

// Enabled reports whether the client is wired to a live cluster.
func (c *Client) Enabled() bool { return c != nil }

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("opensearch: marshal body: %w", err)
		}
		bodyBytes = b
	}
	url := c.endpoint + path
	var r io.Reader
	if bodyBytes != nil {
		r = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, fmt.Errorf("opensearch: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := c.authRequest(ctx, req, bodyBytes); err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// authRequest applies the configured authentication to req: basic auth when a
// username is set (local/admin), otherwise SigV4 when signing is enabled, else
// nothing (local OpenSearch with the security plugin off). Shared by do() and
// BulkIndex so every request — not just the JSON ones through do() — is signed
// in production. body is the request payload (for the SigV4 payload hash).
func (c *Client) authRequest(ctx context.Context, req *http.Request, body []byte) error {
	switch {
	case c.username != "":
		req.SetBasicAuth(c.username, c.password)
	case c.signer != nil:
		return c.signRequest(ctx, req, body)
	}
	return nil
}

// signRequest applies an AWS SigV4 signature for the "es" (OpenSearch) service
// using the pod's IAM credentials. The payload hash is the SHA-256 of the body
// (sha256 of the empty string for body-less GETs), which is what SignHTTP
// expects.
func (c *Client) signRequest(ctx context.Context, req *http.Request, body []byte) error {
	creds, err := c.creds.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("opensearch: retrieve AWS credentials for SigV4: %w", err)
	}
	sum := sha256.Sum256(body)
	if err := c.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "es", c.region, time.Now()); err != nil {
		return fmt.Errorf("opensearch: SigV4 sign: %w", err)
	}
	return nil
}

// readBody drains and closes resp.Body, returning the bytes. If the
// status is >= 400 it returns an error with the response body.
func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("opensearch: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return b, fmt.Errorf("opensearch: HTTP %d: %s", resp.StatusCode, truncate(string(b), 512))
	}
	return b, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
