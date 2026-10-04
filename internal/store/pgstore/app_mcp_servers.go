package pgstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─── APP MCP connectors ──────────────────────────────────────────────
//
// The upstreams an APP may reach through the apps proxy. Every proxied call
// resolves its server here, so the table is cached with the same discipline
// as the block list: served only while the invalidation bus is healthy,
// reloaded from Postgres otherwise, and bounded by a TTL for a replica that
// missed a notification.

const connectorCacheTTL = 15 * time.Second

// AppMCPServer is one connector row.
type AppMCPServer struct {
	Name          string    `json:"name"`
	ResourceURL   string    `json:"resource_url"`
	Auth          string    `json:"auth"`
	Scope         string    `json:"scope"`
	Enabled       bool      `json:"enabled"`
	ToolAllowlist []string  `json:"tool_allowlist"`
	Notes         string    `json:"notes"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedBy     string    `json:"updated_by"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type connectorCache struct {
	mu         sync.Mutex
	servers    map[string]AppMCPServer
	loadedAt   time.Time
	generation uint64
}

func (c *connectorCache) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.generation++
	c.mu.Unlock()
}

const appMCPServerCols = `name, resource_url, auth, scope, enabled, tool_allowlist, notes,
	created_by, created_at, updated_by, updated_at`

func scanAppMCPServer(row pgx.Row) (AppMCPServer, error) {
	var m AppMCPServer
	err := row.Scan(&m.Name, &m.ResourceURL, &m.Auth, &m.Scope, &m.Enabled, &m.ToolAllowlist, &m.Notes,
		&m.CreatedBy, &m.CreatedAt, &m.UpdatedBy, &m.UpdatedAt)
	if m.ToolAllowlist == nil {
		m.ToolAllowlist = []string{}
	}
	return m, err
}

func (s *Store) connectorSnap(ctx context.Context) (map[string]AppMCPServer, error) {
	for {
		s.connectors.mu.Lock()
		if s.authzCacheHealthy() && !s.connectors.loadedAt.IsZero() && time.Since(s.connectors.loadedAt) < connectorCacheTTL {
			m := s.connectors.servers
			s.connectors.mu.Unlock()
			return m, nil
		}
		generation := s.connectors.generation
		s.connectors.mu.Unlock()

		list, err := s.ListAppMCPServers(ctx)
		if err != nil {
			return nil, err
		}
		m := make(map[string]AppMCPServer, len(list))
		for _, c := range list {
			m[c.Name] = c
		}

		s.connectors.mu.Lock()
		if generation != s.connectors.generation {
			s.connectors.mu.Unlock()
			continue
		}
		if s.authzCacheHealthy() {
			s.connectors.servers, s.connectors.loadedAt = m, time.Now()
		}
		s.connectors.mu.Unlock()
		return m, nil
	}
}

// LookupAppMCPServer returns the named connector, disabled or not; the caller
// decides what a disabled one means. found is false for a name with no row.
func (s *Store) LookupAppMCPServer(ctx context.Context, name string) (AppMCPServer, bool, error) {
	m, err := s.connectorSnap(ctx)
	if err != nil {
		return AppMCPServer{}, false, err
	}
	c, ok := m[name]
	return c, ok, nil
}

// ListAppMCPServers returns every connector, by name. Always read from
// Postgres: the admin page must show what is stored, not a cached copy.
func (s *Store) ListAppMCPServers(ctx context.Context) ([]AppMCPServer, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appMCPServerCols+` FROM app_mcp_servers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list app mcp servers: %w", err)
	}
	defer rows.Close()
	out := []AppMCPServer{}
	for rows.Next() {
		m, err := scanAppMCPServer(rows)
		if err != nil {
			return nil, fmt.Errorf("pgstore: list app mcp servers scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CreateAppMCPServer inserts a connector. The caller has validated it.
// ErrConflict when the name is taken.
func (s *Store) CreateAppMCPServer(ctx context.Context, in AppMCPServer, by string) (AppMCPServer, error) {
	m, err := scanAppMCPServer(s.pool.QueryRow(ctx, `
		INSERT INTO app_mcp_servers (name, resource_url, auth, scope, enabled, tool_allowlist, notes, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		RETURNING `+appMCPServerCols,
		in.Name, in.ResourceURL, in.Auth, in.Scope, in.Enabled, nonNilStrings(in.ToolAllowlist), in.Notes, by))
	if err != nil {
		if isUniqueViolation(err) {
			return AppMCPServer{}, fmt.Errorf("%w: connector %q already exists", ErrConflict, in.Name)
		}
		return AppMCPServer{}, fmt.Errorf("pgstore: create app mcp server: %w", err)
	}
	s.invalidateConnectors(ctx)
	return m, nil
}

// UpdateAppMCPServer applies edit to the stored connector under a row lock
// and writes the result, so concurrent edits cannot drop each other. An edit
// error aborts the write and is returned as is. ErrNotFound when absent.
func (s *Store) UpdateAppMCPServer(ctx context.Context, name string, edit func(*AppMCPServer) error, by string) (before, after AppMCPServer, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return before, after, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, err = scanAppMCPServer(tx.QueryRow(ctx,
		`SELECT `+appMCPServerCols+` FROM app_mcp_servers WHERE name = $1 FOR UPDATE`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return before, after, ErrNotFound
	}
	if err != nil {
		return before, after, fmt.Errorf("pgstore: update app mcp server: %w", err)
	}
	in := before
	in.ToolAllowlist = slices.Clone(before.ToolAllowlist)
	if err := edit(&in); err != nil {
		return before, after, err
	}
	after, err = scanAppMCPServer(tx.QueryRow(ctx, `
		UPDATE app_mcp_servers
		   SET resource_url = $2, auth = $3, scope = $4, enabled = $5, tool_allowlist = $6, notes = $7,
		       updated_by = $8, updated_at = now()
		 WHERE name = $1
		RETURNING `+appMCPServerCols,
		name, in.ResourceURL, in.Auth, in.Scope, in.Enabled, nonNilStrings(in.ToolAllowlist), in.Notes, by))
	if err != nil {
		return before, after, fmt.Errorf("pgstore: update app mcp server: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return before, after, err
	}
	s.invalidateConnectors(ctx)
	return before, after, nil
}

// DeleteAppMCPServer removes a connector and revokes every viewer's OBO token
// for its resource URL, unless another connector still points at that URL.
// Returns the deleted row and how many tokens were revoked. ErrNotFound when
// absent.
func (s *Store) DeleteAppMCPServer(ctx context.Context, name string) (AppMCPServer, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AppMCPServer{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	m, err := scanAppMCPServer(tx.QueryRow(ctx,
		`DELETE FROM app_mcp_servers WHERE name = $1 RETURNING `+appMCPServerCols, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return AppMCPServer{}, 0, ErrNotFound
	}
	if err != nil {
		return AppMCPServer{}, 0, fmt.Errorf("pgstore: delete app mcp server: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM oauth_obo_tokens
		 WHERE resource = $1
		   AND NOT EXISTS (SELECT 1 FROM app_mcp_servers WHERE resource_url = $1)`, m.ResourceURL)
	if err != nil {
		return AppMCPServer{}, 0, fmt.Errorf("pgstore: revoke obo tokens: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AppMCPServer{}, 0, err
	}
	s.invalidateConnectors(ctx)
	return m, tag.RowsAffected(), nil
}

// SeedAppMCPServers inserts seed only when the table is empty, and reports how
// many rows it inserted. The table lock makes concurrent booting replicas seed
// once between them.
func (s *Store) SeedAppMCPServers(ctx context.Context, seed []AppMCPServer) (int, error) {
	if len(seed) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE app_mcp_servers IN EXCLUSIVE MODE`); err != nil {
		return 0, fmt.Errorf("pgstore: seed lock: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app_mcp_servers`).Scan(&n); err != nil {
		return 0, fmt.Errorf("pgstore: seed count: %w", err)
	}
	if n > 0 {
		return 0, nil
	}
	for _, c := range seed {
		if _, err := tx.Exec(ctx, `
			INSERT INTO app_mcp_servers (name, resource_url, auth, scope, enabled, tool_allowlist, notes, created_by, updated_by)
			VALUES ($1, $2, $3, $4, TRUE, '{}', '', 'bootstrap', 'bootstrap')`,
			c.Name, c.ResourceURL, c.Auth, c.Scope); err != nil {
			return 0, fmt.Errorf("pgstore: seed %q: %w", c.Name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.invalidateConnectors(ctx)
	return len(seed), nil
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
