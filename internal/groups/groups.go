// Package groups exposes the user-group admin API. A group is a named
// collection of member emails; granting `group:<name>` in an artifact's
// allowed_access lets the group's current members read it (resolved live —
// see pgstore.CallerGroups).
//
// Ownership model: any authenticated caller can create a group and becomes its
// owner (created_by). A caller may see and edit/delete only the groups they
// own; admins see and manage all. Listing is therefore scoped per-caller (it
// feeds both the management page and the access-editor typeahead, so you can
// only grant groups you own). Edits/deletes by a non-owner return 404 (not
// 403) so groups aren't discoverable across owners.
//
// Note: access RESOLUTION (pgstore.CallerGroups) still considers every group
// regardless of ownership — being a member of a group grants you read access
// even if you didn't create it. Ownership gates management, not membership.
package groups

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

type Service struct{ store *pgstore.Store }

func NewService(store *pgstore.Store) *Service { return &Service{store: store} }

// Mount attaches group routes. The outer group already applies auth
// middleware; mutating handlers additionally enforce admin membership.
func (s *Service) Mount(r chi.Router) {
	r.Get("/api/groups", s.list)
	r.Post("/api/groups", s.create)
	r.Patch("/api/groups/{name}", s.update)
	r.Delete("/api/groups/{name}", s.del)
	r.Get("/api/idp-groups", s.listIdP)
	// The person half of the same typeahead the group routes above feed — see
	// people.go for why it lives here and how disclosure is bounded.
	r.Get("/api/people", s.listPeople)
}

// IdPGroupDTO is one grantable IdP (SSO) group: its name, the literal
// `idp:<name>` token to drop into allowed_access/allowed_write, and how many
// users currently carry it. Roster is never exposed.
type IdPGroupDTO struct {
	Name        string `json:"name"`
	Token       string `json:"token"`
	MemberCount int    `json:"member_count"`
}

// listIdP returns the distinct IdP group names captured at login, for the
// access-editor typeahead. Any authenticated caller may read it (it only
// reveals group names + counts, never who is in them).
func (s *Service) listIdP(w http.ResponseWriter, r *http.Request) {
	gs, err := s.store.ListIdPGroupNames(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]IdPGroupDTO, 0, len(gs))
	for _, g := range gs {
		out = append(out, IdPGroupDTO{Name: g.Name, Token: pgstore.IdPToken(g.Name), MemberCount: g.MemberCount})
	}
	writeJSON(w, http.StatusOK, map[string]any{"idp_groups": out})
}

// GroupDTO is the JSON shape returned to clients. Token is the literal string
// to drop into allowed_access; member_count saves the FE a length call.
type GroupDTO struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Members     []string `json:"members"`
	MemberCount int      `json:"member_count"`
	Token       string   `json:"token"`
	CreatedBy   string   `json:"created_by"`
	CreatedAt   string   `json:"created_at"`
	ModifiedAt  string   `json:"modified_at"`
}

func toDTO(g pgstore.Group) GroupDTO {
	members := g.Members
	if members == nil {
		members = []string{}
	}
	return GroupDTO{
		Name:        g.Name,
		DisplayName: g.DisplayName,
		Members:     members,
		MemberCount: len(members),
		Token:       pgstore.GroupToken(g.Name),
		CreatedBy:   g.CreatedBy,
		CreatedAt:   g.CreatedAt.Format(time.RFC3339),
		ModifiedAt:  g.ModifiedAt.Format(time.RFC3339),
	}
}

// list returns the groups the caller may see: admins see all, everyone else
// sees only the groups they created (own). The access-editor typeahead reads
// this too, so a non-admin can only grant groups they own.
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	gs, err := s.store.ListGroups(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	caller := auth.EmailFromContext(r.Context())
	manageAll, err := s.store.HasPermission(r.Context(), caller, rbac.ManageUserGroups)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]GroupDTO, 0, len(gs))
	for _, g := range gs {
		if manageAll || strings.EqualFold(g.CreatedBy, caller) {
			out = append(out, toDTO(g))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

type createReq struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Members     []string `json:"members"`
}

// create lets any authenticated caller make a group; they become its owner
// (created_by), which gates later edits/deletes.
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var body createReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// SoD (H8): if a role is already assigned to this group name (e.g. the group
	// was deleted and is being recreated — the role_assignment persists), only a
	// MANAGE_USER_GROUPS holder may (re)create it. Otherwise an owner could
	// resurrect a privileged name with attacker-chosen members and inherit the
	// lingering role.
	if !s.gatePrivileged(w, r, body.Name) {
		return
	}
	g, err := s.store.CreateGroup(r.Context(), body.Name, body.DisplayName, body.Members, auth.EmailFromContext(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(g))
}

// updateReq uses pointers so a PATCH can change just the display name or just
// the membership; an omitted field is left as-is.
type updateReq struct {
	DisplayName *string   `json:"display_name"`
	Members     *[]string `json:"members"`
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.loadManageable(w, r)
	if !ok {
		return
	}
	var body updateReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	display := cur.DisplayName
	if body.DisplayName != nil {
		display = *body.DisplayName
	}
	members := cur.Members
	if body.Members != nil {
		// SoD (H8): only a MANAGE_USER_GROUPS holder may change the membership of
		// a role-bearing group; the owner alone can't (it would let them hand the
		// role to arbitrary members, including a wildcard). Display-name-only
		// edits stay owner-allowed.
		if !s.gatePrivileged(w, r, cur.Name) {
			return
		}
		members = *body.Members
	}
	g, err := s.store.UpdateGroup(r.Context(), cur.Name, display, members)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(g))
}

func (s *Service) del(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.loadManageable(w, r)
	if !ok {
		return
	}
	// SoD (H8): deleting a role-bearing group is admin-only — otherwise an owner
	// could delete it and recreate it with attacker-chosen members (the role
	// assignment to group:<name> persists across the delete).
	if !s.gatePrivileged(w, r, cur.Name) {
		return
	}
	if err := s.store.DeleteGroup(r.Context(), cur.Name); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loadManageable fetches the group named in the URL and verifies the caller may
// manage it — admin or owner. Anything else (missing, or owned by someone
// else) writes a 404 so non-owners can't probe existence, and returns ok=false.
func (s *Service) loadManageable(w http.ResponseWriter, r *http.Request) (pgstore.Group, bool) {
	g, err := s.store.GetGroup(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		writeStoreErr(w, err) // ErrNotFound → 404
		return pgstore.Group{}, false
	}
	caller := auth.EmailFromContext(r.Context())
	manageAll, err := s.store.HasPermission(r.Context(), caller, rbac.ManageUserGroups)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return pgstore.Group{}, false
	}
	if !manageAll && !strings.EqualFold(g.CreatedBy, caller) {
		writeErr(w, http.StatusNotFound, "not found")
		return pgstore.Group{}, false
	}
	return g, true
}

// isPrivileged reports whether a role is assigned to group:<name>. Membership
// and lifecycle of such a group must be managed by a MANAGE_USER_GROUPS holder,
// not merely its owner (separation of duties, H8) — otherwise the owner could
// hand the role to arbitrary members.
func (s *Service) isPrivileged(ctx context.Context, name string) (bool, error) {
	roles, err := s.store.RolesAssignedTo(ctx, rbac.PrincipalGroup, name)
	if err != nil {
		return false, err
	}
	return len(roles) > 0, nil
}

// gatePrivileged writes a 403 and returns false when the named group carries a
// role and the caller lacks MANAGE_USER_GROUPS; returns true (allow) otherwise.
// A non-privileged group is owner-managed as before.
func (s *Service) gatePrivileged(w http.ResponseWriter, r *http.Request, name string) bool {
	privileged, err := s.isPrivileged(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !privileged {
		return true
	}
	ok, err := s.store.HasPermission(r.Context(), auth.EmailFromContext(r.Context()), rbac.ManageUserGroups)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "this group carries a role; managing it requires the MANAGE_USER_GROUPS permission")
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
