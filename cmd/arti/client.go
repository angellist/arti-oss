package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPError is returned by the client for non-2xx responses.
type HTTPError struct {
	Status int
	Detail string
	Code   string
}

func (e HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d (%s): %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail)
}

// Client is the CLI's HTTP wrapper.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// newClient loads the cached token if present. When no token exists, it
// returns a client with an empty token — server-side `ARTI_AUTH_DISABLED`
// mode accepts those requests. Otherwise the server replies 401 and the
// CLI surfaces the error normally.
func newClient(baseURL string) (*Client, error) {
	t, err := loadToken()
	if err != nil && !errors.Is(err, errNotLoggedIn) {
		return nil, err
	}
	return &Client{
		Base:  strings.TrimRight(baseURL, "/"),
		Token: t.AccessToken, // "" when not logged in
		HTTP:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func newPublicClient(baseURL string) *Client {
	return &Client{Base: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// DoJSON encodes body, posts with Bearer, decodes into `into`. `body` may be nil.
func (c *Client) DoJSON(method, path string, body any, into any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.Base+path, rdr)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// GetRaw returns the raw body + content-type for path. Caller may pass nil
// into for a discard.
func (c *Client) GetRaw(path string) ([]byte, string, error) {
	req, _ := http.NewRequest("GET", c.Base+path, nil)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", readError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	return body, resp.Header.Get("Content-Type"), err
}

// Delete is a thin wrapper.
func (c *Client) Delete(path string) error {
	return c.DoJSON("DELETE", path, nil, nil)
}

func readError(resp *http.Response) error {
	raw, _ := io.ReadAll(resp.Body)
	var apiErr struct {
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(raw, &apiErr)
	if apiErr.Detail == "" {
		apiErr.Detail = string(raw)
	}
	return HTTPError{Status: resp.StatusCode, Detail: apiErr.Detail, Code: apiErr.Code}
}

// QS builds a query-string from k/v pairs. Slice values become repeated keys.
func QS(pairs ...any) string {
	if len(pairs)%2 != 0 {
		panic("QS: odd number of args")
	}
	v := url.Values{}
	for i := 0; i < len(pairs); i += 2 {
		key, _ := pairs[i].(string)
		switch val := pairs[i+1].(type) {
		case nil:
			continue
		case string:
			if val != "" {
				v.Set(key, val)
			}
		case []string:
			for _, x := range val {
				v.Add(key, x)
			}
		case int:
			if val > 0 {
				v.Set(key, fmt.Sprintf("%d", val))
			}
		case bool:
			if val {
				v.Set(key, "true")
			}
		}
	}
	s := v.Encode()
	if s == "" {
		return ""
	}
	return "?" + s
}

var errMissing = errors.New("missing identifier")
