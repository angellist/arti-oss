// Package rbac defines arti's role/permission vocabulary: the opaque permission
// keys and built-in role names. It is intentionally dependency-free (a leaf
// package) so every other package can import the constants without risking an
// import cycle. Storage and resolution live in pgstore; the HTTP surface lives
// in the rbac HTTP service (separate file).
package rbac

// Permission is an opaque capability key. The string is meaningless to the
// database — it's translated to a concrete check in code via HasPermission.
type Permission string

const (
	// ManageRoles: read/write all roles (their permission sets) and
	// assign/unassign roles to people and groups.
	ManageRoles Permission = "MANAGE_ROLES"
	// ManageUserGroups: read/write ALL user groups (the cross-owner admin view).
	ManageUserGroups Permission = "MANAGE_USER_GROUPS"
	// ManageArtifacts: read/write ALL artifacts — the admin bypass of
	// per-artifact access control.
	ManageArtifacts Permission = "MANAGE_ARTIFACTS"
	// ManageSkills: write (create/append/archive) `kind:skill` artifacts, and
	// see `scope:app:couch` artifacts in List/Search (so an app's service
	// identity can build its skill catalog). Narrower than ManageArtifacts:
	// it does NOT bypass per-artifact access control for non-skill artifacts.
	// Granted to app service identities (e.g. couch's service@) that manage
	// skills on the app's behalf.
	ManageSkills Permission = "MANAGE_SKILLS"
	// ManageAPIKeys: read/list and revoke ALL users' API keys — the
	// cross-owner admin view.
	ManageAPIKeys Permission = "MANAGE_API_KEYS"
	// UseArtifacts: baseline ability to read/write one's own or otherwise
	// visible artifacts. Held by every authenticated user via the USER role.
	UseArtifacts Permission = "USE_ARTIFACTS"
)

// AllPermissions is the canonical list of known permission keys. Used to
// validate role definitions (reject unknown keys) and to drive the admin UI's
// permission checkboxes.
var AllPermissions = []Permission{
	ManageRoles,
	ManageUserGroups,
	ManageArtifacts,
	ManageSkills,
	ManageAPIKeys,
	UseArtifacts,
}

// IsKnownPermission reports whether p is one of the defined permission keys.
func IsKnownPermission(p string) bool {
	for _, k := range AllPermissions {
		if string(k) == p {
			return true
		}
	}
	return false
}

// Built-in role names. These two roles always exist (seeded by migration) and
// cannot be deleted; their permission sets are editable.
const (
	// RoleAdmin holds every permission. Bootstrapped to ARTI_ADMIN_EMAILS.
	RoleAdmin = "ADMIN"
	// RoleUser is the implicit baseline every authenticated caller has.
	RoleUser = "USER"
)

// IsBuiltinRole reports whether name is a built-in (non-deletable) role.
func IsBuiltinRole(name string) bool {
	return name == RoleAdmin || name == RoleUser
}

// PrincipalType enumerates what a role can be assigned to.
const (
	PrincipalUser  = "user"  // principal_id is a lowercased email
	PrincipalGroup = "group" // principal_id is a user-group name
)
