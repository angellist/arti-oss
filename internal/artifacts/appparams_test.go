package artifacts

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// q is a tiny helper to build a query from a raw string.
func q(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("bad test query %q: %v", raw, err)
	}
	return v
}

// errsOf pulls the _errors slice out of a resolved param map for assertions.
func errsOf(t *testing.T, m map[string]any) []paramError {
	t.Helper()
	raw, ok := m["_errors"]
	if !ok {
		t.Fatal("_errors channel missing from resolved params")
	}
	got, ok := raw.([]paramError)
	if !ok {
		t.Fatalf("_errors is %T, want []paramError", raw)
	}
	return got
}

// The contract's whole point: an iframe embedder gets a typed, defaulted object
// regardless of what the query contains — so coercion, defaults, and lenient
// fallback must each behave exactly as declared, never failing the page.
func TestResolveAppParams_Coercion(t *testing.T) {
	specs := []appParamSpec{
		{Name: "text", Type: "string", Default: ""},
		{Name: "count", Type: "number", Default: float64(1)},
		{Name: "debug", Type: "boolean", Default: false},
		{Name: "mode", Type: "enum", Values: []string{"short", "long"}, Default: "short"},
	}

	got := resolveAppParams(specs, q(t, "text=hello&count=3&debug=true&mode=long"))
	if got["text"] != "hello" {
		t.Errorf("text = %#v, want hello", got["text"])
	}
	if got["count"] != float64(3) {
		t.Errorf("count = %#v, want 3", got["count"])
	}
	if got["debug"] != true {
		t.Errorf("debug = %#v, want true", got["debug"])
	}
	if got["mode"] != "long" {
		t.Errorf("mode = %#v, want long", got["mode"])
	}
	if len(errsOf(t, got)) != 0 {
		t.Errorf("clean query produced errors: %v", errsOf(t, got))
	}
}

// Missing params fall back to declared defaults (or zero values) so the app
// always sees a complete object.
func TestResolveAppParams_Defaults(t *testing.T) {
	specs := []appParamSpec{
		{Name: "text", Type: "string", Default: "fallback"},
		{Name: "count", Type: "number"}, // no default ⇒ zero
		{Name: "debug", Type: "boolean"},
	}
	got := resolveAppParams(specs, q(t, ""))
	if got["text"] != "fallback" {
		t.Errorf("text default = %#v, want fallback", got["text"])
	}
	if got["count"] != float64(0) {
		t.Errorf("count zero = %#v, want 0", got["count"])
	}
	if got["debug"] != false {
		t.Errorf("debug zero = %#v, want false", got["debug"])
	}
	if len(errsOf(t, got)) != 0 {
		t.Errorf("missing optional params should not error: %v", errsOf(t, got))
	}
}

// Malformed values are lenient: fall back to the default AND record the reason
// in _errors so the app can react. The page must still render.
func TestResolveAppParams_MalformedFallsBackAndRecords(t *testing.T) {
	specs := []appParamSpec{
		{Name: "count", Type: "number", Default: float64(1)},
		{Name: "mode", Type: "enum", Values: []string{"a", "b"}, Default: "a"},
	}
	got := resolveAppParams(specs, q(t, "count=abc&mode=zzz"))
	if got["count"] != float64(1) {
		t.Errorf("malformed number should fall back to default: %#v", got["count"])
	}
	if got["mode"] != "a" {
		t.Errorf("out-of-enum should fall back to default: %#v", got["mode"])
	}
	errs := errsOf(t, got)
	if len(errs) != 2 {
		t.Fatalf("want 2 recorded errors, got %d: %v", len(errs), errs)
	}
}

// Regression: a non-finite number (NaN/Inf) must not survive into the resolved
// object — json.Marshal can't encode it, which would blank the whole injected
// __ARTI_APP__ config and break the bridge. It must fall back to the default
// AND the resolved map must stay JSON-marshalable.
func TestResolveAppParams_NonFiniteNumberRejected(t *testing.T) {
	specs := []appParamSpec{{Name: "count", Type: "number", Default: float64(1)}}
	for _, bad := range []string{"NaN", "Inf", "+Inf", "-Inf", "Infinity"} {
		got := resolveAppParams(specs, q(t, "count="+url.QueryEscape(bad)))
		if got["count"] != float64(1) {
			t.Errorf("count=%q should fall back to default, got %#v", bad, got["count"])
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("resolved map with count=%q is not JSON-marshalable: %v", bad, err)
		}
		if len(errsOf(t, got)) != 1 {
			t.Errorf("count=%q should record one error", bad)
		}
	}
}

// The end-to-end guarantee behind the NaN fix: with ?count=NaN the injected
// config must still be valid JSON carrying the token, not a blanked object.
func TestInjectAppBridge_NaNParamDoesNotBlankConfig(t *testing.T) {
	s := &Service{}
	s.appToken = func(email, artifactID string) (string, error) { return "TESTTOK", nil }
	params := resolveAppParams([]appParamSpec{{Name: "count", Type: "number", Default: float64(0)}}, q(t, "count=NaN"))
	out := string(s.injectAppBridge([]byte("<html><body></body></html>"), "text/html", "AID", "u@x.com", params))
	i := strings.Index(out, "window.__ARTI_APP__=")
	if i < 0 {
		t.Fatal("config not injected")
	}
	cfgJSON := out[i+len("window.__ARTI_APP__=") : strings.Index(out[i:], ";")+i]
	var cfg map[string]any
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatalf("config blanked / invalid JSON with NaN param %q: %v", cfgJSON, err)
	}
	if cfg["token"] != "TESTTOK" {
		t.Errorf("token missing from config — bridge would break: %#v", cfg)
	}
}

// Echoed malformed values in _errors reasons are clipped, so a crafted URL with
// long malformed values can't amplify the injected JSON.
func TestResolveAppParams_ErrorReasonClipped(t *testing.T) {
	long := strings.Repeat("9", 500) + "x" // malformed number, long
	specs := []appParamSpec{{Name: "count", Type: "number", Default: float64(0)}}
	got := resolveAppParams(specs, q(t, "count="+long))
	reason := errsOf(t, got)[0].Reason
	if len(reason) > 120 { // "not a finite number: " + 60 + ellipsis, well under raw 500
		t.Errorf("error reason not clipped (len=%d): %q", len(reason), reason)
	}
}

// Unknown query keys are ignored — never reflected into the typed object — so
// embedders can append tracking params without polluting the contract.
func TestResolveAppParams_UnknownIgnored(t *testing.T) {
	specs := []appParamSpec{{Name: "text", Type: "string"}}
	got := resolveAppParams(specs, q(t, "text=hi&utm_source=email&evil=x"))
	if _, ok := got["utm_source"]; ok {
		t.Error("undeclared utm_source leaked into typed params")
	}
	if _, ok := got["evil"]; ok {
		t.Error("undeclared evil leaked into typed params")
	}
	// declared + _errors only
	if len(got) != 2 {
		t.Errorf("resolved map has %d keys, want 2 (text + _errors): %v", len(got), got)
	}
}

// A required param missing is a non-fatal note, never a page error.
func TestResolveAppParams_RequiredMissingIsNote(t *testing.T) {
	specs := []appParamSpec{{Name: "id", Type: "string", Required: true}}
	got := resolveAppParams(specs, q(t, ""))
	if got["id"] != "" {
		t.Errorf("missing required should still default: %#v", got["id"])
	}
	errs := errsOf(t, got)
	if len(errs) != 1 || errs[0].Name != "id" {
		t.Fatalf("want one note for missing required id, got %v", errs)
	}
}

// No declared params ⇒ nil, so the bridge surfaces an empty {} (and never an
// _errors channel for apps that don't opt in).
func TestResolveAppParams_NoneDeclared(t *testing.T) {
	if got := resolveAppParams(nil, q(t, "anything=1")); got != nil {
		t.Errorf("no specs should yield nil, got %#v", got)
	}
}

// Reserved underscore names (the _errors channel) can't be shadowed by a
// declared param.
func TestResolveAppParams_ReservedNamesSkipped(t *testing.T) {
	specs := []appParamSpec{{Name: "_errors", Type: "string"}, {Name: "ok", Type: "string"}}
	got := resolveAppParams(specs, q(t, "_errors=hacked&ok=yes"))
	if _, isString := got["_errors"].(string); isString {
		t.Error("_errors must remain the reserved error channel, not a string param")
	}
	if got["ok"] != "yes" {
		t.Errorf("ok = %#v, want yes", got["ok"])
	}
}

// Over-long values are truncated and noted — defensive against a giant query
// value reflected into the page.
func TestResolveAppParams_LengthCap(t *testing.T) {
	long := strings.Repeat("x", maxAppParamLen+50)
	specs := []appParamSpec{{Name: "blob", Type: "string"}}
	got := resolveAppParams(specs, q(t, "blob="+long))
	if len(got["blob"].(string)) != maxAppParamLen {
		t.Errorf("value not truncated to cap: len=%d", len(got["blob"].(string)))
	}
	if len(errsOf(t, got)) != 1 {
		t.Errorf("truncation should be recorded in _errors")
	}
}

// Security property: a param value containing </script> must be JSON-escaped in
// the injected config so it cannot break out of the <script> tag. This locks
// the escaping at the injectAppBridge boundary, not just json.Marshal.
func TestInjectAppBridge_ParamsEscapedAndExposed(t *testing.T) {
	s := &Service{}
	s.appToken = func(email, artifactID string) (string, error) { return "TESTTOK", nil }

	params := resolveAppParams(
		[]appParamSpec{{Name: "text", Type: "string"}},
		q(t, "text="+url.QueryEscape("</script><img src=x onerror=alert(1)>")),
	)
	out := string(s.injectAppBridge([]byte("<html><body></body></html>"), "text/html", "AID", "u@x.com", params))

	if strings.Contains(out, "</script><img") {
		t.Fatal("raw </script> survived into the page — script-tag breakout possible")
	}
	if !strings.Contains(out, `</script>`) {
		t.Fatalf("expected escaped </script> in injected config; got:\n%s", out)
	}
	if !strings.Contains(out, `"params":`) {
		t.Fatal("params not embedded in __ARTI_APP__ config")
	}
	if !strings.Contains(out, "window.arti.params = (cfg && cfg.params) || {};") {
		t.Fatal("bridge does not expose window.arti.params")
	}
}

// The embedding use case: an APP response must let the configured origins
// iframe it. frame-ancestors carries the allowlist, and the global
// X-Frame-Options: SAMEORIGIN (which can't express an allowlist and would
// otherwise still block cross-origin framing) is dropped for APP HTML.
func TestSetContentSecurityApp_FrameAncestors(t *testing.T) {
	s := &Service{}
	s.SetAppFrameAncestors("'self' https://flowdash.example.com")

	w := httptest.NewRecorder()
	w.Header().Set("X-Frame-Options", "SAMEORIGIN") // as the global middleware would
	s.setContentSecurityApp(w, "text/html; charset=utf-8")

	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self' https://flowdash.example.com") {
		t.Fatalf("frame-ancestors allowlist not in CSP: %q", csp)
	}
	if !strings.Contains(csp, "sandbox allow-scripts") {
		t.Errorf("sandbox directive lost: %q", csp)
	}
	if w.Header().Get("X-Frame-Options") != "" {
		t.Error("X-Frame-Options must be dropped for APP HTML so frame-ancestors is authoritative")
	}
}

// With no allowlist configured, APP HTML stays same-origin only ('self'),
// preserving the prior (X-Frame-Options: SAMEORIGIN equivalent) behavior.
func TestSetContentSecurityApp_DefaultSelf(t *testing.T) {
	s := &Service{}
	w := httptest.NewRecorder()
	s.setContentSecurityApp(w, "text/html")
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("empty allowlist should default to 'self': %q", w.Header().Get("Content-Security-Policy"))
	}
}

// Non-HTML APP responses (e.g. a JSON asset) get no CSP/frame handling.
func TestSetContentSecurityApp_NonHTMLUntouched(t *testing.T) {
	s := &Service{}
	s.SetAppFrameAncestors("'self' https://x.example.com")
	w := httptest.NewRecorder()
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	s.setContentSecurityApp(w, "application/json")
	if w.Header().Get("Content-Security-Policy") != "" {
		t.Error("non-HTML must not get a CSP")
	}
	if w.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Error("non-HTML must not have X-Frame-Options stripped")
	}
}

// When no params are declared, the injected config carries no params key (the
// bridge falls back to {}), preserving prior behavior for apps that don't opt in.
func TestInjectAppBridge_NoParamsKeyWhenNilParams(t *testing.T) {
	s := &Service{}
	s.appToken = func(email, artifactID string) (string, error) { return "TESTTOK", nil }
	out := string(s.injectAppBridge([]byte("<html><body></body></html>"), "text/html", "AID", "u@x.com", nil))

	// Confirm the marshaled config object has no "params" field.
	i := strings.Index(out, "window.__ARTI_APP__=")
	if i < 0 {
		t.Fatal("config not injected")
	}
	cfgJSON := out[i+len("window.__ARTI_APP__=") : strings.Index(out[i:], ";")+i]
	var cfg map[string]any
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatalf("config not valid JSON %q: %v", cfgJSON, err)
	}
	if _, ok := cfg["params"]; ok {
		t.Error("params key present despite nil params")
	}
}
