export type ArtifactType = "TEXT" | "PACKAGE" | "ATTACHMENT" | "APP" | "MAP";

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
  // written_via / written_via_name: the credential that wrote this version,
  // and the owner's name for it. `creator` cannot answer this — an API key
  // authenticates as its owner, so an agent's write looks like a person's.
  // null on documents written before attribution shipped, which means
  // "unknown", not "a person in a browser".
  written_via: string | null;
  written_via_name: string | null;
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
  // comments_enabled — the document's owner (its first version's creator,
  // unless transferred) or an admin. Present only on single-artifact viewer
  // responses.
  can_manage_comments?: boolean;
  // owner: who the DOCUMENT belongs to, and the address to ask for access.
  // Distinct from `creator`, which names whoever pushed THIS version. Present
  // only on single-artifact viewer responses.
  owner?: string;
  // can_edit_metadata: the server's own verdict on whether this caller may edit
  // title/description/labels/scopes. Prefer it over re-deriving from `creator`,
  // which names whoever pushed this version — on a transferred document the two
  // answer for different people.
  can_edit_metadata?: boolean;
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
  // View counts are slug-scoped: every version of a named slug contributes.
  // undefined means the optional catalog count query was unavailable.
  view_count?: number;
  view_count_30d?: number;
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
// Block is one entry of the admin block list: a pattern that takes every
// document it matches away from every reader, admins included. The document
// itself is untouched, so removing the entry restores it.
export interface Block {
  pattern: string;
  reason: string;
  created_by: string;
  created_at: string;
}

// BlockedDoc is what a block currently hides, as the admin review surface
// sees it: identity and provenance, never content. Fetching a body is a
// separate call, so nothing that renders a list can render a document.
export interface BlockedDoc {
  artifact_id: string;
  named_slug: string | null;
  version: number | null;
  title: string;
  creator: string;
  artifact_type: string;
  content_type: string;
  size_bytes: number | null;
  created_at: string;
}

// BlockedDocDetail adds the fields the single-document review page shows.
export interface BlockedDocDetail extends BlockedDoc {
  description: string | null;
  labels: string[];
  scopes: string[];
  archived: boolean;
  body_readable: boolean;
  max_bytes: number;
}

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

// NotificationCategory is one Slack message the signed-in person can choose to
// receive or not. The choice is theirs alone and affects nobody else.
export interface NotificationCategory {
  key: string;
  label: string;
  description: string;
  enabled: boolean;
}

// NotificationSettings is the signed-in person's own choices, plus the two
// facts that explain why a choice might do nothing: slack_configured is false
// when the deployment has no bot token, and deployment_enabled is false when an
// admin has stopped every notification for everyone.
export interface NotificationSettings {
  categories: NotificationCategory[];
  slack_configured: boolean;
  deployment_enabled: boolean;
  can_manage_deployment: boolean;
}

// DeploymentNotificationSwitch is the admin-only master.
export interface DeploymentNotificationSwitch {
  key: string;
  label: string;
  enabled: boolean;
  slack_configured: boolean;
}

// CredentialSource is one place a credential has been used from, aggregated
// from the daily usage rows. `cred` is the credential reference stamped on the
// documents it wrote (artifacts.written_via), which is how a source row and a
// document line up. owner_email is present only on the admin view.
export interface CredentialSource {
  cred: string;
  owner_email?: string;
  ip: string;
  user_agent: string;
  reads: number;
  writes: number;
  first_seen: string | null;
  last_seen: string | null;
}

// CredentialUsage answers "where has each credential been used, and what has
// it written". docs maps a credential reference to how many live documents it
// wrote — the count that makes a shared key visible.
export interface CredentialUsage {
  sources: CredentialSource[];
  docs: Record<string, number>;
  days: number;
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

// DenialInfo is what the server will say about an artifact the caller cannot
// read. Every ordinary read answers 404 whether the document is missing or
// merely closed, so this is the only response that distinguishes the two —
// and it carries just enough to end the reader's search: the document's name,
// its slug, and the address that can grant access.
//
// `owner` is the slug's owner, which is the authority ACL changes answer to.
// It is not `ArtifactInfo.creator`, which versioning reassigns to whoever
// pushed the latest version.
export interface DenialInfo {
  named_slug: string | null;
  version: number | null;
  title: string;
  owner: string;
}
