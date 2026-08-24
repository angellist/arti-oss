export type ArtifactType = "TEXT" | "PACKAGE" | "ATTACHMENT" | "APP";

export interface ArtifactInfo {
  artifact_id: string;
  artifact_type: ArtifactType;
  named_slug: string | null;
  version: number | null;
  title: string;
  description: string | null;
  content_type: string;
  size_bytes: number | null;
  sha256: string | null;
  creator: string;
  scopes: string[];
  labels: string[];
  allowed_access: string[];
  // allowed_write: tokens allowed to write. null = write follows read
  // (back-compat default); [] = creator-only writes; [tokens] = those + creator.
  // The null-vs-[] distinction is load-bearing — the server emits it without
  // omitempty precisely so an empty list isn't confused with mirror mode.
  allowed_write: string[] | null;
  // can_write: whether the requesting caller may version/append/edit this
  // artifact — the server's effective checkWriteAccess result. Present only on
  // single-artifact viewer responses (Get/GetBySlug); absent on list/search.
  can_write?: boolean;
  // comments_enabled: the per-DOCUMENT comment switch. false means the owner
  // turned commenting off — no comment controls anywhere, and the API reports
  // no threads and refuses new ones. Optional in this type only because
  // fixtures and older cached payloads may predate the field; treat
  // `undefined` as ON (`!== false`), never as OFF, or every artifact rendered
  // from a stale payload would silently lose its comments.
  comments_enabled?: boolean;
  // can_manage_comments: whether the requesting caller may flip
  // comments_enabled — the slug's owner (earliest version's creator) or an
  // admin. Present only on single-artifact viewer responses.
  can_manage_comments?: boolean;
  // Whether the caller may mint an external share link. Server-computed from
  // the slug's immutable owner; never re-derive it from `creator`, which
  // versioning reassigns to whoever pushed the latest version.
  can_share?: boolean;
  metadata: Record<string, unknown>;
  created_at: string;
  modified_at: string;
  deleted_at: string | null;
  url: string;
  // comment_count / open_thread_count: discussion on THIS version (comments
  // key off artifact_id, which is per-version). Present on catalog list and
  // search responses — where the server counts the whole page in one query —
  // and absent elsewhere. undefined means "not computed", which the catalog
  // renders as "—"; 0 means "no comments".
  comment_count?: number;
  open_thread_count?: number;
  score?: number;
  highlights?: Record<string, string[]>;
}

export interface ArtifactListResponse {
  artifacts: ArtifactInfo[];
  total: number;
}

export interface PackageManifest {
  entries: Array<{ path: string; size: number; sha256?: string; content_type: string }>;
  entry_point?: string;
}

export interface ScopeTypeCount {
  type: string;
  count: number;
}

export interface ScopeCount {
  scope: string;
  count: number;
}

export interface LabelCount {
  label: string;
  count: number;
}

export interface ContentTypeCount {
  content_type: string;
  count: number;
}

export interface AggregatesResponse {
  scope_types: ScopeTypeCount[];
  scopes: ScopeCount[];
  labels: LabelCount[];
  content_types: ContentTypeCount[];
}

// BrowseFacet is one of the Browse page's four facets — the value each maps
// to on the wire (?facet=…) and in row-link filter tokens.
export type BrowseFacet = "type" | "label" | "scope" | "content_type" | "owner";

export interface BrowseValueCount {
  value: string;
  count: number;
}

export interface BrowseAggregatesResponse {
  values: BrowseValueCount[];
  total: number;
}

// Group is a named collection of member emails. Granting its `token`
// (`group:<name>`) in an artifact's allowed_access lets the group's current
// members read it. Served by GET /api/groups.
export interface Group {
  name: string;
  display_name: string;
  members: string[];
  member_count: number;
  token: string;
  created_by: string;
  created_at: string;
  modified_at: string;
}

// IdpGroup is a grantable IdP (SSO) group captured at login. `token` is the
// `idp:<name>` string to drop into allowed_access/allowed_write. No roster is
// exposed — only the name and how many users currently carry it. Served by
// GET /api/idp-groups.
export interface IdpGroup {
  name: string;
  token: string;
  member_count: number;
}

// Me is the authenticated caller's identity, served by GET /api/me.
// Used to gate archive / delete UI controls and display the user's name.
// `permissions` is the caller's effective RBAC permission keys; prefer it
// over `is_admin` for capability gating. `is_admin` (holds the ADMIN role)
// is kept for back-compat.
export interface Me {
  email: string;
  name: string;
  picture?: string;
  is_admin: boolean;
  permissions?: string[];
  // Whether the server has external share links switched on
  // (ARTI_SHARE_ENABLED). When false the Share dialog offers only the
  // copy-this-URL half, because the mint endpoint is not mounted at all.
  share_links_enabled?: boolean;
}

// ShareLink is one external timed link as its owner sees it. There is
// deliberately no token field: the plaintext is returned exactly once, at
// mint, and the prefix identifies a row without opening it.
export interface ShareLink {
  id: string;
  token_prefix: string;
  scope: "version" | "slug";
  note: string;
  created_by: string;
  created_at: string;
  expires_at: string;
  revoked_at?: string;
  open_count: number;
  last_opened_at?: string;
}

// MintedShare is the one and only sighting of a share URL.
export interface MintedShare {
  id: string;
  url: string;
  token_prefix: string;
  expires_at: string;
}

// ShareOpen is one recorded read of a shared document. `ip` is
// header-derived and therefore forgeable; `peer_addr` is the socket address.
// A disagreement between them means the forwarded header was set by the
// client, which is why both are shown.
export interface ShareOpen {
  at: string;
  ip: string;
  peer_addr: string;
  user_agent: string;
}

// Role is a named set of opaque permission keys. Served by GET /api/roles.
export interface Role {
  name: string;
  description: string;
  permissions: string[];
  builtin: boolean;
  created_at: string;
  modified_at: string;
}

// RoleAssignment binds a principal (a user email or a group name) to a role.
export interface RoleAssignment {
  principal_type: "user" | "group";
  principal_id: string;
  role_name: string;
  created_by: string;
}

// ApiKey is a self-serve, scope-generic API key entry. key_prefix is the
// display-safe prefix (e.g. "arti_upload_abc12…"). The plaintext key is never
// stored — it is returned only once at creation time in CreatedApiKey.
export interface ApiKey {
  id: string;
  name: string;
  key_prefix: string;
  owner_email: string;
  scopes: string[];
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
}

// CreatedApiKey extends ApiKey with the one-time plaintext key (shown once,
// treat like a password — arti does not store it).
export interface CreatedApiKey extends ApiKey {
  key: string;
}

// HeldRole is one role a person holds in the per-user lookup, with where it
// came from: "baseline" (USER), "direct", or "group:<name>".
export interface HeldRole {
  name: string;
  permissions: string[];
  source: string;
}

// UserAccess is the per-user lookup result (GET /api/role-lookup).
export interface UserAccess {
  email: string;
  roles: HeldRole[];
  effective_permissions: string[];
}

// ─── users roster ──────────────────────────────────────────────────────
//
// One row of the Users page. arti has no account provisioning, so most of a
// roster row is DERIVED from wherever an email was recorded — see
// pgstore/people.go's knownPrincipalsCTE, which this shares with the
// access-editor typeahead. `registered` marks the exception: a principal
// somebody recorded deliberately.

export interface RosterRole {
  name: string;
  // "baseline" (the implicit USER role) | "direct" | "group:<name>"
  source: string;
}

export interface RosterUser {
  email: string;
  kind: "human" | "service";
  note: string;
  roles: RosterRole[];
  groups: string[];
  idp_groups: string[];
  // The IdP snapshot has aged past ARTI_IDP_GROUPS_MAX_AGE (or the bound is
  // disabled), so idp_groups currently grant nothing. Never render a stale
  // group as live access.
  idp_stale: boolean;
  // null = no login recorded, which is NOT "never signed in": logins were only
  // recorded from migration 0020 (2026-07-27) onward.
  last_seen_at: string | null;
  registered: boolean;
  added_by: string;
  added_at: string | null;
}

export interface RosterResponse {
  users: RosterUser[];
  // The tables the roster read, so a missing principal class is diagnosable
  // from the response rather than from the source.
  sources: string[];
}
