package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
)

// toolMapGet serves both reads a caller can want: one key, or a page under a
// prefix. They are one tool because an agent that has the key wants the entry
// and an agent that does not wants the page, and splitting them would add a
// tool to the list without adding a decision.
func (s *Server) toolMapGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Slug   string `json:"slug"`
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Slug == "" {
		return nil, errors.New("slug is required")
	}
	email := auth.EmailFromContext(ctx)
	if a.Key != "" {
		e, err := s.svc.MapGet(ctx, a.Slug, a.Key, email)
		if err != nil {
			return nil, err
		}
		return toolReply(e), nil
	}
	out, err := s.svc.MapList(ctx, a.Slug, a.Prefix, a.Cursor, a.Limit, email)
	if err != nil {
		return nil, err
	}
	return toolReply(out), nil
}

func (s *Server) toolMapPut(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Slug string `json:"slug"`
		artifacts.MapPutRequest
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Slug == "" {
		return nil, errors.New("slug is required")
	}
	out, anyConflict, err := s.svc.MapPut(ctx, a.Slug, a.MapPutRequest, auth.EmailFromContext(ctx))
	if err != nil {
		return nil, err
	}
	// A lost guard is a RESULT, not a tool error: if_absent losing is the
	// normal outcome of a dedupe check, and surfacing it as isError would make
	// every second trigger look like a failure.
	return toolReply(map[string]any{"results": out.Results, "stats": out.Stats, "conflict": anyConflict}), nil
}

func (s *Server) toolMapDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Slug string   `json:"slug"`
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Slug == "" {
		return nil, errors.New("slug is required")
	}
	n, _, err := s.svc.MapDelete(ctx, a.Slug, a.Keys, auth.EmailFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return toolReply(map[string]any{"deleted": n}), nil
}

func (s *Server) toolMapSnapshot(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Slug == "" {
		return nil, errors.New("slug is required")
	}
	res, err := s.svc.MapSnapshot(ctx, a.Slug, auth.EmailFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return toolReply(res), nil
}
