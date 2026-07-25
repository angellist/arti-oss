package opensearch

import (
	"context"
	"fmt"
	"strings"
)

// IndexName is the single index that holds all artifact documents.
const IndexName = "artifacts"

// EnsureIndex creates the artifacts index with the full mapping if it
// doesn't already exist. Safe to call on every startup — the PUT is
// a no-op when the index is already present.
func (c *Client) EnsureIndex(ctx context.Context) error {
	if c == nil {
		return nil
	}
	// Check existence first.
	resp, err := c.do(ctx, "HEAD", "/"+IndexName, nil)
	if err != nil {
		return fmt.Errorf("opensearch: check index: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return nil // already exists
	}

	body := map[string]any{
		"settings": indexSettings(),
		"mappings": indexMappings(),
	}
	resp, err = c.do(ctx, "PUT", "/"+IndexName, body)
	if err != nil {
		return fmt.Errorf("opensearch: create index: %w", err)
	}
	respBody, rerr := readBody(resp)
	if rerr != nil {
		// A concurrent pod can create the index between our HEAD and PUT (the
		// check-then-create isn't atomic). OpenSearch then returns 400
		// resource_already_exists_exception — treat that as success so the
		// losing pod doesn't fail startup / disable search.
		if strings.Contains(string(respBody), "resource_already_exists_exception") {
			return nil
		}
		return rerr
	}
	c.logger.Info("opensearch: created index", "index", IndexName)
	return nil
}

func indexSettings() map[string]any {
	return map[string]any{
		"index.knn":          true,
		"number_of_shards":   1,
		"number_of_replicas": 1,
		"refresh_interval":   "1s",
		"max_result_window":  10000,
		"analysis": map[string]any{
			"analyzer": map[string]any{
				"slug_analyzer": map[string]any{
					"type":        "custom",
					"tokenizer":   "standard",
					"char_filter": []string{"slug_splitter"},
					"filter":      []string{"lowercase"},
				},
			},
			"char_filter": map[string]any{
				"slug_splitter": map[string]any{
					"type":        "pattern_replace",
					"pattern":     "[-_]",
					"replacement": " ",
				},
			},
		},
	}
}

func indexMappings() map[string]any {
	return map[string]any{
		"properties": map[string]any{
			"artifact_id":   keyword(),
			"artifact_type": keyword(),
			"named_slug": map[string]any{
				"type":    "keyword",
				"copy_to": "slug_text",
			},
			"slug_text": map[string]any{
				"type":     "text",
				"analyzer": "slug_analyzer",
			},
			"version": map[string]any{"type": "integer"},
			"title": map[string]any{
				"type": "text",
				"fields": map[string]any{
					"keyword": keyword(),
				},
			},
			"description":  map[string]any{"type": "text"},
			"content_text": map[string]any{"type": "text"},
			"content_type": keyword(),
			// creator is a keyword for exact filtering, but also copied to an
			// analyzed creator_text field so free-text search matches by
			// name/email the way the Postgres ILIKE path does (mirrors the
			// named_slug -> slug_text pattern above).
			"creator": map[string]any{
				"type":    "keyword",
				"copy_to": "creator_text",
			},
			"creator_text":   map[string]any{"type": "text"},
			"scopes":         keyword(),
			"labels":         keyword(),
			"allowed_access": keyword(),
			"created_at":     map[string]any{"type": "date"},
			"modified_at":    map[string]any{"type": "date"},
			"is_deleted":     map[string]any{"type": "boolean"},
			"is_latest":      map[string]any{"type": "boolean"},
			"size_bytes":     map[string]any{"type": "long"},
			// Embedding vector for future kNN search. 1024 dimensions
			// matches Amazon Titan Embeddings v2 / Voyage.
			"embedding": map[string]any{
				"type":      "knn_vector",
				"dimension": 1024,
				"method": map[string]any{
					"name":       "hnsw",
					"engine":     "nmslib",
					"space_type": "cosinesimil",
					"parameters": map[string]any{
						"ef_construction": 128,
						"m":               16,
					},
				},
			},
		},
	}
}

func keyword() map[string]any {
	return map[string]any{"type": "keyword"}
}
