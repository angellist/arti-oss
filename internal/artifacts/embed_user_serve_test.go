package artifacts

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A user-mode page must boot the bridge with NO token — only the connect block
// the handshake needs (surface + mint path + the version-pinned appId), plus
// the surface's embedder-origin list for the token relay.
func TestInjectAppBridgeUser_ConnectConfigNoToken(t *testing.T) {
	s := &Service{}
	out := string(s.injectAppBridgeUser([]byte("<html><body></body></html>"), "text/html", "AID-1", "panel",
		[]string{"https://host.example.com"}, nil))
	i := strings.Index(out, "window.__ARTI_APP__=")
	if i < 0 {
		t.Fatal("config not injected")
	}
	cfgJSON := out[i+len("window.__ARTI_APP__=") : strings.Index(out[i:], ";")+i]
	var cfg struct {
		AppID    string `json:"appId"`
		Endpoint string `json:"endpoint"`
		Token    string `json:"token"`
		Connect  struct {
			Surface string   `json:"surface"`
			Mint    string   `json:"mint"`
			Poll    string   `json:"poll"`
			Origins []string `json:"origins"`
		} `json:"connect"`
	}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatalf("injected config is not valid JSON: %v\n%s", err, cfgJSON)
	}
	if cfg.Token != "" {
		t.Fatal("user-mode config must carry NO token")
	}
	if cfg.AppID != "AID-1" || cfg.Endpoint != "/api/apps/mcp" ||
		cfg.Connect.Surface != "panel" || cfg.Connect.Mint != "/auth/embed/app-token" ||
		cfg.Connect.Poll != "/embed/panel/token" {
		t.Fatalf("bad connect config: %+v", cfg)
	}
	if len(cfg.Connect.Origins) != 1 || cfg.Connect.Origins[0] != "https://host.example.com" {
		t.Fatalf("surface origins must reach the bridge config (token-relay targets): %+v", cfg.Connect)
	}
	if !strings.Contains(out, "arti_embed_connect") {
		t.Fatal("bridge JS with the connect handshake not injected")
	}
	// Without origins the key is omitted — the bridge never announces to "*".
	out2 := string(s.injectAppBridgeUser([]byte("<html><body></body></html>"), "text/html", "AID-1", "panel", nil, nil))
	if strings.Contains(out2, `"origins"`) {
		t.Fatal("origins key must be absent when the surface config has none")
	}
	// Non-HTML bodies are untouched.
	if got := string(s.injectAppBridgeUser([]byte("{}"), "application/json", "A", "panel", nil, nil)); got != "{}" {
		t.Fatalf("non-HTML body modified: %q", got)
	}
}

// The bridge JS must handle BOTH boot states: token-carrying (/app and
// service-mode embeds) and connect-carrying (user-mode embeds) — tell the two
// 401s apart (OBO consent vs expired app token → re-mint), and deliver the
// user-mode token by POLLING (works in a sandboxed popup), not postMessage.
func TestAppBridgeJS_UserModeBranches(t *testing.T) {
	for _, want := range []string{
		"(!cfg.token && !cfg.connect)", // boots with either credential source
		"authorization_required",       // OBO discriminator kept
		"staleToken && cfg.connect",    // unusable-token → re-mint path
		"does not match app_id",        // version-mismatch 403 joins that path
		"Connect as you",               // visible affordance, never auto-popup
		"fetch(pollURL(state)",         // delivery is by polling, not postMessage/opener
		"b.ready && b.token",           // poll result shape
		"arti-app:token",               // relay: minted token announced to the host…
		"cfg.connect.origins",          // …pinned to the surface's configured origins
		"arti_token=",                  // relay: host-cached token adopted from the fragment
		"history.replaceState",         // …and scrubbed from the URL
	} {
		if !strings.Contains(appBridgeJS, want) {
			t.Errorf("appBridgeJS missing %q", want)
		}
	}
	// The embed-connect's old postMessage delivery must be GONE (the OBO
	// consent popup's own e.source/arti_oauth handshake legitimately remains —
	// it degrades gracefully because it also finishes on popup-close).
	if strings.Contains(appBridgeJS, `postMessage({source:"arti-embed"`) {
		t.Error("appBridgeJS still delivers the embed token via postMessage")
	}
}

// User-mode sibling files must never fall back to a caller-minted token: they
// ride the email-less files token, with the cookie path only as the local-dev
// (no minter wired) fallback.
func TestEmbedFilesBaseUser(t *testing.T) {
	s := &Service{}
	if got := s.embedFilesBaseUser("panel", "AID"); got != "/api/artifacts/AID/files/" {
		t.Fatalf("no minter: %q", got)
	}
	s.embedFilesToken = func(artifactID string) (string, error) { return "FTOK-" + artifactID, nil }
	if got := s.embedFilesBaseUser("panel", "AID"); got != "/embed/panel/_files/FTOK-AID/" {
		t.Fatalf("with minter: %q", got)
	}
	s.embedFilesToken = func(string) (string, error) { return "", errors.New("boom") }
	if got := s.embedFilesBaseUser("panel", "AID"); got != "/api/artifacts/AID/files/" {
		t.Fatalf("minter error must fall back: %q", got)
	}
}
