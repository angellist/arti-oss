package admin

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Every connector the deployments run today must pass the admin write rules,
// or the first edit after cutover would refuse a row that already works. The
// policy's hosts are the seed's hosts, which is what serve derives when
// ARTI_APP_MCP_ALLOWED_HOSTS is unset.
func TestConnectorPolicyAdmitsDeployedConnectors(t *testing.T) {
	files, _ := filepath.Glob("../../angellist/config/*/env.yaml")
	if len(files) == 0 {
		t.Skip("no deployment config in this tree")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		if err := yaml.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		js, _ := env["ARTI_APP_MCP_SERVERS"].(string)
		if js == "" {
			continue
		}
		var entries map[string]pgstore.AppMCPServer
		if err := json.Unmarshal([]byte(js), &entries); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		p := ConnectorPolicy{Reserved: []string{"arti-self", "llm"}}
		for _, e := range entries {
			if u, err := url.Parse(e.ResourceURL); err == nil {
				p.Hosts = append(p.Hosts, u.Hostname())
			}
		}
		for name, e := range entries {
			e.Name = name
			if err := p.validate(e); err != nil {
				t.Errorf("%s: %s: %v", f, name, err)
			}
		}
	}
}

func TestConnectorPolicyValidate(t *testing.T) {
	p := ConnectorPolicy{Reserved: []string{"arti-self", "llm"}, Hosts: []string{"mcp.example.com", ".corp.example"}}
	ok := pgstore.AppMCPServer{Name: "notion", ResourceURL: "https://mcp.example.com/x", Auth: "oauth", Scope: "s"}
	if err := p.validate(ok); err != nil {
		t.Fatalf("valid connector refused: %v", err)
	}
	for label, mut := range map[string]func(*pgstore.AppMCPServer){
		"uppercase name":      func(c *pgstore.AppMCPServer) { c.Name = "Notion" },
		"leading dash":        func(c *pgstore.AppMCPServer) { c.Name = "-x" },
		"built-in":            func(c *pgstore.AppMCPServer) { c.Name = "arti-self" },
		"http":                func(c *pgstore.AppMCPServer) { c.ResourceURL = "http://mcp.example.com/x" },
		"off-allowlist":       func(c *pgstore.AppMCPServer) { c.ResourceURL = "https://mcp.example.com.evil.net/x" },
		"suffix without dot":  func(c *pgstore.AppMCPServer) { c.ResourceURL = "https://evilcorp.example/x" },
		"service auth":        func(c *pgstore.AppMCPServer) { c.Auth = "service" },
		"oauth without scope": func(c *pgstore.AppMCPServer) { c.Scope = " " },
		"blank tool":          func(c *pgstore.AppMCPServer) { c.ToolAllowlist = []string{" "} },
	} {
		c := ok
		mut(&c)
		if err := p.validate(c); err == nil {
			t.Errorf("%s: accepted, want refused", label)
		}
	}
	sub := ok
	sub.ResourceURL = "https://a.corp.example/x"
	if err := p.validate(sub); err != nil {
		t.Errorf("subdomain of a dotted host entry refused: %v", err)
	}
	if err := (ConnectorPolicy{}).validate(ok); err == nil {
		t.Errorf("an empty host allowlist must refuse every write")
	}
}
