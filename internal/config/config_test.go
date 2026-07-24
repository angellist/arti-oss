package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadWith runs LoadFrom against a fake environment. When yamlBody is
// non-empty it is written to a temp file and exposed as ARTI_CONFIG_FILE.
func loadWith(t *testing.T, env map[string]string, yamlBody string) (*Config, error) {
	t.Helper()
	if yamlBody != "" {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(yamlBody), 0o600); err != nil {
			t.Fatal(err)
		}
		env["ARTI_CONFIG_FILE"] = p
	}
	return LoadFrom(func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}, RequireDatabase, RequireStorage)
}

// required returns the minimal env that satisfies the required fields, plus
// any extra entries.
func required(extra map[string]string) map[string]string {
	env := map[string]string{
		"ARTI_DATABASE_URL": "postgres://u:p@localhost:5432/arti",
		"S3_BUCKET":         "arti-test-bucket",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestGenericDefaults(t *testing.T) {
	cfg, err := loadWith(t, required(nil), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Server.Addr", cfg.Server.Addr, ":8090"},
		{"Server.BaseURL", cfg.Server.BaseURL, "http://localhost:8090"},
		{"Server.WebURL", cfg.Server.WebURL, ""},
		{"Server.CookieSecure", cfg.Server.CookieSecure, false},
		{"Storage.Endpoint", cfg.Storage.Endpoint, ""},
		{"Storage.Region", cfg.Storage.Region, "us-east-1"},
		{"Storage.UseSSL", cfg.Storage.UseSSL, true}, // TLS on by default; local MinIO opts out
		{"Auth.Audience", cfg.Auth.Audience, "auth"},
		{"Auth.SigningKey", cfg.Auth.SigningKey, "dev-key-not-for-prod"},
		{"Auth.TestMode", cfg.Auth.TestMode, false},
		{"Auth.Disabled", cfg.Auth.Disabled, false},
		{"Auth.LocalEmail", cfg.Auth.LocalEmail, "local@example.com"},
		{"Auth.ServiceEmail", cfg.Auth.ServiceEmail, "service@arti.invalid"},
		{"Auth.LogoutURL", cfg.Auth.LogoutURL, ""},
		{"Apps.FrameAncestors", cfg.Apps.FrameAncestors, "'self'"},
		{"Auth.Device.TokenTTL", cfg.Auth.Device.TokenTTL, Duration(24 * time.Hour)},
		{"Auth.Device.TokenMaxTTL", cfg.Auth.Device.TokenMaxTTL, Duration(720 * time.Hour)},
		{"Auth.Device.MaxUploadBytes", cfg.Auth.Device.MaxUploadBytes, 26214400},
		{"Auth.Device.CodeRPM", cfg.Auth.Device.CodeRPM, 10},
		{"Auth.APIKeys.MaxTTL", cfg.Auth.APIKeys.MaxTTL, Duration(8760 * time.Hour)},
		{"Auth.APIKeys.MintRPM", cfg.Auth.APIKeys.MintRPM, 10},
		{"Auth.OAuthRegisterRPM", cfg.Auth.OAuthRegisterRPM, 10},
		{"LLM.DefaultModel", cfg.LLM.DefaultModel, "claude-sonnet-4-6"},
		{"LLM.RPMPerViewer", cfg.LLM.RPMPerViewer, 30},
		{"LLM.TPHPerViewer", cfg.LLM.TPHPerViewer, int64(300000)},
		{"LLM.TPHPerApp", cfg.LLM.TPHPerApp, int64(1000000)},
		{"LLM.TPDPerOrg", cfg.LLM.TPDPerOrg, int64(0)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// Generic, tenant-neutral defaults: nobody is allowed and nobody is an
	// admin until a deployment says so.
	if cfg.Auth.AllowedDomains != nil {
		t.Errorf("Auth.AllowedDomains = %v, want nil (fail closed)", cfg.Auth.AllowedDomains)
	}
	if cfg.Admin.Emails != nil {
		t.Errorf("Admin.Emails = %v, want nil (no default admins)", cfg.Admin.Emails)
	}
	if got := strings.Join(cfg.LLM.AllowedModels, ","); got != "claude-opus-4-8,claude-sonnet-4-6,claude-haiku-4-5" {
		t.Errorf("LLM.AllowedModels = %q", got)
	}
	if cfg.Auth.RequiredGroups != nil {
		t.Errorf("Auth.RequiredGroups = %v, want nil", cfg.Auth.RequiredGroups)
	}
}

func TestRequiredFieldsMissing(t *testing.T) {
	_, err := loadWith(t, map[string]string{}, "")
	if err == nil {
		t.Fatal("want error for missing required fields")
	}
	for _, name := range []string{"ARTI_DATABASE_URL", "S3_BUCKET"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestYAMLOverridesDefaults(t *testing.T) {
	cfg, err := loadWith(t, required(nil), `
server:
  addr: ":9999"
  base_url: https://arti.example.com
storage:
  region: eu-west-1
auth:
  allowed_domains:
    - example.com
  required_groups:
    - engineers
admin:
  emails:
    - admin@example.com
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != ":9999" {
		t.Errorf("Server.Addr = %q", cfg.Server.Addr)
	}
	if cfg.Server.BaseURL != "https://arti.example.com" {
		t.Errorf("Server.BaseURL = %q", cfg.Server.BaseURL)
	}
	if cfg.Storage.Region != "eu-west-1" {
		t.Errorf("Storage.Region = %q", cfg.Storage.Region)
	}
	if got := strings.Join(cfg.Auth.AllowedDomains, ","); got != "example.com" {
		t.Errorf("Auth.AllowedDomains = %q", got)
	}
	if got := strings.Join(cfg.Auth.RequiredGroups, ","); got != "engineers" {
		t.Errorf("Auth.RequiredGroups = %q", got)
	}
	if got := strings.Join(cfg.Admin.Emails, ","); got != "admin@example.com" {
		t.Errorf("Admin.Emails = %q", got)
	}
}

func TestRequirementsArePerCommand(t *testing.T) {
	empty := func(string) (string, bool) { return "", false }
	// No requirements (defaults everywhere) resolves fine.
	if _, err := LoadFrom(empty); err != nil {
		t.Fatalf("LoadFrom with no requirements: %v", err)
	}
	// migrate-shaped: database only — no bucket needed.
	dbOnly := func(key string) (string, bool) {
		if key == "ARTI_DATABASE_URL" {
			return "postgres://u@h/db", true
		}
		return "", false
	}
	if _, err := LoadFrom(dbOnly, RequireDatabase); err != nil {
		t.Fatalf("RequireDatabase with DSN set: %v", err)
	}
	// reindex-shaped: search endpoint missing must be named.
	_, err := LoadFrom(dbOnly, RequireDatabase, RequireSearch)
	if err == nil || !strings.Contains(err.Error(), "OPENSEARCH_ENDPOINT") {
		t.Fatalf("err = %v, want mention of OPENSEARCH_ENDPOINT", err)
	}
}

func TestStorageEndpointValidation(t *testing.T) {
	// Scheme prefixes are normalized away (minio-go wants bare host:port).
	cfg, err := loadWith(t, required(map[string]string{"S3_ENDPOINT": "http://localhost:9210"}), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.Endpoint != "localhost:9210" {
		t.Errorf("Endpoint = %q, want scheme stripped", cfg.Storage.Endpoint)
	}
	// A path is a typo (bucket or console URL pasted in) — reject with the key named.
	_, err = loadWith(t, required(map[string]string{"S3_ENDPOINT": "https://minio.example.com/arti"}), "")
	if err == nil || !strings.Contains(err.Error(), "S3_ENDPOINT") {
		t.Fatalf("err = %v, want S3_ENDPOINT validation error", err)
	}
}

func TestAuthModeValidation(t *testing.T) {
	// Unknown mode fails with the variable named.
	_, err := loadWith(t, required(map[string]string{"ARTI_AUTH_MODE": "saml"}), "")
	if err == nil || !strings.Contains(err.Error(), "ARTI_AUTH_MODE") {
		t.Fatalf("err = %v, want unknown-mode error", err)
	}
	// oidc demands issuer + client id.
	_, err = loadWith(t, required(map[string]string{"ARTI_AUTH_MODE": "oidc"}), "")
	if err == nil || !strings.Contains(err.Error(), "AUTH_OIDC_CLIENT_ID") {
		t.Fatalf("err = %v, want oidc-requires error", err)
	}
	cfg, err := loadWith(t, required(map[string]string{
		"ARTI_AUTH_MODE":          "oidc",
		"AUTH_DEX_ISSUER_URL":     "https://id.example.com",
		"AUTH_OIDC_CLIENT_ID":     "arti",
		"AUTH_OIDC_CLIENT_SECRET": "shh",
	}), "")
	if err != nil {
		t.Fatalf("oidc mode with issuer+client: %v", err)
	}
	if got := strings.Join(cfg.Auth.Scopes, ","); got != "openid,email,profile" {
		t.Errorf("Scopes = %q", got)
	}
	if cfg.Auth.GroupsClaim != "groups" {
		t.Errorf("GroupsClaim = %q", cfg.Auth.GroupsClaim)
	}
	// disabled mode implies Auth.Disabled.
	cfg, err = loadWith(t, required(map[string]string{"ARTI_AUTH_MODE": "disabled"}), "")
	if err != nil {
		t.Fatalf("disabled mode: %v", err)
	}
	if !cfg.Auth.Disabled {
		t.Error("mode=disabled must set Auth.Disabled")
	}
}

func TestYAMLDurationsUseGoSyntax(t *testing.T) {
	cfg, err := loadWith(t, required(nil), `
auth:
  device:
    token_ttl: 36h
  api_keys:
    max_ttl: 48h
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := time.Duration(cfg.Auth.Device.TokenTTL); got != 36*time.Hour {
		t.Errorf("Device.TokenTTL = %v, want 36h", got)
	}
	if got := time.Duration(cfg.Auth.APIKeys.MaxTTL); got != 48*time.Hour {
		t.Errorf("APIKeys.MaxTTL = %v, want 48h", got)
	}
	// A bare integer is ambiguous (nanoseconds? seconds?) — reject it.
	if _, err := loadWith(t, required(nil), "auth:\n  device:\n    token_ttl: 86400\n"); err == nil {
		t.Error("bare integer duration accepted, want error")
	}
	if _, err := loadWith(t, required(nil), "auth:\n  device:\n    token_ttl: soon\n"); err == nil {
		t.Error("bad duration string accepted, want error")
	}
}

func TestEnvOverridesYAML(t *testing.T) {
	cfg, err := loadWith(t, required(map[string]string{
		"S3_REGION":            "us-west-2",
		"AUTH_ALLOWED_DOMAINS": "env.example.com, other.example.com",
	}), `
storage:
  region: eu-west-1
auth:
  allowed_domains:
    - yaml.example.com
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.Region != "us-west-2" {
		t.Errorf("Storage.Region = %q, want env value us-west-2", cfg.Storage.Region)
	}
	if got := strings.Join(cfg.Auth.AllowedDomains, ","); got != "env.example.com,other.example.com" {
		t.Errorf("Auth.AllowedDomains = %q, want split env value", got)
	}
}

func TestEnvTypeParsing(t *testing.T) {
	cfg, err := loadWith(t, required(map[string]string{
		"S3_USE_SSL":              "true",
		"ARTI_DEVICE_TOKEN_TTL":   "1h30m",
		"ARTI_LLM_TPH_PER_VIEWER": "42",
		"ARTI_DEVICE_CODE_RPM":    "0",
		"ARTI_LLM_ALLOWED_MODELS": "m1,m2",
		"AUTH_REQUIRED_GROUPS":    "",
	}), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Storage.UseSSL {
		t.Error("Storage.UseSSL = false, want true")
	}
	if cfg.Auth.Device.TokenTTL.Std() != 90*time.Minute {
		t.Errorf("Device.TokenTTL = %v", cfg.Auth.Device.TokenTTL)
	}
	if cfg.LLM.TPHPerViewer != 42 {
		t.Errorf("LLM.TPHPerViewer = %d", cfg.LLM.TPHPerViewer)
	}
	if cfg.Auth.Device.CodeRPM != 0 {
		t.Errorf("Device.CodeRPM = %d", cfg.Auth.Device.CodeRPM)
	}
	if got := strings.Join(cfg.LLM.AllowedModels, ","); got != "m1,m2" {
		t.Errorf("LLM.AllowedModels = %q", got)
	}
	// Explicitly-set empty CSV means "none", overriding any default.
	if cfg.Auth.RequiredGroups != nil {
		t.Errorf("Auth.RequiredGroups = %v, want nil for empty env", cfg.Auth.RequiredGroups)
	}
}

func TestBadEnvValueNamesVariable(t *testing.T) {
	_, err := loadWith(t, required(map[string]string{"ARTI_DEVICE_TOKEN_TTL": "not-a-duration"}), "")
	if err == nil || !strings.Contains(err.Error(), "ARTI_DEVICE_TOKEN_TTL") {
		t.Fatalf("err = %v, want mention of ARTI_DEVICE_TOKEN_TTL", err)
	}
}

func TestUnknownYAMLKeyRejected(t *testing.T) {
	_, err := loadWith(t, required(nil), "server:\n  adress: \":1\"\n")
	if err == nil || !strings.Contains(err.Error(), "adress") {
		t.Fatalf("err = %v, want unknown-key error naming 'adress'", err)
	}
}

func TestSecretsNotAcceptedInYAML(t *testing.T) {
	// Secrets are environment-only by construction: their fields are
	// invisible to the YAML decoder, so setting them in the file is an
	// unknown-key error rather than a silent success.
	for _, body := range []string{
		"auth:\n  signing_key: sneaky\n",
		"database:\n  url: postgres://x\n",
		"storage:\n  access_key: AKIA123\n",
		"llm:\n  api_key: sk-ant-123\n",
		"notifications:\n  slack_bot_token: xoxb-1\n",
	} {
		if _, err := loadWith(t, required(nil), body); err == nil {
			t.Errorf("yaml %q accepted, want unknown-key error", body)
		}
	}
}

func TestConfigFileMissing(t *testing.T) {
	_, err := loadWith(t, required(map[string]string{"ARTI_CONFIG_FILE": "/nonexistent/nope.yaml"}), "")
	if err == nil || !strings.Contains(err.Error(), "ARTI_CONFIG_FILE") {
		t.Fatalf("err = %v, want error naming ARTI_CONFIG_FILE", err)
	}
}

// TestEffectiveConfigRoundTrips pins that a dumped effective configuration
// (doctor --print-effective-config) is itself loadable: durations must
// marshal in Go syntax, not bare nanoseconds.
func TestEffectiveConfigRoundTrips(t *testing.T) {
	dumped, err := yamlMarshal(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadWith(t, required(nil), string(dumped)); err != nil {
		t.Fatalf("dumped config does not load back: %v", err)
	}
	if !strings.Contains(string(dumped), "token_ttl: 24h") {
		t.Errorf("dump should carry Go duration syntax, got:\n%s", dumped)
	}
}
