package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Index upserts a single document. The document ID is the artifact_id
// (UUID string), so re-indexing the same artifact is idempotent.
func (c *Client) Index(ctx context.Context, doc Doc) error {
	if c == nil {
		return nil
	}
	path := fmt.Sprintf("/%s/_doc/%s", IndexName, doc.ArtifactID)
	resp, err := c.do(ctx, "PUT", path, doc)
	if err != nil {
		c.logger.Error("opensearch: index doc", "id", doc.ArtifactID, "err", err)
		return err
	}
	if _, rerr := readBody(resp); rerr != nil {
		c.logger.Error("opensearch: index doc response", "id", doc.ArtifactID, "err", rerr)
		return rerr
	}
	return nil
}

// Delete removes a document by artifact_id. Returns nil on 404 (already gone).
func (c *Client) Delete(ctx context.Context, artifactID string) error {
	if c == nil {
		return nil
	}
	path := fmt.Sprintf("/%s/_doc/%s", IndexName, artifactID)
	resp, err := c.do(ctx, "DELETE", path, nil)
	if err != nil {
		c.logger.Error("opensearch: delete doc", "id", artifactID, "err", err)
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("opensearch: delete %s: HTTP %d", artifactID, resp.StatusCode)
	}
	return nil
}

// BulkIndex indexes multiple documents in a single _bulk request.
// Returns the number of successfully indexed documents.
func (c *Client) BulkIndex(ctx context.Context, docs []Doc) (int, error) {
	if c == nil || len(docs) == 0 {
		return 0, nil
	}

	var buf bytes.Buffer
	for _, doc := range docs {
		action := fmt.Sprintf(`{"index":{"_index":"%s","_id":"%s"}}`, IndexName, doc.ArtifactID)
		buf.WriteString(action)
		buf.WriteByte('\n')
		b, err := json.Marshal(doc)
		if err != nil {
			return 0, fmt.Errorf("opensearch: bulk marshal: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}

	url := c.endpoint + "/_bulk"
	bodyBytes := buf.Bytes()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, fmt.Errorf("opensearch: bulk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	// Use the shared auth path so /_bulk is SigV4-signed in production, not just
	// the JSON requests routed through do().
	if err := c.authRequest(ctx, req, bodyBytes); err != nil {
		return 0, fmt.Errorf("opensearch: bulk auth: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("opensearch: bulk request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("opensearch: bulk read: %w", err)
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("opensearch: bulk HTTP %d: %s", resp.StatusCode, truncate(string(body), 512))
	}

	var result struct {
		Errors bool `json:"errors"`
		Items  []struct {
			Index struct {
				Status int `json:"status"`
			} `json:"index"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("opensearch: bulk response parse: %w", err)
	}
	ok := 0
	for _, item := range result.Items {
		if item.Index.Status >= 200 && item.Index.Status < 300 {
			ok++
		}
	}
	if result.Errors {
		c.logger.Warn("opensearch: bulk had errors", "total", len(docs), "ok", ok)
	}
	return ok, nil
}

// MarkDeleted updates the is_deleted flag on a document without removing it
// from the index (so archived artifacts are searchable by admins).
func (c *Client) MarkDeleted(ctx context.Context, artifactID string, deleted bool) error {
	if c == nil {
		return nil
	}
	path := fmt.Sprintf("/%s/_update/%s", IndexName, artifactID)
	body := map[string]any{
		"doc": map[string]any{
			"is_deleted": deleted,
		},
	}
	resp, err := c.do(ctx, "POST", path, body)
	if err != nil {
		c.logger.Error("opensearch: mark deleted", "id", artifactID, "err", err)
		return err
	}
	if resp.StatusCode == 404 {
		resp.Body.Close()
		return nil
	}
	if _, rerr := readBody(resp); rerr != nil {
		return rerr
	}
	return nil
}
