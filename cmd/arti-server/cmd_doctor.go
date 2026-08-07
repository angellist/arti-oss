package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/jackc/pgx/v5/pgxpool"
	yaml "go.yaml.in/yaml/v3"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/config"
	"github.com/angellist/arti-oss/internal/store/blob"
)

// DoctorCmd verifies a configuration end to end: parse and validate, then
// probe each dependency (database, object store, issuer) with actionable,
// key-naming errors. Built for iteration by a human or their AI assistant
// during setup — run it after every configuration change.
type DoctorCmd struct {
	ConfigOnly  bool `name:"config-only" help:"validate configuration without any network checks"`
	PrintConfig bool `name:"print-effective-config" help:"print the fully resolved configuration (secrets omitted) and exit"`
}

// doctorCheck is one named verification. fn returns a human detail string on
// success; an error marks the check failed. skip marks a check not
// applicable under the current configuration.
type doctorCheck struct {
	name string
	fn   func(ctx context.Context, cfg *config.Config) (detail string, skip bool, err error)
}

func (c *DoctorCmd) Run(_ *kong.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("arti-server doctor")
	cfg, err := config.Load(config.RequireDatabase, config.RequireStorage)
	if err != nil {
		fmt.Printf("  ✗ configuration: %v\n", err)
		return fmt.Errorf("doctor: configuration failed to load")
	}
	fmt.Println("  ✓ configuration: parsed and required fields present")

	if c.PrintConfig {
		// Secret fields are yaml:"-" by construction, so marshalling the
		// resolved config omits them entirely.
		out, merr := yaml.Marshal(cfg)
		if merr != nil {
			return merr
		}
		fmt.Println("# effective configuration (secret fields omitted)")
		fmt.Print(string(out))
		return nil
	}

	checks := []doctorCheck{
		{"signing key", checkSigningKey},
		{"base URL & cookies", checkBaseURL},
		{"access gate", checkAccessGate},
	}
	if !c.ConfigOnly {
		checks = append(checks,
			doctorCheck{"database", checkDatabase},
			doctorCheck{"object store", checkStorage},
			doctorCheck{"identity provider", checkIssuer},
		)
	}

	failed := 0
	for _, ch := range checks {
		detail, skip, cerr := ch.fn(ctx, cfg)
		switch {
		case skip:
			fmt.Printf("  - %s: %s\n", ch.name, detail)
		case cerr != nil:
			failed++
			fmt.Printf("  ✗ %s: %v\n", ch.name, cerr)
		default:
			fmt.Printf("  ✓ %s: %s\n", ch.name, detail)
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	fmt.Println("all checks passed")
	return nil
}

func checkSigningKey(_ context.Context, cfg *config.Config) (string, bool, error) {
	if err := auth.ValidateSigningKey(cfg.Auth.SigningKey, cfg.Auth.Disabled); err != nil {
		return "", false, fmt.Errorf("%w — set JWT_SIGNING_KEY to a strong (≥32 byte) secret", err)
	}
	if cfg.Auth.Disabled {
		return "auth disabled (local dev) — key not enforced", false, nil
	}
	return "strong", false, nil
}

func checkBaseURL(_ context.Context, cfg *config.Config) (string, bool, error) {
	u, err := url.Parse(cfg.Server.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false, fmt.Errorf("ARTI_BASE_URL (server.base_url) %q is not an absolute http(s) URL", cfg.Server.BaseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false, fmt.Errorf("ARTI_BASE_URL (server.base_url) scheme %q — want http or https", u.Scheme)
	}
	if u.Scheme == "https" && !cfg.Server.CookieSecure {
		return "", false, fmt.Errorf("base URL is https but ARTI_COOKIE_SECURE (server.cookie_secure) is false — session cookies would be sent without the Secure flag")
	}
	if u.Scheme == "http" && cfg.Server.CookieSecure && !isLocalHost(u.Hostname()) {
		return "", false, fmt.Errorf("ARTI_COOKIE_SECURE is true but the base URL is plain http — browsers will drop the session cookie")
	}
	return cfg.Server.BaseURL, false, nil
}

// checkAccessGate spells out who the email allowlist actually admits, which
// is not always what the operator reads into it. Deliberately judges no
// domain: whether `example.com` means "my colleagues" or "the internet"
// depends on who can get an account there, which only the operator knows —
// so the check states the implication for every domain entry rather than
// pattern-matching a doomed list of public mail providers. Empty is the one
// configuration it calls out, because fail-closed means nobody at all.
func checkAccessGate(_ context.Context, cfg *config.Config) (string, bool, error) {
	if cfg.Auth.Disabled {
		return "auth disabled — everyone is local@example.com; not for shared or public deployments", true, nil
	}
	var domains, addresses []string
	for _, e := range auth.NormalizeAllowlist(cfg.Auth.AllowedEmails) {
		if strings.Contains(e, "@") {
			addresses = append(addresses, e)
			continue
		}
		domains = append(domains, e)
	}
	if len(domains)+len(addresses) == 0 {
		return "AUTH_ALLOWED_EMAILS (auth.allowed_emails) is empty — fail-closed, so no interactive login can succeed; list the domains and/or full email addresses allowed to sign in", true, nil
	}
	var parts []string
	if len(addresses) > 0 {
		parts = append(parts, fmt.Sprintf("%d address(es) — exactly %s", len(addresses), strings.Join(addresses, ", ")))
	}
	if len(domains) > 0 {
		parts = append(parts, fmt.Sprintf("%d domain(s) — EVERY account your issuer will authenticate at %s (if that is a shared mail provider rather than a domain you control, list full addresses instead)",
			len(domains), strings.Join(domains, ", ")))
	}
	return strings.Join(parts, "; "), false, nil
}

func checkDatabase(ctx context.Context, cfg *config.Config) (string, bool, error) {
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return "", false, fmt.Errorf("ARTI_DATABASE_URL: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return "", false, fmt.Errorf("ARTI_DATABASE_URL: cannot reach Postgres: %w", err)
	}
	// goose's version table is an append-only log with is_applied toggles —
	// after a down/reset, old version_ids remain. Take the latest state per
	// version and count only the ones still applied.
	var version int64
	err = pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(version_id), 0) FROM (
			SELECT DISTINCT ON (version_id) version_id, is_applied
			FROM goose_db_version ORDER BY version_id, id DESC
		) latest WHERE is_applied`).Scan(&version)
	switch {
	case err != nil && strings.Contains(err.Error(), "42P01"): // undefined_table
		return "", false, fmt.Errorf("connected, but migrations have never run — run `arti-server migrate`")
	case err != nil:
		return "", false, fmt.Errorf("connected, but reading migration state failed: %w", err)
	case version == 0:
		return "", false, fmt.Errorf("connected, but no migrations applied — run `arti-server migrate`")
	}
	return fmt.Sprintf("connected; migrations at version %d", version), false, nil
}

func checkStorage(ctx context.Context, cfg *config.Config) (string, bool, error) {
	s3, err := blob.NewS3(blob.S3Config{
		Endpoint:  trimScheme(cfg.Storage.Endpoint),
		AccessKey: cfg.Storage.AccessKey,
		SecretKey: cfg.Storage.SecretKey,
		Bucket:    cfg.Storage.Bucket,
		Region:    cfg.Storage.Region,
		UseSSL:    cfg.Storage.UseSSL,
	})
	if err != nil {
		return "", false, fmt.Errorf("S3_* (storage.*): %w", err)
	}
	// Probe object: prove reach, bucket existence, and read/write/delete
	// permission in one pass.
	key := fmt.Sprintf("arti-doctor-probe/%d", time.Now().UnixNano())
	if _, err := s3.Put(ctx, key, strings.NewReader("probe"), blob.PutOpts{ContentType: "text/plain"}); err != nil {
		hint := ""
		if cfg.Storage.Endpoint == "" {
			hint = " (endpoint empty = real AWS S3; is the bucket created and the credential chain valid?)"
		} else {
			hint = fmt.Sprintf(" (endpoint %s, use_ssl=%v — create the bucket, e.g. `mc mb local/%s` on MinIO)", cfg.Storage.Endpoint, cfg.Storage.UseSSL, cfg.Storage.Bucket)
		}
		return "", false, fmt.Errorf("S3_BUCKET %q: write failed: %w%s", cfg.Storage.Bucket, err, hint)
	}
	// Best-effort cleanup even when a later step fails, so repeated doctor
	// runs never accumulate probe objects. Uses its own short context: the
	// shared one may already be expired when the defer runs.
	deleted := false
	defer func() {
		if !deleted {
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer ccancel()
			_ = s3.Delete(cctx, key)
		}
	}()
	rc, _, err := s3.Get(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("S3_BUCKET %q: read-back failed: %w", cfg.Storage.Bucket, err)
	}
	_ = rc.Close()
	// Own short context, like the deferred cleanup: a near-deadline shared
	// context would fail this delete and misreport it as a permission issue.
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	if err := s3.Delete(dctx, key); err != nil {
		return "", false, fmt.Errorf("S3_BUCKET %q: delete failed (need s3:DeleteObject): %w", cfg.Storage.Bucket, err)
	}
	deleted = true
	where := "AWS S3"
	if cfg.Storage.Endpoint != "" {
		where = cfg.Storage.Endpoint
	}
	return fmt.Sprintf("bucket %q on %s: write/read/delete ok", cfg.Storage.Bucket, where), false, nil
}

func checkIssuer(ctx context.Context, cfg *config.Config) (string, bool, error) {
	if cfg.Auth.Disabled {
		return "auth disabled — skipped", true, nil
	}
	if cfg.Auth.IssuerURL == "" {
		return "no issuer configured (AUTH_DEX_ISSUER_URL / auth.issuer_url) — interactive OIDC login unavailable", true, nil
	}
	v, err := auth.NewOIDCVerifier(ctx, auth.OIDCConfig{
		IssuerURL: cfg.Auth.IssuerURL,
		Audience:  cfg.Auth.Audience,
	})
	if err != nil || v == nil {
		return "", false, fmt.Errorf("AUTH_DEX_ISSUER_URL %q: discovery failed: %w (is the issuer URL exact, including any path?)", cfg.Auth.IssuerURL, err)
	}
	// go-oidc loads JWKS lazily on first verify — fetch it here so a broken
	// jwks_uri fails the check instead of the first real login.
	if err := fetchJWKS(ctx, cfg.Auth.IssuerURL); err != nil {
		return "", false, fmt.Errorf("AUTH_DEX_ISSUER_URL %q: %w", cfg.Auth.IssuerURL, err)
	}
	detail := "discovery + JWKS ok"
	if cfg.Auth.Mode == "oidc" && cfg.Auth.ClientSecret == "" {
		detail += "; note: AUTH_OIDC_CLIENT_SECRET is empty (public client — most issuers require a secret)"
	}
	detail += issuerAudienceNote(cfg)
	return detail, false, nil
}

// issuerAudienceNote returns the trailing note for an oidc deployment whose
// expected `aud` is not its client ID — nearly always a leftover from the
// Dex-shaped default. Scoped deliberately: only raw ID-token bearer auth
// consults the audience, so the note must not read as "login is broken".
func issuerAudienceNote(cfg *config.Config) string {
	if cfg.Auth.Mode != "oidc" || cfg.Auth.ClientID == "" || cfg.Auth.Audience == cfg.Auth.ClientID {
		return ""
	}
	return fmt.Sprintf("; note: AUTH_JWT_AUDIENCE %q differs from AUTH_OIDC_CLIENT_ID %q — standard issuers set `aud` to the client ID, so raw ID-token bearer auth will reject every token (interactive login is unaffected; leave AUTH_JWT_AUDIENCE unset to default it to the client ID)",
		cfg.Auth.Audience, cfg.Auth.ClientID)
}

// fetchJWKS resolves jwks_uri from the issuer's discovery document and
// fetches it, verifying it parses as a key set with at least one key.
func fetchJWKS(ctx context.Context, issuer string) error {
	discoURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	var disco struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := getJSON(ctx, discoURL, &disco); err != nil {
		return fmt.Errorf("discovery document: %w", err)
	}
	if disco.JWKSURI == "" {
		return fmt.Errorf("discovery document has no jwks_uri")
	}
	var jwks struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := getJSON(ctx, disco.JWKSURI, &jwks); err != nil {
		return fmt.Errorf("jwks_uri %s: %w", disco.JWKSURI, err)
	}
	if len(jwks.Keys) == 0 {
		return fmt.Errorf("jwks_uri %s: key set is empty", disco.JWKSURI)
	}
	return nil
}

func getJSON(ctx context.Context, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst)
}

func isLocalHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
