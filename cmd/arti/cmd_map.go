package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// MapCmd groups the verbs for a MAP artifact: a keyed key/value store held
// under one slug. Keys are flat strings namespaced by a ':' separator; there
// are no tables and no joins.
//
//	arti map put   my-notes seen:cnv_123 '{"at":"2026-09-11"}'
//	arti map put   my-notes seen:cnv_123 - --if-absent      # claim, from stdin
//	arti map get   my-notes seen:cnv_123
//	arti map list  my-notes --prefix seen:
//	arti map rm    my-notes seen:cnv_123
//	arti map snapshot my-notes
type MapCmd struct {
	Get      MapGetCmd      `cmd:"" help:"read one entry"`
	List     MapListCmd     `cmd:"" help:"list entries, optionally under a key prefix"`
	Put      MapPutCmd      `cmd:"" help:"write one entry (creates the map if absent, with --title)"`
	Rm       MapRmCmd       `cmd:"" help:"delete one entry"`
	Snapshot MapSnapshotCmd `cmd:"" help:"freeze the map as a new immutable NDJSON version"`
}

func mapKeyPath(slug, key string) string {
	return "/api/artifacts/by-slug/" + url.PathEscape(slug) + "/map/keys/" + url.PathEscape(key)
}

func mapPath(slug string) string {
	return "/api/artifacts/by-slug/" + url.PathEscape(slug) + "/map"
}

type MapGetCmd struct {
	Slug string `arg:"" help:"map slug"`
	Key  string `arg:"" help:"entry key"`
	Raw  bool   `help:"print only the value, not the entry envelope"`
}

func (c *MapGetCmd) Run(cli *CLI) error {
	client, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	var out map[string]json.RawMessage
	if err := client.DoJSON("GET", mapKeyPath(c.Slug, c.Key), nil, &out); err != nil {
		return err
	}
	if c.Raw {
		fmt.Println(string(out["value"]))
		return nil
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

type MapListCmd struct {
	Slug   string `arg:"" help:"map slug"`
	Prefix string `help:"only keys starting with this prefix"`
	Cursor string `help:"continue a previous page"`
	Limit  int    `help:"page size (default 100, max 1000)"`
	Keys   bool   `help:"print only the keys, one per line"`
}

func (c *MapListCmd) Run(cli *CLI) error {
	client, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	q := url.Values{}
	if c.Prefix != "" {
		q.Set("prefix", c.Prefix)
	}
	if c.Cursor != "" {
		q.Set("cursor", c.Cursor)
	}
	if c.Limit > 0 {
		q.Set("limit", strconv.Itoa(c.Limit))
	}
	path := mapPath(c.Slug)
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Entries []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
			Rev   int64           `json:"rev"`
		} `json:"entries"`
		Cursor string `json:"cursor"`
		Stats  struct {
			// Explicit tags: Go matches field names case-insensitively but
			// not across the underscore, so MaxKeys silently read 0 from
			// max_keys and the list footer reported "3/0 keys".
			Keys     int64 `json:"keys"`
			Bytes    int64 `json:"bytes"`
			MaxKeys  int64 `json:"max_keys"`
			MaxBytes int64 `json:"max_bytes"`
		} `json:"stats"`
	}
	if err := client.DoJSON("GET", path, nil, &out); err != nil {
		return err
	}
	for _, e := range out.Entries {
		if c.Keys {
			fmt.Println(e.Key)
			continue
		}
		fmt.Printf("%s\trev=%d\t%s\n", e.Key, e.Rev, string(e.Value))
	}
	fmt.Fprintf(os.Stderr, "%d shown · map holds %d/%d keys, %d/%d bytes\n",
		len(out.Entries), out.Stats.Keys, out.Stats.MaxKeys, out.Stats.Bytes, out.Stats.MaxBytes)
	if out.Cursor != "" {
		fmt.Fprintf(os.Stderr, "more: --cursor %s\n", out.Cursor)
	}
	return nil
}

type MapPutCmd struct {
	Slug     string `arg:"" help:"map slug"`
	Key      string `arg:"" help:"entry key"`
	Value    string `arg:"" optional:"" help:"JSON value, or '-' / omitted to read stdin"`
	IfAbsent bool   `name:"if-absent" help:"write only if the key is free; a lost claim exits 1 and prints the incumbent"`
	IfRev    int64  `name:"if-rev" help:"write only if the entry is still at this revision (compare-and-set)"`
	Title    string `help:"title, required when this call creates the map"`
	// Create-time only, and deliberately so: changing a live map's access is
	// `arti access`, which applies to every version of the document.
	Access       []string `help:"read-access pattern (repeatable), applied when this call CREATES the map"`
	Private      bool     `help:"creator-only reads on a map this call creates. Mutually exclusive with --access."`
	WriteAccess  []string `name:"write-access" help:"write-access pattern (repeatable), applied on create"`
	WritePrivate bool     `name:"write-private" help:"creator-only writes on a map this call creates"`
}

func (c *MapPutCmd) Run(cli *CLI) error {
	if c.IfAbsent && c.IfRev != 0 {
		return fmt.Errorf("--if-absent and --if-rev are mutually exclusive")
	}
	client, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	raw := c.Value
	if raw == "" || raw == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		raw = strings.TrimSpace(string(b))
	}
	if !json.Valid([]byte(raw)) {
		return fmt.Errorf("value is not valid JSON: %s", raw)
	}
	entry := map[string]any{"key": c.Key, "value": json.RawMessage(raw)}
	if c.IfAbsent {
		entry["if_absent"] = true
	}
	if c.IfRev != 0 {
		entry["if_rev"] = c.IfRev
	}
	if c.Private && len(c.Access) > 0 {
		return fmt.Errorf("--private and --access are mutually exclusive")
	}
	if c.WritePrivate && len(c.WriteAccess) > 0 {
		return fmt.Errorf("--write-private and --write-access are mutually exclusive")
	}
	body := map[string]any{"entries": []any{entry}}
	if c.Title != "" {
		body["title"] = c.Title
	}
	if c.Private {
		body["allowed_access"] = []string{}
	} else if len(c.Access) > 0 {
		body["allowed_access"] = c.Access
	}
	if c.WritePrivate {
		body["allowed_write"] = []string{}
	} else if len(c.WriteAccess) > 0 {
		body["allowed_write"] = c.WriteAccess
	}
	var out struct {
		Results []struct {
			Key       string          `json:"key"`
			Written   bool            `json:"written"`
			Conflict  bool            `json:"conflict"`
			Entry     json.RawMessage `json:"entry"`
			Incumbent json.RawMessage `json:"incumbent"`
		} `json:"results"`
	}
	status, err := client.DoJSONAllowing("POST", mapPath(c.Slug), body, &out, http.StatusConflict)
	if err != nil {
		return err
	}
	if len(out.Results) == 0 {
		return fmt.Errorf("server returned no result for key %q", c.Key)
	}
	r := out.Results[0]
	if status == http.StatusConflict || r.Conflict {
		fmt.Fprintf(os.Stderr, "conflict: key %q was not written\n", r.Key)
		if len(r.Incumbent) > 0 {
			fmt.Println(string(r.Incumbent))
		}
		// A lost claim is a legitimate outcome, not a crash, but a script
		// needs to branch on it — so exit non-zero with no error banner.
		os.Exit(1)
		return nil
	}
	fmt.Println(string(r.Entry))
	return nil
}

type MapRmCmd struct {
	Slug string `arg:"" help:"map slug"`
	Key  string `arg:"" help:"entry key"`
}

func (c *MapRmCmd) Run(cli *CLI) error {
	client, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	var out map[string]any
	if err := client.DoJSON("DELETE", mapKeyPath(c.Slug, c.Key), nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

type MapSnapshotCmd struct {
	Slug string `arg:"" help:"map slug"`
}

func (c *MapSnapshotCmd) Run(cli *CLI) error {
	client, err := newClient(cli.BaseURL)
	if err != nil {
		return err
	}
	var out struct {
		Unchanged bool   `json:"unchanged"`
		Version   *int32 `json:"version"`
		Entries   int    `json:"entries"`
		SizeBytes int    `json:"size_bytes"`
	}
	if err := client.DoJSON("POST", mapPath(c.Slug)+"/snapshot", nil, &out); err != nil {
		return err
	}
	v := int32(0)
	if out.Version != nil {
		v = *out.Version
	}
	if out.Unchanged {
		fmt.Fprintf(os.Stderr, "unchanged: head already matches v%d (%d entries)\n", v, out.Entries)
	} else {
		fmt.Fprintf(os.Stderr, "snapshot v%d: %d entries, %d bytes\n", v, out.Entries, out.SizeBytes)
	}
	fmt.Println(cli.BaseURL + "/s/" + c.Slug + "/" + strconv.Itoa(int(v)))
	return nil
}
