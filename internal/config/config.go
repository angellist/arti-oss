// Package config owns arti-server's runtime configuration. It is the only
// package allowed to read the process environment or configuration files.
//
// Configuration resolves in precedence order:
//
//  1. Safe defaults (Defaults).
//  2. An optional YAML file named by ARTI_CONFIG_FILE — structured,
//     non-secret settings only. Secret fields are invisible to the YAML
//     decoder (yaml:"-"), so a secret in the file fails as an unknown key.
//  3. Environment-variable overrides (the same names ServeCmd has always
//     used, so existing deployments keep working unchanged).
//
// Downstream packages receive the section they need, never the environment.
package config

import (
	"fmt"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Config is the typed root configuration for arti-server.
type Config struct {
	Server        Server        `yaml:"server"`
	Database      Database      `yaml:"database"`
	Storage       Storage       `yaml:"storage"`
	Auth          Auth          `yaml:"auth"`
	Admin         Admin         `yaml:"admin"`
	Search        Search        `yaml:"search"`
	Apps          Apps          `yaml:"apps"`
	Embedding     Embedding     `yaml:"embedding"`
	Notifications Notifications `yaml:"notifications"`
	LLM           LLM           `yaml:"llm"`
}

// Server holds listener addressing and canonical URLs.
type Server struct {
	Addr         string `yaml:"addr"`
	BaseURL      string `yaml:"base_url"`
	WebURL       string `yaml:"web_url"`
	CookieSecure bool   `yaml:"cookie_secure"`
}

// Database holds the Postgres connection. The DSN embeds credentials, so it
// is environment-only.
type Database struct {
	URL string `yaml:"-"` // ARTI_DATABASE_URL, required
}

// Storage configures the S3-compatible blob store.
type Storage struct {
	Endpoint  string `yaml:"endpoint"` // empty = real AWS S3
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"` // required
	AccessKey string `yaml:"-"`      // AWS_ACCESS_KEY_ID; empty = ambient credential chain
	SecretKey string `yaml:"-"`      // AWS_SECRET_ACCESS_KEY
	UseSSL    bool   `yaml:"use_ssl"`
}

// Auth configures every authentication surface: interactive login, OIDC
// verification, session signing, device grants, API keys, and OBO brokering.
type Auth struct {
	// Mode selects how humans sign in:
	//
	//	"oidc"     built-in OpenID Connect login: arti runs the
	//	           authorization-code flow itself (requires issuer_url +
	//	           client_id, and normally a client secret).
	//	"proxy"    a trusted reverse proxy (oauth2-proxy, Cloudflare Access,
	//	           …) terminates authentication and injects identity
	//	           headers; arti must not be reachable except through it.
	//	"disabled" local development only; equivalent to ARTI_AUTH_DISABLED.
	//	""         legacy default: behaves like "proxy".
	Mode string `yaml:"mode"`

	IssuerURL string `yaml:"issuer_url"` // empty → OIDC disabled
	Audience  string `yaml:"audience"`
	// ClientID / ClientSecret are the OAuth client arti is registered as at
	// the issuer, used by the built-in "oidc" login mode.
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"-"` // AUTH_OIDC_CLIENT_SECRET
	// Scopes requested during built-in login.
	Scopes []string `yaml:"scopes"`
	// GroupsClaim is the ID-token claim carrying group membership; providers
	// disagree on its name and some need it enabled explicitly.
	GroupsClaim    string   `yaml:"groups_claim"`
	RequiredGroups []string `yaml:"required_groups"`
	AllowedDomains []string `yaml:"allowed_domains"`
	ServiceSecret  string   `yaml:"-"` // ARTI_SERVICE_SECRET
	// ServiceEmail is the synthetic identity attributed to service-secret
	// callers.
	ServiceEmail string `yaml:"service_email"`
	SigningKey   string `yaml:"-"` // JWT_SIGNING_KEY
	TestMode     bool   `yaml:"test_mode"`
	Disabled     bool   `yaml:"disabled"`
	LocalEmail   string `yaml:"local_email"`
	// LogoutURL is where /auth/logout redirects after clearing the session
	// (e.g. an SSO proxy's sign-out endpoint). Empty → redirect to the app
	// base URL.
	LogoutURL        string  `yaml:"logout_url"`
	OAuthRegisterRPM int     `yaml:"oauth_register_rpm"`
	Device           Device  `yaml:"device"`
	APIKeys          APIKeys `yaml:"api_keys"`
	OBO              OBO     `yaml:"obo"`
}

// Duration is a time.Duration that round-trips through YAML in Go duration
// syntax ("24h"), so a dumped effective configuration is loadable again
// (plain time.Duration would marshal as bare nanoseconds, which the loader
// rejects as ambiguous).
type Duration time.Duration

// MarshalYAML emits the Go duration string form.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// UnmarshalYAML accepts Go duration strings and rejects bare integers.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q (want Go syntax like 24h)", node.Value)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the standard-library form.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Device configures the RFC 8628 device authorization grant.
type Device struct {
	TokenTTL       Duration `yaml:"token_ttl"`
	TokenMaxTTL    Duration `yaml:"token_max_ttl"`
	MaxUploadBytes int      `yaml:"max_upload_bytes"`
	CodeRPM        int      `yaml:"code_rpm"`
}

// APIKeys configures self-serve API keys.
type APIKeys struct {
	MaxTTL  Duration `yaml:"max_ttl"`
	MintRPM int      `yaml:"mint_rpm"`
}

// OBO configures the on-behalf-of OAuth broker.
type OBO struct {
	CallbackBase string `yaml:"callback_base"` // empty → Server.BaseURL
	EncKey       string `yaml:"-"`             // ARTI_OBO_ENC_KEY; empty → derive from SigningKey
}

// Admin holds the administrator bootstrap allowlist.
type Admin struct {
	Emails []string `yaml:"emails"`
}

// Search configures OpenSearch. Empty endpoint disables it.
type Search struct {
	Endpoint string `yaml:"endpoint"`
	Username string `yaml:"username"`
	Password string `yaml:"-"` // OPENSEARCH_PASSWORD
	Region   string `yaml:"region"`
	// ReindexBatchSize is the page size for `arti-server reindex`.
	ReindexBatchSize int `yaml:"reindex_batch_size"`
}

// Apps configures APP-artifact serving.
type Apps struct {
	// MCPServersJSON is the raw ARTI_APP_MCP_SERVERS JSON map, merged over the
	// built-in catalog by the apps package. Environment-only until the catalog
	// itself moves into configuration data.
	MCPServersJSON string `yaml:"-"`
	FrameAncestors string `yaml:"frame_ancestors"`
}

// Embedding configures embed surfaces. The JSON carries per-surface shared
// secrets, so it is environment-only.
type Embedding struct {
	SurfacesJSON string `yaml:"-"` // ARTI_EMBED_SURFACES
}

// Notifications configures outbound notifications.
type Notifications struct {
	SlackBotToken string `yaml:"-"` // ARTI_SLACK_BOT_TOKEN
}

// LLM configures the built-in llm.complete service.
type LLM struct {
	APIKey        string   `yaml:"-"` // ANTHROPIC_API_KEY; empty → llm disabled
	DefaultModel  string   `yaml:"default_model"`
	AllowedModels []string `yaml:"allowed_models"`
	RPMPerViewer  int      `yaml:"rpm_per_viewer"`
	TPHPerViewer  int64    `yaml:"tph_per_viewer"`
	TPHPerApp     int64    `yaml:"tph_per_app"`
	TPDPerOrg     int64    `yaml:"tpd_per_org"`
}

// LookupFn abstracts the process environment so Load is testable.
type LookupFn func(key string) (string, bool)
