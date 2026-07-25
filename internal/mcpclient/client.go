// Package mcpclient is a minimal outbound MCP client: it forwards a single
// tool call to a remote Streamable-HTTP MCP server (e.g. a Runlayer proxy, or
// arti's own /mcp). It speaks the JSON-RPC 2.0 subset MCP servers expect —
// `initialize` → (optional) `notifications/initialized` → `tools/call` — and
// tolerates both plain `application/json` and `text/event-stream` responses,
// an optional `Mcp-Session-Id`, and an optional Bearer token.
//
// It is deliberately tiny: arti only ever needs to drive one tool call on
// behalf of an APP artifact, never the full client lifecycle.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized is returned when the upstream answers 401. The apps proxy
// uses it to start the Runlayer OAuth handshake (mint an authorize URL).
var ErrUnauthorized = errors.New("mcpclient: upstream requires authorization (401)")

// Client is a reusable MCP-over-HTTP caller. Safe for concurrent use.
type Client struct{ httpc *http.Client }

func New() *Client { return &Client{httpc: &http.Client{Timeout: 60 * time.Second}} }

const protocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// CallTool runs initialize (+ initialized when the server is stateful) then
// tools/call, returning the raw JSON-RPC `result` (an MCP tool result with a
// `content` array and optional `structuredContent`). bearer may be "".
func (c *Client) CallTool(ctx context.Context, endpoint, bearer, tool string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	sess, err := c.initialize(ctx, endpoint, bearer)
	if err != nil {
		return nil, err
	}
	// Only stateful Streamable-HTTP servers (those that hand back a session
	// id) expect the initialized notification; arti's own stateless /mcp does
	// not, so we skip it there.
	if sess != "" {
		_, _, _ = c.do(ctx, endpoint, bearer, sess, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	}
	resp, _, err := c.do(ctx, endpoint, bearer, sess, rpcRequest{
		JSONRPC: "2.0", ID: 2, Method: "tools/call",
		Params: map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (c *Client) initialize(ctx context.Context, endpoint, bearer string) (string, error) {
	resp, sess, err := c.do(ctx, endpoint, bearer, "", rpcRequest{
		JSONRPC: "2.0", ID: 1, Method: "initialize",
		Params: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "arti", "version": "0.1.0"},
		},
	})
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", resp.Error
	}
	return sess, nil
}

// do posts one JSON-RPC message and returns the parsed response plus any
// Mcp-Session-Id header. A 401 maps to ErrUnauthorized; other 4xx/5xx to a
// descriptive error. Notifications (no id) tolerate an empty/202 body.
func (c *Client) do(ctx context.Context, endpoint, bearer, sess string, req rpcRequest) (rpcResponse, string, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return rpcResponse{}, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	if sess != "" {
		httpReq.Header.Set("Mcp-Session-Id", sess)
	}
	res, err := c.httpc.Do(httpReq)
	if err != nil {
		return rpcResponse{}, "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	outSess := res.Header.Get("Mcp-Session-Id")

	if res.StatusCode == http.StatusUnauthorized {
		return rpcResponse{}, outSess, ErrUnauthorized
	}
	if res.StatusCode >= 400 {
		return rpcResponse{}, outSess, fmt.Errorf("mcpclient: upstream %d: %s", res.StatusCode, snippet(raw))
	}
	rr, err := parseRPC(raw, res.Header.Get("Content-Type"), req.ID)
	return rr, outSess, err
}

// parseRPC extracts a JSON-RPC response from either a bare JSON body or an SSE
// (`text/event-stream`) body. On an SSE stream a compliant server may interleave
// progress notifications and other messages, so we return the `data:` payload
// whose `id` matches the request (wantID); only if none matches do we fall back
// to the last well-formed response (tolerates servers that omit/format id).
func parseRPC(raw []byte, contentType string, wantID int) (rpcResponse, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return rpcResponse{}, nil // notification / 202 with no body
	}
	isSSE := strings.Contains(contentType, "text/event-stream") ||
		bytes.HasPrefix(raw, []byte("event:")) || bytes.HasPrefix(raw, []byte("data:")) ||
		bytes.Contains(raw, []byte("\ndata:"))
	if isSSE {
		var last, matched rpcResponse
		var found, gotMatch bool
		for _, line := range bytes.Split(raw, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[len("data:"):])
			if len(payload) == 0 {
				continue
			}
			var r rpcResponse
			if json.Unmarshal(payload, &r) == nil && (len(r.Result) > 0 || r.Error != nil || len(r.ID) > 0) {
				last, found = r, true
				if idEquals(r.ID, wantID) {
					matched, gotMatch = r, true
				}
			}
		}
		if gotMatch {
			return matched, nil
		}
		if found {
			return last, nil
		}
		return rpcResponse{}, fmt.Errorf("mcpclient: no JSON-RPC payload in SSE stream: %s", snippet(raw))
	}
	// Non-streamed application/json: a single response object IS the response to
	// this single request (we never batch and open a fresh request per call), so
	// there's nothing to disambiguate by id. If the server echoed an id, sanity-
	// check it but don't fail on a benign mismatch — the body is the only reply.
	var r rpcResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return rpcResponse{}, fmt.Errorf("mcpclient: bad JSON-RPC response: %w (%s)", err, snippet(raw))
	}
	return r, nil
}

// idEquals reports whether a JSON-RPC response id (raw JSON) equals the numeric
// request id. JSON-RPC ids may be numbers or strings; we only ever send numeric
// ids, so we compare numerically and treat anything else as non-matching.
func idEquals(rawID json.RawMessage, want int) bool {
	if len(rawID) == 0 {
		return false
	}
	var n json.Number
	if err := json.Unmarshal(rawID, &n); err != nil {
		return false
	}
	i, err := n.Int64()
	return err == nil && int(i) == want
}

func snippet(b []byte) string {
	const max = 240
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
