// Package mcp implements a minimal MCP (Model Context Protocol) server
// over HTTP. It speaks the JSON-RPC 2.0 subset that claude.ai's MCP
// client and the Anthropic MCP inspector understand: `initialize`,
// `tools/list`, `tools/call`. Authentication is handled by the calling
// router (Bearer JWT, same as REST).
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/comments"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Server exposes the artifacts.Service as MCP tools. The comments service is
// optional and read-only here (list_comments); nil disables that tool.
type Server struct {
	svc      *artifacts.Service
	comments *comments.Service
}

func NewServer(svc *artifacts.Service, cs *comments.Service) *Server {
	return &Server{svc: svc, comments: cs}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handle)
}

// JSON-RPC envelopes.
type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rpcReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCErr(w, nil, -32700, "parse error: "+err.Error())
		return
	}
	resp := rpcResp{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "arti", "version": "0.1.0"},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolSpecs()}
	case "tools/call":
		out, err := s.callTool(r.Context(), req.Params)
		if err != nil {
			resp.Error = &rpcError{Code: -32000, Message: err.Error()}
		} else {
			resp.Result = out
		}
	case "ping":
		resp.Result = map[string]any{}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeRPCErr(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResp{
		JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg},
	})
}

// ─── tools ───────────────────────────────────────────────────────────

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func toolSpecs() []toolSpec {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	strArr := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	orderBy := func() map[string]any {
		return map[string]any{"type": "string", "enum": []string{"created", "title", "type", "slug", "version", "creator", "scope"}}
	}
	orderDir := func() map[string]any {
		return map[string]any{"type": "string", "enum": []string{"desc", "asc"}}
	}
	return []toolSpec{
		{
			Name:        "add_artifact",
			Description: "Create (or version) an artifact. Default type=TEXT. Pass ensure_new=true with a named_slug to fail (409) if that slug already exists. Pass allowed_access to gate who can read this version — array of glob-on-email patterns; '*' (default) allows any authenticated reader, '*@example.com' restricts to that domain, ['alice@x','bob@x'] restricts to specific people. Empty array = creator-only. Pass allowed_write to split read from write: it's the subset of readers allowed to push new versions / append / edit (unioned into allowed_access automatically). Omit allowed_write = writers are the same as readers (today's behavior); empty array = creator-only writes. Pass scopes as an array (e.g. [\"a:bt-auto-route\",\"u:lavina.kalwani\"]); the singular \"scope\" is deprecated.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"title", "content_type"},
				"properties": map[string]any{
					"title":          str(),
					"content_type":   str(),
					"content":        str(),
					"content_base64": str(),
					"artifact_type":  map[string]any{"type": "string", "enum": []string{"TEXT", "PACKAGE", "APP"}},
					"named_slug":     str(),
					"description":    str(),
					"scope":          str(),
					"scopes":         strArr(),
					"labels":         strArr(),
					"entry_point":    str(),
					"ensure_new":     map[string]any{"type": "boolean"},
					"allowed_access": strArr(),
					"allowed_write":  strArr(),
				},
			},
		},
		{
			Name:        "append_artifact",
			Description: "Append text content to an existing slug as a new version, atomically (body = prior || separator || content). Auto-creates v1 if the slug doesn't exist (title + content_type required in that path; optional overrides after). Pass idempotency_key (a stable hash of the logical event) to make retries safe — the server caches the response for 24h per (key, creator) and replays it on repeat. Binary content_types are rejected; use add_artifact with artifact_type=ATTACHMENT for those.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"slug", "content"},
				"properties": map[string]any{
					"slug":            str(),
					"content":         str(),
					"separator":       str(),
					"idempotency_key": str(),
					"title":           str(),
					"description":     str(),
					"content_type":    str(),
					"scopes":          strArr(),
					"labels":          strArr(),
					"allowed_access":  strArr(),
					"allowed_write":   strArr(),
				},
			},
		},
		{
			Name:        "update_artifact",
			Description: "Update an existing artifact's METADATA in place — title, scopes, labels, and/or allowed_access — WITHOUT creating a new version. Content and artifact_type are immutable: to change the body, call add_artifact with the same named_slug to publish a new version. `ident` is a UUID (edits that exact version) or a slug (edits the latest version you can read; sibling versions keep their prior metadata, so edit them individually if needed). Only fields you pass are changed; omit a field to leave it untouched, or pass an empty array to clear it (e.g. allowed_access:[] = creator-only). allowed_write is the subset of readers who may write (omit = writers follow readers; [] = creator-only writes); it is unioned into allowed_access. Creator-or-MANAGE_ARTIFACTS only; editing a kind:skill artifact also requires MANAGE_SKILLS.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ident"},
				"properties": map[string]any{
					"ident":          str(),
					"version":        map[string]any{"type": "integer"},
					"title":          str(),
					"scopes":         strArr(),
					"labels":         strArr(),
					"allowed_access": strArr(),
					"allowed_write":  strArr(),
				},
			},
		},
		{
			Name:        "get_artifact",
			Description: "Fetch metadata for one artifact by UUID or slug. The result includes size_bytes and sha256 — a cheap way to gauge how large the content is BEFORE reading it (for large/blob-backed artifacts this does not transfer the content). Check size_bytes here, then decide whether to read_artifact in full or with max_bytes.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ident"},
				"properties": map[string]any{
					"ident":   str(),
					"version": map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name:        "read_artifact",
			Description: "Fetch the content of an artifact. Textual content_types come back as UTF-8 text; binary as base64. The reply is annotated with size_bytes (full artifact size), sha256, returned_bytes, and truncated. For a large artifact, check size_bytes first (via get_artifact or a prior reply) and/or pass max_bytes to read only the first N bytes — avoids pulling a big document into context just to peek at it.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ident"},
				"properties": map[string]any{
					"ident":     str(),
					"version":   map[string]any{"type": "integer"},
					"max_bytes": map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name:        "list_artifacts",
			Description: "List artifacts (filterable). Each result carries size_bytes, so you can gauge document sizes from the listing without a separate fetch. Sort with order_by (created|title|type|slug|version|creator|scope; default created) + order_dir (desc|asc; default desc) — e.g. order_by=created,order_dir=desc for newest-first.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type":             str(),
					"creator":          str(),
					"scope":            str(),
					"labels":           strArr(),
					"limit":            map[string]any{"type": "integer"},
					"offset":           map[string]any{"type": "integer"},
					"order_by":         orderBy(),
					"order_dir":        orderDir(),
					"include_archived": map[string]any{"type": "boolean"},
				},
			},
		},
		{
			Name:        "search_artifacts",
			Description: "Substring search over title, description, slug. q is optional when you filter by labels and/or scope (e.g. enumerate a dataset by label). Each result carries size_bytes, so you can gauge document sizes from the results without a separate fetch. Sort with order_by (created|title|type|slug|version|creator|scope; default created) + order_dir (desc|asc; default desc) — e.g. order_by=created,order_dir=desc for the latest matches first. An explicit order_by is always honored (it routes through Postgres rather than relevance ranking).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"q":                str(),
					"scope":            str(),
					"labels":           strArr(),
					"limit":            map[string]any{"type": "integer"},
					"offset":           map[string]any{"type": "integer"},
					"order_by":         orderBy(),
					"order_dir":        orderDir(),
					"include_archived": map[string]any{"type": "boolean"},
				},
			},
		},
		{
			Name:        "list_artifact_versions",
			Description: "List every non-deleted version of a slug.",
			InputSchema: map[string]any{
				"type":       "object",
				"required":   []string{"slug"},
				"properties": map[string]any{"slug": str()},
			},
		},
		{
			Name:        "archive_artifact",
			Description: "Soft-delete by UUID (one version) or slug (all versions).",
			InputSchema: map[string]any{
				"type":       "object",
				"required":   []string{"ident"},
				"properties": map[string]any{"ident": str()},
			},
		},
		{
			Name:        "list_package_files",
			Description: "List entries inside a PACKAGE artifact.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ident"},
				"properties": map[string]any{
					"ident":   str(),
					"version": map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name:        "read_package_file",
			Description: "Read one file from inside a PACKAGE artifact. The reply carries size_bytes (the file's full size), returned_bytes, and truncated; pass max_bytes to read only the first N bytes of a large file instead of pulling it whole.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ident", "path"},
				"properties": map[string]any{
					"ident":     str(),
					"path":      str(),
					"version":   map[string]any{"type": "integer"},
					"max_bytes": map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name:        "list_comments",
			Description: "List comment threads (with their comments) on an artifact. Read-only. `ident` is a UUID or slug; `version` is optional (for a slug, defaults to the latest version you can read). Returns all threads by default; pass exclude_resolved=true to omit resolved ones. Only returns comments on artifacts you can already read.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ident"},
				"properties": map[string]any{
					"ident":            str(),
					"version":          map[string]any{"type": "integer"},
					"exclude_resolved": map[string]any{"type": "boolean"},
				},
			},
		},
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	return s.dispatch(ctx, p.Name, p.Arguments)
}

// dispatch routes a single tool call by name; args is the raw JSON each tool
// unmarshals into its own struct. Split out of callTool so it can also be
// invoked in-process (see CallToolInProcess).
func (s *Server) dispatch(ctx context.Context, name string, args json.RawMessage) (any, error) {
	switch name {
	case "add_artifact":
		return s.toolAdd(ctx, args)
	case "append_artifact":
		return s.toolAppend(ctx, args)
	case "update_artifact":
		return s.toolUpdate(ctx, args)
	case "get_artifact":
		return s.toolGet(ctx, args)
	case "read_artifact":
		return s.toolRead(ctx, args)
	case "list_artifacts":
		return s.toolList(ctx, args)
	case "search_artifacts":
		return s.toolSearch(ctx, args)
	case "list_artifact_versions":
		return s.toolVersions(ctx, args)
	case "archive_artifact":
		return s.toolArchive(ctx, args)
	case "list_package_files":
		return s.toolListPkg(ctx, args)
	case "read_package_file":
		return s.toolReadPkg(ctx, args)
	case "list_comments":
		return s.toolListComments(ctx, args)
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

// CallToolInProcess dispatches one tool call in-process as the caller already
// in ctx (set via auth.WithIdentity) and returns the marshaled MCP tool-result
// envelope — the same shape mcpclient.CallTool yields for a remote call. The
// apps proxy uses this to short-circuit arti's own read tools (no OBO hop, no
// consent popup) while reusing the exact handlers and per-caller access checks.
// The caller is responsible for restricting which tool names reach this.
func (s *Server) CallToolInProcess(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	out, err := s.dispatch(ctx, name, args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// content envelope used by all tool replies. MCP clients expect a list
// of { type, text } blocks.
func toolReply(payload any) map[string]any {
	b, _ := json.Marshal(payload)
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(b)},
		},
	}
}

// callerForList returns the email to drop into ListInput.CallerEmail.
// Admins bypass the access filter, so they get "". Non-admins always
// get filtered by their own email. include_archived for non-admins is
// allowed (so users can see their own archived), but with the access
// filter still in place — combined with the service-layer
// archive-creator-only check, that keeps non-admins from peeking at
// archived docs that aren't theirs even if they had read access.
// listCaller returns the (CallerEmail, CallerGroups) to filter a list/search
// by. A caller with MANAGE_ARTIFACTS bypasses the filter entirely (""), so no
// groups are needed; otherwise the caller's group tokens are resolved so
// group-granted artifacts show up in the catalog (parity with the REST list).
func (s *Server) listCaller(ctx context.Context, _ bool) (string, []string, error) {
	caller := auth.EmailFromContext(ctx)
	ok, err := s.svc.HasPermission(ctx, caller, rbac.ManageArtifacts)
	if err != nil {
		return "", nil, err
	}
	if ok {
		return "", nil, nil // MANAGE_ARTIFACTS bypasses the access filter
	}
	groups, err := s.svc.CallerGroups(ctx, caller)
	if err != nil {
		return "", nil, err
	}
	return caller, groups, nil
}

func (s *Server) toolAdd(ctx context.Context, raw json.RawMessage) (any, error) {
	var args artifacts.CreateRequest
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	email := auth.EmailFromContext(ctx)
	info, err := s.svc.Create(ctx, args, email)
	if err != nil {
		return nil, err
	}
	return toolReply(info), nil
}

func (s *Server) toolAppend(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Slug string `json:"slug"`
		artifacts.AppendRequest
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args.Slug == "" {
		return nil, fmt.Errorf("slug is required")
	}
	email := auth.EmailFromContext(ctx)
	info, replayed, err := s.svc.Append(ctx, args.Slug, args.AppendRequest, email)
	if err != nil {
		return nil, err
	}
	// Mark replays so MCP clients can distinguish a fresh write from a
	// cached one (e.g. for retry logic, metrics, or surfacing in the UI).
	payload := map[string]any{
		"artifact":          info,
		"idempotent_replay": replayed,
	}
	return toolReply(payload), nil
}

func (s *Server) toolUpdate(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Ident   string `json:"ident"`
		Version *int32 `json:"version,omitempty"`
		artifacts.UpdateMetadataRequest
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Ident == "" {
		return nil, errors.New("ident is required")
	}
	caller := auth.EmailFromContext(ctx)
	// Resolve ident → the artifact UUID to edit. A UUID names that exact
	// version; a slug resolves to the latest version the caller can read
	// (GetBySlug enforces access and returns that version's id). The edit
	// touches only that one version — siblings keep their prior metadata.
	id, err := uuid.Parse(a.Ident)
	if err != nil {
		info, gerr := s.svc.GetBySlug(ctx, a.Ident, a.Version, caller)
		if gerr != nil {
			return nil, gerr
		}
		if id, err = uuid.Parse(info.ArtifactID); err != nil {
			return nil, err
		}
	}
	out, err := s.svc.UpdateMetadata(ctx, id, a.UpdateMetadataRequest, caller)
	if err != nil {
		return nil, err
	}
	return toolReply(out), nil
}

func (s *Server) toolGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Ident   string `json:"ident"`
		Version *int32 `json:"version,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	caller := auth.EmailFromContext(ctx)
	if id, err := uuid.Parse(a.Ident); err == nil {
		info, err := s.svc.Get(ctx, id, caller)
		if err != nil {
			return nil, err
		}
		return toolReply(info), nil
	}
	info, err := s.svc.GetBySlug(ctx, a.Ident, a.Version, caller)
	if err != nil {
		return nil, err
	}
	return toolReply(info), nil
}

func (s *Server) toolRead(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Ident    string `json:"ident"`
		Version  *int32 `json:"version,omitempty"`
		MaxBytes int    `json:"max_bytes,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	var (
		rc  io.ReadCloser
		ct  string
		row sqlc.Artifact
		err error
	)
	caller := auth.EmailFromContext(ctx)
	if id, perr := uuid.Parse(a.Ident); perr == nil {
		rc, ct, row, err = s.svc.Content(ctx, id, caller)
	} else {
		rc, ct, row, err = s.svc.ContentBySlug(ctx, a.Ident, a.Version, caller)
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	body, truncated, err := readUpTo(rc, a.MaxBytes)
	if err != nil {
		return nil, err
	}
	// size_bytes/sha256 describe the FULL artifact (stored columns), so they're
	// accurate even when max_bytes returned only a prefix.
	return contentReply(body, ct, row.SizeBytes, row.SHA256, truncated), nil
}

func (s *Server) toolList(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Type            *string  `json:"type,omitempty"`
		Creator         *string  `json:"creator,omitempty"`
		Scope           *string  `json:"scope,omitempty"`
		Labels          []string `json:"labels,omitempty"`
		Limit           int      `json:"limit,omitempty"`
		Offset          int      `json:"offset,omitempty"`
		OrderBy         string   `json:"order_by,omitempty"`
		OrderDir        string   `json:"order_dir,omitempty"`
		IncludeArchived bool     `json:"include_archived,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	caller, groups, err := s.listCaller(ctx, a.IncludeArchived)
	if err != nil {
		return nil, err
	}
	in := pgstore.ListInput{
		Creator:         a.Creator,
		Scope:           a.Scope,
		Labels:          a.Labels,
		Limit:           int32(a.Limit),
		Offset:          int32(a.Offset),
		OrderBy:         a.OrderBy,
		OrderDir:        a.OrderDir,
		IncludeArchived: a.IncludeArchived,
		CallerEmail:     caller,
		CallerGroups:    groups,
	}
	// Resolves pseudo-types (markdown / html / diagram / json / pdf / image) to a
	// content_type glob, exactly as the web UI does. Assigning a.Type straight to
	// ArtifactType made `type: "MARKDOWN"` silently return zero rows here.
	if a.Type != nil {
		artifacts.ApplyTypeFilter(&in, *a.Type)
	}
	res, err := s.svc.List(ctx, in)
	if err != nil {
		return nil, err
	}
	return toolReply(res), nil
}

func (s *Server) toolSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Q               string   `json:"q"`
		Scope           *string  `json:"scope,omitempty"`
		Labels          []string `json:"labels,omitempty"`
		Limit           int      `json:"limit,omitempty"`
		Offset          int      `json:"offset,omitempty"`
		OrderBy         string   `json:"order_by,omitempty"`
		OrderDir        string   `json:"order_dir,omitempty"`
		IncludeArchived bool     `json:"include_archived,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	// An empty scope string is not a real filter (the store ignores it), so it
	// must not satisfy the "at least one filter" requirement — otherwise a
	// {q:"", scope:""} call would return everything unfiltered.
	hasScope := a.Scope != nil && *a.Scope != ""
	if a.Q == "" && len(a.Labels) == 0 && !hasScope {
		return nil, errors.New("provide q, labels, or scope to search")
	}
	caller, groups, err := s.listCaller(ctx, a.IncludeArchived)
	if err != nil {
		return nil, err
	}
	res, err := s.svc.Search(ctx, a.Q, pgstore.ListInput{
		Scope:           a.Scope,
		Labels:          a.Labels,
		Limit:           int32(a.Limit),
		Offset:          int32(a.Offset),
		OrderBy:         a.OrderBy,
		OrderDir:        a.OrderDir,
		IncludeArchived: a.IncludeArchived,
		CallerEmail:     caller,
		CallerGroups:    groups,
	})
	if err != nil {
		return nil, err
	}
	return toolReply(res), nil
}

func (s *Server) toolVersions(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	v, err := s.svc.Versions(ctx, a.Slug, auth.EmailFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return toolReply(map[string]any{"versions": v}), nil
}

func (s *Server) toolArchive(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Ident string `json:"ident"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	caller := auth.EmailFromContext(ctx)
	if id, err := uuid.Parse(a.Ident); err == nil {
		admin, err := s.svc.HasPermission(ctx, caller, rbac.ManageArtifacts)
		if err != nil {
			return nil, err
		}
		if !admin {
			creator, cerr := s.svc.CreatorOf(ctx, id)
			if cerr != nil {
				return nil, cerr
			}
			if !strings.EqualFold(creator, caller) {
				return nil, errors.New("forbidden: only the creator or MANAGE_ARTIFACTS may archive")
			}
		}
		n, err := s.svc.ArchiveByID(ctx, id)
		if err != nil {
			return nil, err
		}
		return toolReply(map[string]int64{"archived_count": n}), nil
	}
	allowed, exists, err := s.svc.CanArchiveSlug(ctx, caller, a.Ident)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("not found")
	}
	if !allowed {
		return nil, errors.New("forbidden: only the creator of every version or MANAGE_ARTIFACTS may archive this slug")
	}
	n, err := s.svc.ArchiveBySlug(ctx, a.Ident)
	if err != nil {
		return nil, err
	}
	return toolReply(map[string]int64{"archived_count": n}), nil
}

func (s *Server) toolListPkg(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Ident   string `json:"ident"`
		Version *int32 `json:"version,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	caller := auth.EmailFromContext(ctx)
	// We need the raw row (not just the DTO) to call ListPackageFiles.
	// Re-fetch via service for simplicity.
	if id, perr := uuid.Parse(a.Ident); perr == nil {
		info, err := s.svc.Get(ctx, id, caller)
		if err != nil {
			return nil, err
		}
		if !pgstore.IsPackageLike(info.ArtifactType) {
			return nil, fmt.Errorf("not a PACKAGE artifact")
		}
		return toolReply(info.Metadata["package"]), nil
	}
	info, err := s.svc.GetBySlug(ctx, a.Ident, a.Version, caller)
	if err != nil {
		return nil, err
	}
	return toolReply(info.Metadata["package"]), nil
}

func (s *Server) toolReadPkg(ctx context.Context, raw json.RawMessage) (any, error) {
	// We use a small shim: fetch zip via Content, then pkgzip.ReadEntry.
	// To keep this self-contained without a circular import, the work
	// is done by the artifacts.Service exposes used here.
	var a struct {
		Ident    string `json:"ident"`
		Path     string `json:"path"`
		Version  *int32 `json:"version,omitempty"`
		MaxBytes int    `json:"max_bytes,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	// ReadPackageFile decompresses the whole entry, so we have its full size
	// even when max_bytes returns only a prefix (zip entries can't be range-read
	// the way a blob can — the win here is keeping the prefix out of context).
	body, ct, err := s.readPackageEntry(ctx, a.Ident, a.Version, a.Path)
	if err != nil {
		return nil, err
	}
	total := int64(len(body))
	truncated := false
	if a.MaxBytes > 0 && len(body) > a.MaxBytes {
		body = body[:a.MaxBytes]
		truncated = true
	}
	return contentReply(body, ct, &total, nil, truncated), nil
}

func (s *Server) readPackageEntry(ctx context.Context, ident string, ver *int32, path string) ([]byte, string, error) {
	caller := auth.EmailFromContext(ctx)
	if id, err := uuid.Parse(ident); err == nil {
		rc, _, row, err := s.svc.Content(ctx, id, caller)
		if err != nil {
			return nil, "", err
		}
		_ = rc.Close()
		return s.svc.ReadPackageFile(ctx, row, path)
	}
	rc, _, row, err := s.svc.ContentBySlug(ctx, ident, ver, caller)
	if err != nil {
		return nil, "", err
	}
	_ = rc.Close()
	return s.svc.ReadPackageFile(ctx, row, path)
}

func (s *Server) toolListComments(ctx context.Context, raw json.RawMessage) (any, error) {
	if s.comments == nil {
		return nil, errors.New("comments are not available")
	}
	var a struct {
		Ident           string `json:"ident"`
		Version         *int32 `json:"version,omitempty"`
		ExcludeResolved bool   `json:"exclude_resolved,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Ident == "" {
		return nil, errors.New("ident is required")
	}
	caller := auth.EmailFromContext(ctx)
	// Resolve ident → the artifact UUID whose comments we read. A UUID names a
	// specific version; a slug resolves to the latest version the caller can
	// read (GetBySlug enforces access and returns that version's id).
	id, err := uuid.Parse(a.Ident)
	if err != nil {
		info, gerr := s.svc.GetBySlug(ctx, a.Ident, a.Version, caller)
		if gerr != nil {
			return nil, gerr
		}
		if id, err = uuid.Parse(info.ArtifactID); err != nil {
			return nil, err
		}
	}
	out, err := s.comments.ListForCaller(ctx, id, caller, !a.ExcludeResolved)
	if err != nil {
		return nil, err
	}
	return toolReply(out), nil
}

func isTextual(ct string) bool {
	if ct == "application/json" || ct == "application/yaml" || ct == "application/javascript" {
		return true
	}
	if len(ct) >= 5 && ct[:5] == "text/" {
		return true
	}
	return false
}

// readUpTo reads from r, capping at maxBytes when maxBytes > 0 (0 or negative
// reads everything). It reads one byte past the cap so it can report whether
// content remained without buffering the whole stream — for a blob-backed
// artifact the caller's deferred Close abandons the rest, so a small max_bytes
// peek doesn't transfer the full object.
func readUpTo(r io.Reader, maxBytes int) (body []byte, truncated bool, err error) {
	if maxBytes <= 0 {
		b, e := io.ReadAll(r)
		return b, false, e
	}
	b, e := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if e != nil {
		return nil, false, e
	}
	if len(b) > maxBytes {
		return b[:maxBytes], true, nil
	}
	return b, false, nil
}

// contentReply wraps read bytes in the MCP content envelope and annotates it
// with size metadata so an agent can tell how big the artifact is and whether
// it received only a prefix. Textual content_types are returned as UTF-8 text
// (a trailing partial rune left by a byte cap is trimmed); everything else is
// base64. totalBytes/sha256 describe the FULL artifact (nil → field omitted);
// returned_bytes is how many raw content bytes this reply carries.
func contentReply(body []byte, ct string, totalBytes *int64, sha256 *string, truncated bool) map[string]any {
	var text string
	if isTextual(ct) {
		if truncated {
			body = trimPartialRune(body)
		}
		text = string(body)
	} else {
		text = base64.StdEncoding.EncodeToString(body)
	}
	out := map[string]any{
		"content":        []map[string]any{{"type": "text", "text": text}},
		"content_type":   ct,
		"returned_bytes": len(body),
		"truncated":      truncated,
	}
	if totalBytes != nil {
		out["size_bytes"] = *totalBytes
	}
	if sha256 != nil {
		out["sha256"] = *sha256
	}
	return out
}

// trimPartialRune drops a trailing incomplete UTF-8 sequence left by a byte-
// length cap, so a peeked text prefix stays valid UTF-8 instead of ending in a
// replacement char. At most utf8.UTFMax-1 bytes are removed.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return b
}
