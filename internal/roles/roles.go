// Package roles exposes the role/assignment admin API. Every endpoint is gated
// by the MANAGE_ROLES permission (404 for callers without it, so the surface
// isn't discoverable). The permission/role vocabulary lives in internal/rbac;
// storage + resolution live in pgstore. This package is only the HTTP layer.
package roles

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

type Service struct{ store *pgstore.Store }

func NewService(store *pgstore.Store) *Service { return &Service{store: store} }

func (s *Service) Mount(r chi.Router) {
	r.Get("/api/permissions", s.listPermissions)
	r.Get("/api/roles", s.listRoles)
	r.Post("/api/roles", s.createRole)
	r.Patch("/api/roles/{name}", s.updateRole)
	r.Delete("/api/roles/{name}", s.deleteRole)
	r.Get("/api/role-assignments", s.listAssignments)
	r.Post("/api/role-assignments", s.assign)
	r.Delete("/api/role-assignments", s.unassign)
	r.Get("/api/role-lookup", s.lookup)
	r.Get("/api/users", s.listUsers)
	r.Post("/api/users", s.addUser)
}

// ─── DTOs ────────────────────────────────────────────────────────────

type RoleDTO struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Builtin     bool     `json:"builtin"`
	CreatedAt   string   `json:"created_at"`
	ModifiedAt  string   `json:"modified_at"`
}

func toRoleDTO(r pgstore.Role) RoleDTO {
	perms := r.Permissions
	if perms == nil {
		perms = []string{}
	}
	return RoleDTO{
		Name:        r.Name,
		Description: r.Description,
		Permissions: perms,
		Builtin:     rbac.IsBuiltinRole(r.Name),
		CreatedAt:   r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		ModifiedAt:  r.ModifiedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type AssignmentDTO struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	RoleName      string `json:"role_name"`
	CreatedBy     string `json:"created_by"`
}

// ─── permissions ─────────────────────────────────────────────────────

func (s *Service) listPermissions(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	keys := make([]string, 0, len(rbac.AllPermissions))
	for _, p := range rbac.AllPermissions {
		keys = append(keys, string(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": keys})
}

// ─── roles ───────────────────────────────────────────────────────────

func (s *Service) listRoles(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	roles, err := s.store.ListRoles(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RoleDTO, 0, len(roles))
	for _, role := range roles {
		out = append(out, toRoleDTO(role))
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

type roleReq struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

func (s *Service) createRole(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	var body roleReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	role, err := s.store.CreateRole(r.Context(), body.Name, body.Description, body.Permissions)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRoleDTO(role))
}

// updateRoleReq lets a PATCH change description and/or permissions; an omitted
// field is left unchanged.
type updateRoleReq struct {
	Description *string   `json:"description"`
	Permissions *[]string `json:"permissions"`
}

func (s *Service) updateRole(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	cur, err := s.store.GetRole(r.Context(), name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var body updateRoleReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	desc := cur.Description
	if body.Description != nil {
		desc = *body.Description
	}
	perms := cur.Permissions
	if body.Permissions != nil {
		perms = *body.Permissions
	}
	role, err := s.store.UpdateRole(r.Context(), name, desc, perms)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRoleDTO(role))
}

func (s *Service) deleteRole(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	if err := s.store.DeleteRole(r.Context(), chi.URLParam(r, "name")); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── assignments ─────────────────────────────────────────────────────

func (s *Service) listAssignments(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	as, err := s.store.ListAssignments(r.Context(), r.URL.Query().Get("role"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]AssignmentDTO, 0, len(as))
	for _, a := range as {
		out = append(out, AssignmentDTO{a.PrincipalType, a.PrincipalID, a.RoleName, a.CreatedBy})
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": out})
}

type assignReq struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	RoleName      string `json:"role_name"`
}

func (s *Service) assign(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	var body assignReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	caller := auth.EmailFromContext(r.Context())
	if err := s.store.AssignRole(r.Context(), body.PrincipalType, body.PrincipalID, body.RoleName, caller); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unassign removes an assignment. Parameters come from the query string
// (?principal_type=&principal_id=&role_name=) rather than a request body, since
// bodies on DELETE are unreliable through some proxies/clients.
func (s *Service) unassign(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	q := r.URL.Query()
	ptype, pid, role := q.Get("principal_type"), q.Get("principal_id"), q.Get("role_name")
	// Self-demotion guard: don't let a caller remove ADMIN from themselves — a
	// sole admin could otherwise drop their own MANAGE_ROLES and be locked out
	// until the next restart re-asserts the config floor. Another admin must do
	// it instead.
	caller := auth.EmailFromContext(r.Context())
	if role == rbac.RoleAdmin && ptype == rbac.PrincipalUser &&
		strings.EqualFold(strings.TrimSpace(pid), strings.TrimSpace(caller)) {
		writeErr(w, http.StatusBadRequest, "you can't remove your own ADMIN role; ask another admin")
		return
	}
	if err := s.store.UnassignRole(r.Context(), ptype, pid, role); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── per-user lookup ─────────────────────────────────────────────────

// HeldRoleDTO is one role a person holds, with where it came from: "baseline"
// (the USER role), "direct" (assigned to their email), or "group:<name>".
type HeldRoleDTO struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Source      string   `json:"source"`
}

// lookup returns a person's effective access: the roles they hold (with source)
// and the union of permissions. The FE renders this as a role×permission matrix.
func (s *Service) lookup(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
	if email == "" {
		writeErr(w, http.StatusBadRequest, "email query param required")
		return
	}

	roles, err := s.store.ListRoles(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	permsOf := map[string][]string{}
	for _, role := range roles {
		p := role.Permissions
		if p == nil {
			p = []string{}
		}
		permsOf[role.Name] = p
	}

	// Held roles come from the store so this endpoint and the Users roster
	// share one definition of role sources — baseline/direct/group:<name>, one
	// entry per (role, source) pair. Reimplementing it here is what would let
	// the two surfaces disagree.
	heldRoles, err := s.store.HeldRoles(r.Context(), email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	held := make([]HeldRoleDTO, 0, len(heldRoles))
	for _, hr := range heldRoles {
		held = append(held, HeldRoleDTO{Name: hr.Name, Permissions: permsOf[hr.Name], Source: hr.Source})
	}

	effSet, err := s.store.EffectivePermissions(r.Context(), email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	eff := make([]string, 0, len(effSet))
	for p := range effSet {
		eff = append(eff, p)
	}
	sort.Strings(eff)

	writeJSON(w, http.StatusOK, map[string]any{
		"email":                 email,
		"roles":                 held,
		"effective_permissions": eff,
	})
}

// ─── helpers ─────────────────────────────────────────────────────────

func (s *Service) requireManageRoles(w http.ResponseWriter, r *http.Request) bool {
	ok, err := s.store.HasPermission(r.Context(), auth.EmailFromContext(r.Context()), rbac.ManageRoles)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return false
	}
	return true
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgstore.ErrInvalidInput):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "pgstore: "))
	case errors.Is(err, pgstore.ErrConflict):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "pgstore: "))
	case errors.Is(err, pgstore.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"detail": msg, "code": http.StatusText(code)})
}

// ─── users roster ────────────────────────────────────────────────────
//
// GET /api/users enumerates every principal arti knows. That is a deliberate
// difference from GET /api/people (internal/groups/people.go), which answers a
// bounded typeahead for any authenticated caller and refuses to enumerate: both
// read the same union, but only this one is allowed to return all of it, and
// only behind MANAGE_ROLES.

// RosterRoleDTO is one role a principal holds, with its source.
type RosterRoleDTO struct {
	Name   string `json:"name"`
	Source string `json:"source"` // "baseline" | "direct" | "group:<name>"
}

// RosterUserDTO is one row of the Users page.
type RosterUserDTO struct {
	Email string `json:"email"`
	Kind  string `json:"kind"` // "human" | "service"
	Note  string `json:"note"`

	Roles     []RosterRoleDTO `json:"roles"`
	Groups    []string        `json:"groups"`
	IdPGroups []string        `json:"idp_groups"`

	// IdPStale reports that the IdP snapshot has aged past
	// ARTI_IDP_GROUPS_MAX_AGE (or that the bound is disabled), so the
	// idp_groups above currently grant nothing. Clients must not render a stale
	// group as live access.
	IdPStale bool `json:"idp_stale"`

	// LastSeenAt is null when no login was recorded. That is NOT "never signed
	// in": logins were only recorded from migration 0020 onward.
	LastSeenAt *string `json:"last_seen_at"`

	Registered bool    `json:"registered"`
	AddedBy    string  `json:"added_by"`
	AddedAt    *string `json:"added_at"`
}

func stamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02T15:04:05Z07:00")
	return &s
}

func toRosterDTO(u pgstore.RosterUser) RosterUserDTO {
	roles := make([]RosterRoleDTO, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, RosterRoleDTO{Name: r.Name, Source: r.Source})
	}
	// Slices are built empty rather than left nil so the JSON carries [] and a
	// client never has to distinguish absent from empty.
	groups := u.Groups
	if groups == nil {
		groups = []string{}
	}
	idp := u.IdPGroups
	if idp == nil {
		idp = []string{}
	}
	return RosterUserDTO{
		Email:      u.Email,
		Kind:       u.Kind,
		Note:       u.Note,
		Roles:      roles,
		Groups:     groups,
		IdPGroups:  idp,
		IdPStale:   len(idp) > 0 && !u.IdPFresh,
		LastSeenAt: stamp(u.LastSeenAt),
		Registered: u.Registered,
		AddedBy:    u.AddedBy,
		AddedAt:    stamp(u.AddedAt),
	}
}

func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RosterUserDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toRosterDTO(u))
	}
	// sources comes from the store, beside the CTE it describes, so the two
	// cannot drift — a principal class that is missing stays diagnosable from
	// the response rather than from the source.
	writeJSON(w, http.StatusOK, map[string]any{
		"users":   out,
		"sources": pgstore.KnownPrincipalSources,
	})
}

type addUserReq struct {
	Email string `json:"email"`
	Kind  string `json:"kind"`
	Note  string `json:"note"`
}

// addUser records a principal. It assigns no role: the response's roles field
// shows the baseline every authenticated caller already holds, and granting
// anything more is a separate role assignment.
func (s *Service) addUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireManageRoles(w, r) {
		return
	}
	var body addUserReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	u, err := s.store.AddUser(r.Context(), body.Email, body.Kind, body.Note,
		auth.EmailFromContext(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRosterDTO(u))
}
