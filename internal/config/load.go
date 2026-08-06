package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// legacyDexAudience is the `aud` claim Dex issues behind oauth2-proxy, and
// the fallback for every non-oidc mode so existing proxy deployments that
// never set AUTH_JWT_AUDIENCE keep working unchanged.
const legacyDexAudience = "auth"

// Defaults returns the baseline configuration — the same values the legacy
// ServeCmd env-tag defaults carried.
func Defaults() *Config {
	return &Config{
		Server: Server{
			Addr:    ":8090",
			BaseURL: "http://localhost:8090",
		},
		Storage: Storage{
			Region: "us-east-1",
			// TLS to the object store defaults ON; plaintext is an explicit
			// opt-out intended for local MinIO (S3_USE_SSL=false).
			UseSSL: true,
		},
		Auth: Auth{
			// Resolved in LoadFrom once the mode is known: standard issuers
			// put the client ID in `aud`, a Dex-fronted proxy uses "auth".
			Audience:        "",
			Scopes:          []string{"openid", "email", "profile"},
			GroupsClaim:     "groups",
			IdPGroupsMaxAge: Duration(14 * 24 * time.Hour), // 336h
			// No allowed domains by default: with auth enabled, an empty
			// allowlist denies everyone (fail closed). Deployments must
			// configure their own domains.
			AllowedDomains:   nil,
			ServiceEmail:     "service@arti.invalid",
			SigningKey:       "dev-key-not-for-prod", // kept in sync with auth.DevDefaultSigningKey
			LocalEmail:       "local@example.com",
			OAuthRegisterRPM: 10,
			Device: Device{
				TokenTTL:       Duration(24 * time.Hour),
				TokenMaxTTL:    Duration(720 * time.Hour),
				MaxUploadBytes: 26214400, // 25 MiB
				CodeRPM:        10,
			},
			APIKeys: APIKeys{
				MaxTTL:  Duration(8760 * time.Hour), // 365d
				MintRPM: 10,
			},
		},
		// No default administrators: admin capability is granted only by
		// explicit configuration (or existing DB role assignments).
		Admin: Admin{},
		Search: Search{
			ReindexBatchSize: 100,
		},
		Apps: Apps{
			// Same-origin only; deployments allowlist trusted embedding
			// origins explicitly.
			FrameAncestors: "'self'",
		},
		LLM: LLM{
			DefaultModel:  "claude-sonnet-4-6",
			AllowedModels: []string{"claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"},
			RPMPerViewer:  30,
			TPHPerViewer:  300000,
			TPHPerApp:     1000000,
		},
	}
}

// Requirement names a configuration precondition a command needs; Load
// validates the ones it is given, so e.g. `migrate` can run without a
// blob-store bucket configured.
type Requirement int

const (
	// RequireDatabase demands a Postgres DSN (ARTI_DATABASE_URL).
	RequireDatabase Requirement = iota
	// RequireStorage demands a blob-store bucket (S3_BUCKET / storage.bucket).
	RequireStorage
	// RequireSearch demands an OpenSearch endpoint (OPENSEARCH_ENDPOINT /
	// search.endpoint).
	RequireSearch
)

// Load resolves the configuration from the real process environment and
// validates the given requirements.
func Load(reqs ...Requirement) (*Config, error) {
	return LoadFrom(os.LookupEnv, reqs...)
}

// LoadFrom resolves the configuration using the given environment lookup:
// defaults, then the optional ARTI_CONFIG_FILE YAML, then env overrides,
// then validation of the given requirements.
func LoadFrom(lookup LookupFn, reqs ...Requirement) (*Config, error) {
	cfg := Defaults()

	if path, ok := lookup("ARTI_CONFIG_FILE"); ok && path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("ARTI_CONFIG_FILE: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("ARTI_CONFIG_FILE %s: %w", path, err)
		}
	}

	for _, b := range bindings(cfg) {
		v, ok := lookup(b.name)
		if !ok {
			continue
		}
		if err := b.set(v); err != nil {
			return nil, fmt.Errorf("%s: %w", b.name, err)
		}
	}

	// Endpoint typos are the most common self-hosting storage failure:
	// normalize an accidental scheme prefix and reject anything that is not
	// host[:port].
	if ep := cfg.Storage.Endpoint; ep != "" {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://")
		if trimmed == "" || strings.ContainsAny(trimmed, "/ ") {
			return nil, fmt.Errorf("S3_ENDPOINT (storage.endpoint) %q: want host[:port] with no path — e.g. localhost:9210 or s3.us-west-2.amazonaws.com", ep)
		}
		cfg.Storage.Endpoint = trimmed
	}

	switch cfg.Auth.Mode {
	case "", "proxy":
		// Legacy default: a trusted proxy (or nothing, for bearer-only use).
	case "disabled":
		cfg.Auth.Disabled = true
	case "oidc":
		if cfg.Auth.IssuerURL == "" || cfg.Auth.ClientID == "" {
			return nil, fmt.Errorf("auth.mode oidc requires AUTH_DEX_ISSUER_URL (auth.issuer_url) and AUTH_OIDC_CLIENT_ID (auth.client_id)")
		}
	default:
		return nil, fmt.Errorf("ARTI_AUTH_MODE: unknown mode %q (want oidc, proxy, or disabled)", cfg.Auth.Mode)
	}

	// AUTH_JWT_AUDIENCE is the `aud` claim demanded of raw ID-token bearer
	// auth. Left unset it resolves from the mode rather than to a constant:
	// every standard issuer (Google, Okta, Entra, Auth0…) sets `aud` to the
	// client ID, so hard-defaulting to Dex's "auth" rejected every such
	// token. An explicit value always wins.
	if cfg.Auth.Audience == "" {
		if cfg.Auth.Mode == "oidc" {
			cfg.Auth.Audience = cfg.Auth.ClientID
		} else {
			cfg.Auth.Audience = legacyDexAudience
		}
	}

	var missing []string
	for _, r := range reqs {
		switch r {
		case RequireDatabase:
			if cfg.Database.URL == "" {
				missing = append(missing, "ARTI_DATABASE_URL")
			}
		case RequireStorage:
			if cfg.Storage.Bucket == "" {
				missing = append(missing, "S3_BUCKET (or storage.bucket in ARTI_CONFIG_FILE)")
			}
		case RequireSearch:
			if cfg.Search.Endpoint == "" {
				missing = append(missing, "OPENSEARCH_ENDPOINT (or search.endpoint in ARTI_CONFIG_FILE)")
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

// binding maps one environment variable onto a config field.
type binding struct {
	name string
	set  func(v string) error
}

func bindings(c *Config) []binding {
	return []binding{
		// Server
		str("ARTI_ADDR", &c.Server.Addr),
		str("ARTI_BASE_URL", &c.Server.BaseURL),
		str("ARTI_WEB_URL", &c.Server.WebURL),
		boolean("ARTI_COOKIE_SECURE", &c.Server.CookieSecure),

		// Database
		str("ARTI_DATABASE_URL", &c.Database.URL),

		// Storage
		str("S3_ENDPOINT", &c.Storage.Endpoint),
		str("S3_REGION", &c.Storage.Region),
		str("S3_BUCKET", &c.Storage.Bucket),
		str("AWS_ACCESS_KEY_ID", &c.Storage.AccessKey),
		str("AWS_SECRET_ACCESS_KEY", &c.Storage.SecretKey),
		boolean("S3_USE_SSL", &c.Storage.UseSSL),

		// Auth
		str("ARTI_AUTH_MODE", &c.Auth.Mode),
		str("AUTH_DEX_ISSUER_URL", &c.Auth.IssuerURL),
		str("AUTH_JWT_AUDIENCE", &c.Auth.Audience),
		str("AUTH_OIDC_CLIENT_ID", &c.Auth.ClientID),
		str("AUTH_OIDC_CLIENT_SECRET", &c.Auth.ClientSecret),
		csv("AUTH_OIDC_SCOPES", &c.Auth.Scopes),
		str("AUTH_OIDC_GROUPS_CLAIM", &c.Auth.GroupsClaim),
		csv("AUTH_REQUIRED_GROUPS", &c.Auth.RequiredGroups),
		csv("AUTH_ALLOWED_DOMAINS", &c.Auth.AllowedDomains),
		str("ARTI_SERVICE_SECRET", &c.Auth.ServiceSecret),
		str("ARTI_SERVICE_EMAIL", &c.Auth.ServiceEmail),
		str("ARTI_LOGOUT_URL", &c.Auth.LogoutURL),
		str("JWT_SIGNING_KEY", &c.Auth.SigningKey),
		boolean("ARTI_TEST_MODE", &c.Auth.TestMode),
		boolean("ARTI_AUTH_DISABLED", &c.Auth.Disabled),
		str("ARTI_LOCAL_EMAIL", &c.Auth.LocalEmail),
		integer("ARTI_OAUTH_REGISTER_RPM", &c.Auth.OAuthRegisterRPM),
		duration("ARTI_DEVICE_TOKEN_TTL", &c.Auth.Device.TokenTTL),
		duration("ARTI_DEVICE_TOKEN_MAX_TTL", &c.Auth.Device.TokenMaxTTL),
		integer("ARTI_DEVICE_MAX_UPLOAD_BYTES", &c.Auth.Device.MaxUploadBytes),
		integer("ARTI_DEVICE_CODE_RPM", &c.Auth.Device.CodeRPM),
		duration("ARTI_IDP_GROUPS_MAX_AGE", &c.Auth.IdPGroupsMaxAge),
		duration("ARTI_API_KEY_MAX_TTL", &c.Auth.APIKeys.MaxTTL),
		integer("ARTI_API_KEY_RPM", &c.Auth.APIKeys.MintRPM),
		str("ARTI_OBO_CALLBACK_BASE", &c.Auth.OBO.CallbackBase),
		str("ARTI_OBO_ENC_KEY", &c.Auth.OBO.EncKey),

		// Admin
		csv("ARTI_ADMIN_EMAILS", &c.Admin.Emails),

		// Search
		str("OPENSEARCH_ENDPOINT", &c.Search.Endpoint),
		str("OPENSEARCH_USERNAME", &c.Search.Username),
		str("OPENSEARCH_PASSWORD", &c.Search.Password),
		str("OPENSEARCH_REGION", &c.Search.Region),
		integer("REINDEX_BATCH_SIZE", &c.Search.ReindexBatchSize),

		// Apps
		str("ARTI_APP_MCP_SERVERS", &c.Apps.MCPServersJSON),
		str("ARTI_APP_FRAME_ANCESTORS", &c.Apps.FrameAncestors),

		// Embedding
		str("ARTI_EMBED_SURFACES", &c.Embedding.SurfacesJSON),

		// Notifications
		str("ARTI_SLACK_BOT_TOKEN", &c.Notifications.SlackBotToken),

		// LLM
		str("ANTHROPIC_API_KEY", &c.LLM.APIKey),
		str("ARTI_LLM_DEFAULT_MODEL", &c.LLM.DefaultModel),
		csv("ARTI_LLM_ALLOWED_MODELS", &c.LLM.AllowedModels),
		integer("ARTI_LLM_RPM_PER_VIEWER", &c.LLM.RPMPerViewer),
		int64Var("ARTI_LLM_TPH_PER_VIEWER", &c.LLM.TPHPerViewer),
		int64Var("ARTI_LLM_TPH_PER_APP", &c.LLM.TPHPerApp),
		int64Var("ARTI_LLM_TPD_PER_ORG", &c.LLM.TPDPerOrg),
	}
}

func str(name string, dst *string) binding {
	return binding{name, func(v string) error { *dst = v; return nil }}
}

func boolean(name string, dst *bool) binding {
	return binding{name, func(v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid bool %q", v)
		}
		*dst = b
		return nil
	}}
}

func integer(name string, dst *int) binding {
	return binding{name, func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid integer %q", v)
		}
		*dst = n
		return nil
	}}
}

func int64Var(name string, dst *int64) binding {
	return binding{name, func(v string) error {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer %q", v)
		}
		*dst = n
		return nil
	}}
}

func duration(name string, dst *Duration) binding {
	return binding{name, func(v string) error {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid duration %q", v)
		}
		*dst = Duration(d)
		return nil
	}}
}

// csv splits a comma-separated value, trimming whitespace and dropping empty
// elements. A set-but-empty variable means "none", overriding any default.
func csv(name string, dst *[]string) binding {
	return binding{name, func(v string) error {
		*dst = SplitCSV(v)
		return nil
	}}
}

// SplitCSV splits a comma-separated string into trimmed, non-empty elements;
// it returns nil for an empty input.
func SplitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
