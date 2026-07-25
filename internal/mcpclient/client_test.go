package mcpclient

import (
	"encoding/json"
	"testing"
)

// On a stateful SSE stream the server may interleave notifications/other
// messages; parseRPC must return the payload whose id matches the request, not
// merely the last well-formed one.
func TestParseRPC_SSE_MatchesRequestID(t *testing.T) {
	// A progress notification (no id), then the real id:2 result, then a
	// trailing stray id:99 message. The id:2 result must win.
	sse := "event: message\n" +
		`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"p":0.5}}` + "\n\n" +
		"event: message\n" +
		`data: {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"RIGHT"}]}}` + "\n\n" +
		"event: message\n" +
		`data: {"jsonrpc":"2.0","id":99,"result":{"content":[{"type":"text","text":"WRONG"}]}}` + "\n\n"

	r, err := parseRPC([]byte(sse), "text/event-stream", 2)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(r.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Content) == 0 || got.Content[0].Text != "RIGHT" {
		t.Fatalf("want the id:2 result (RIGHT), got %s", string(r.Result))
	}
}

// A plain application/json body parses straight through.
func TestParseRPC_JSON(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`
	r, err := parseRPC([]byte(body), "application/json", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Result) == 0 || r.Error != nil {
		t.Fatalf("want a result, got err=%v result=%s", r.Error, string(r.Result))
	}
}
