package opensearch

import (
	"time"
)

// Doc is the OpenSearch document shape for an artifact.
type Doc struct {
	ArtifactID    string    `json:"artifact_id"`
	ArtifactType  string    `json:"artifact_type"`
	NamedSlug     *string   `json:"named_slug,omitempty"`
	Version       *int32    `json:"version,omitempty"`
	Title         string    `json:"title"`
	Description   *string   `json:"description,omitempty"`
	ContentText   string    `json:"content_text,omitempty"`
	ContentType   string    `json:"content_type"`
	Creator       string    `json:"creator"`
	Scopes        []string  `json:"scopes"`
	Labels        []string  `json:"labels"`
	AllowedAccess []string  `json:"allowed_access"`
	CreatedAt     time.Time `json:"created_at"`
	ModifiedAt    time.Time `json:"modified_at"`
	IsDeleted     bool      `json:"is_deleted"`
	IsLatest      bool      `json:"is_latest"`
	SizeBytes     *int64    `json:"size_bytes,omitempty"`
}
