package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/pkgzip"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// The APP MCP connector list: which upstreams the apps proxy may reach. The
// table replaces a reviewed config value, so every write is validated here
// and logged with who made it.

var connectorNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ConnectorPolicy is the deployment's limits on what an admin may register.
type ConnectorPolicy struct {
	// Reserved names belong to the in-process built-ins and cannot be rows.
	Reserved []string
	// Hosts a resource_url may point at: an exact hostname, or ".example.com"
	// for any subdomain of it. Empty refuses every write.
	Hosts []string
}

// SetConnectorPolicy wires the limits every connector write is checked against.
func (s *Service) SetConnectorPolicy(p ConnectorPolicy) { s.connectors = p }

func (s *Service) mountConnectors(r chi.Router) {
	r.Get("/api/admin/mcp-servers", s.listConnectors)
	r.Post("/api/admin/mcp-servers", s.createConnector)
	r.Patch("/api/admin/mcp-servers/{name}", s.updateConnector)
	r.Delete("/api/admin/mcp-servers/{name}", s.deleteConnector)
	r.Get("/api/admin/mcp-servers/{name}/apps", s.connectorApps)
}

func (p ConnectorPolicy) validate(c pgstore.AppMCPServer) error {
	if !connectorNameRe.MatchString(c.Name) {
		return errors.New("name must be lowercase letters, digits and dashes, starting with a letter or digit")
	}
	if slices.Contains(p.Reserved, c.Name) {
		return fmt.Errorf("%q is a built-in server and cannot be registered", c.Name)
	}
	u, err := url.Parse(c.ResourceURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("resource_url must be an https URL")
	}
	if !p.hostAllowed(u.Hostname()) {
		return fmt.Errorf("resource_url host %q is not in this arti's connector host allowlist", u.Hostname())
	}
	switch c.Auth {
	case "none":
	case "oauth":
		if strings.TrimSpace(c.Scope) == "" {
			return errors.New("scope is required when auth is oauth")
		}
	default:
		return errors.New(`auth must be "none" or "oauth"`)
	}
	for _, t := range c.ToolAllowlist {
		if strings.TrimSpace(t) == "" {
			return errors.New("tool_allowlist entries must not be empty")
		}
	}
	return nil
}

func (p ConnectorPolicy) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, h := range p.Hosts {
		h = strings.ToLower(h)
		if host == h || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return true
		}
	}
	return false
}

func (s *Service) requireConnectorAdmin(w http.ResponseWriter, r *http.Request) bool {
	return s.requirePerm(w, r, rbac.ManageConnectors)
}

func (s *Service) listConnectors(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnectorAdmin(w, r) {
		return
	}
	list, err := s.store.ListAppMCPServers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": list})
}

type connectorInput struct {
	Name          *string   `json:"name"`
	ResourceURL   *string   `json:"resource_url"`
	Auth          *string   `json:"auth"`
	Scope         *string   `json:"scope"`
	Enabled       *bool     `json:"enabled"`
	ToolAllowlist *[]string `json:"tool_allowlist"`
	Notes         *string   `json:"notes"`
}

func (in connectorInput) applyTo(c *pgstore.AppMCPServer) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&c.ResourceURL, in.ResourceURL)
	set(&c.Auth, in.Auth)
	set(&c.Scope, in.Scope)
	set(&c.Notes, in.Notes)
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.ToolAllowlist != nil {
		c.ToolAllowlist = []string{}
		for _, t := range *in.ToolAllowlist {
			if t = strings.TrimSpace(t); t != "" && !slices.Contains(c.ToolAllowlist, t) {
				c.ToolAllowlist = append(c.ToolAllowlist, t)
			}
		}
	}
}

func decodeConnector(w http.ResponseWriter, r *http.Request) (connectorInput, bool) {
	var in connectorInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return in, false
	}
	return in, true
}

func (s *Service) createConnector(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnectorAdmin(w, r) {
		return
	}
	in, ok := decodeConnector(w, r)
	if !ok {
		return
	}
	c := pgstore.AppMCPServer{Enabled: true}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	in.applyTo(&c)
	if err := s.connectors.validate(c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	by := auth.EmailFromContext(r.Context())
	out, err := s.store.CreateAppMCPServer(r.Context(), c, by)
	if errors.Is(err, pgstore.ErrConflict) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	auditConnector(r.Context(), "create", by, nil, &out, 0)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) updateConnector(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnectorAdmin(w, r) {
		return
	}
	in, ok := decodeConnector(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	if in.Name != nil && strings.TrimSpace(*in.Name) != name {
		writeErr(w, http.StatusBadRequest, "a connector cannot be renamed; delete it and create a new one")
		return
	}
	var invalid error
	by := auth.EmailFromContext(r.Context())
	before, out, err := s.store.UpdateAppMCPServer(r.Context(), name, func(c *pgstore.AppMCPServer) error {
		in.applyTo(c)
		invalid = s.connectors.validate(*c)
		return invalid
	}, by)
	if invalid != nil {
		writeErr(w, http.StatusBadRequest, invalid.Error())
		return
	}
	if errors.Is(err, pgstore.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no connector named "+name)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	auditConnector(r.Context(), "update", by, &before, &out, 0)
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) deleteConnector(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnectorAdmin(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	gone, revoked, err := s.store.DeleteAppMCPServer(r.Context(), name)
	if errors.Is(err, pgstore.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no connector named "+name)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	auditConnector(r.Context(), "delete", auth.EmailFromContext(r.Context()), &gone, nil, revoked)
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "deleted": true, "tokens_revoked": revoked})
}

// auditConnector is the change record for this surface: the git history the
// env var had is gone, so every mutation leaves a line naming who and what.
func auditConnector(ctx context.Context, action, by string, before, after *pgstore.AppMCPServer, revoked int64) {
	attrs := []any{"action", action, "by", by}
	if before != nil {
		attrs = append(attrs, "name", before.Name, "before", *before)
	}
	if after != nil {
		if before == nil {
			attrs = append(attrs, "name", after.Name)
		}
		attrs = append(attrs, "after", *after)
	}
	if action == "delete" {
		attrs = append(attrs, "tokens_revoked", revoked)
	}
	slog.Default().InfoContext(ctx, "admin: app mcp connector changed", attrs...)
}

// connectorApp is one APP whose manifest declares a connector.
type connectorApp struct {
	ArtifactID string  `json:"artifact_id"`
	NamedSlug  *string `json:"named_slug"`
	Title      string  `json:"title"`
	Creator    string  `json:"creator"`
}

// connectorApps lists the live APPs whose manifest declares this server, so an
// admin can see what a disable or delete breaks before doing it.
//
// ponytail: reads every live APP zip per request; fine for the low hundreds
// of APPs and a rare admin action. Store the manifest's server list on the
// row if this page gets slow.
func (s *Service) connectorApps(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnectorAdmin(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	rows, err := s.pool.Query(r.Context(), `
		SELECT artifact_id FROM artifacts
		 WHERE artifact_type = $1 AND deleted_at IS NULL
		   AND (named_slug IS NULL
		        OR version = (SELECT MAX(b.version) FROM artifacts b
		                       WHERE b.named_slug = artifacts.named_slug AND b.deleted_at IS NULL))`,
		pgstore.TypeApp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var ids []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := []connectorApp{}
	unreadable := 0
	for _, id := range ids {
		row, err := s.store.GetByID(r.Context(), pgstore.UUIDFromPG(id))
		if err != nil {
			unreadable++
			continue
		}
		declares, err := s.manifestDeclares(r.Context(), row, name)
		if err != nil {
			unreadable++
			continue
		}
		if declares {
			out = append(out, connectorApp{
				ArtifactID: pgstore.UUIDFromPG(id).String(), NamedSlug: row.NamedSlug, Title: row.Title, Creator: row.Creator,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "apps": out, "scanned": len(ids), "unreadable": unreadable,
	})
}

func (s *Service) manifestDeclares(ctx context.Context, row sqlc.Artifact, server string) (bool, error) {
	rc, err := s.store.Content(ctx, row)
	if err != nil {
		return false, err
	}
	defer rc.Close()
	zb, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return false, err
	}
	body, _, err := pkgzip.ReadEntry(zb, "arti-app.json")
	if errors.Is(err, pkgzip.ErrEntryNotFound) {
		return false, nil // no manifest: the proxy refuses every call it makes
	}
	if err != nil {
		return false, err
	}
	var man struct {
		Tools []struct {
			Server string `json:"server"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &man); err != nil {
		return false, err
	}
	for _, t := range man.Tools {
		if t.Server == server {
			return true, nil
		}
	}
	return false, nil
}
