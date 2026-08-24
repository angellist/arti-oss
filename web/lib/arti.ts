import { ArtifactInfo, ArtifactListResponse, AggregatesResponse, BrowseAggregatesResponse, BrowseFacet, PackageManifest, ArtifactType, Group, IdpGroup, Me, Role, RoleAssignment, RosterResponse, RosterUser, UserAccess, ApiKey, CreatedApiKey, ShareLink, MintedShare, ShareOpen } from "./types";

// hasPerm reports whether `me` holds an RBAC permission key. Prefer this over
// the bare is_admin flag for capability gating so a non-ADMIN role carrying a
// permission (e.g. MANAGE_ARTIFACTS) still enables the matching UI.
export function hasPerm(me: Me | null | undefined, perm: string): boolean {
  return !!me?.permissions?.includes(perm);
}

// sameEmail compares two emails case-insensitively, matching the backend's
// creator checks (strings.EqualFold) so UI controls aren't disabled on a mere
// casing difference.
export function sameEmail(a?: string | null, b?: string | null): boolean {
  return !!a && !!b && a.toLowerCase() === b.toLowerCase();
}

// Browser fetches go through the same-origin reverse proxy on arti-server.
// SSR fetches use ARTI_API_URL (server-only, points at the Go binary).
const SERVER_BASE = process.env.ARTI_API_URL ?? "http://localhost:8095";

function baseFor(isServer: boolean) {
  return isServer ? SERVER_BASE : "";
}

export class ArtiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

// Read a failed response into an ArtiError.
//
// The server's error envelope is `{detail, code}` on every endpoint, and an
// ArtiError's `message` is rendered directly to the user by a dozen components
// (`setErr(e.message)`). So the body must be unwrapped to its `detail` — a bare
// `await resp.text()` puts `{"detail":"…","code":"Conflict"}` on screen
// verbatim, which is what most call sites here used to do.
//
// One helper for every call site, rather than the parse being inlined in the
// few that bothered: sharing it is the only thing that keeps the 17 throw sites
// from drifting apart again.
async function errorFrom(resp: Response): Promise<ArtiError> {
  let detail = "";
  try {
    const body = (await resp.text()).trim();
    if (body) detail = detailFromBody(body);
  } catch {
    // Body already consumed or the stream failed; fall back to the status line.
  }
  // `HTTP <status>` last, and it is NOT redundant: HTTP/2 and HTTP/3 dropped the
  // reason phrase, so browsers report an EMPTY statusText for every response
  // over them — which is how arti is actually served in production. Without this
  // the message would be blank exactly where it matters, and the components that
  // do setErr(e.message) would render an empty error. statusText is still
  // preferred when present (HTTP/1.1, and the Node fetch used for SSR).
  return new ArtiError(resp.status, detail || resp.statusText || `HTTP ${resp.status}`);
}

// Longest body we are willing to show verbatim. Past this it is a document, not
// a message, and belongs in devtools rather than a toast.
const MAX_INLINE_BODY = 200;

function detailFromBody(body: string): string {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    // Not JSON, so this came from infrastructure we do not control: a gateway
    // error page, or a plain-text message. Echo it only when it reads as prose.
    //
    // A length cap alone is not enough — a DEFAULT NGINX ERROR PAGE IS ~142
    // CHARACTERS, so it slips under any sane cutoff and lands raw markup in a
    // toast. Worse, it would be a regression: the call sites that used to go
    // through resp.json() got statusText for an HTML body, because parsing
    // threw. So reject anything opening like markup or a broken data structure
    // and let the status line speak instead.
    if (/^[<{[]/.test(body)) return "";
    return body.length <= MAX_INLINE_BODY ? body : "";
  }
  // It parsed, so it is machine output. Only our envelope's `detail` is written
  // for a human — a proxy's own `{message}`, a bare scalar, or an array must
  // never be pasted on screen, which is the whole point of this helper. The
  // narrow type check also keeps `null` and a non-string `detail` from reaching
  // the UI as the literal "null" or "[object Object]".
  if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
    const d = (parsed as { detail?: unknown }).detail;
    if (typeof d === "string") return d.trim();
  }
  return "";
}

async function http<T>(
  path: string,
  init: RequestInit = {},
  cookie?: string,
): Promise<T> {
  const isServer = typeof window === "undefined";
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (cookie && isServer) headers.set("Cookie", cookie);
  const resp = await fetch(baseFor(isServer) + path, {
    ...init,
    headers,
    cache: "no-store",
    credentials: "include",
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  // 204 No Content (e.g. DELETE) has no body; resp.json() would throw.
  if (resp.status === 204) {
    return undefined as T;
  }
  return resp.json() as Promise<T>;
}

export type SortField =
  | "title"
  | "type"
  | "slug"
  | "version"
  | "creator"
  | "scope"
  | "created"
  | "archived";
export type SortDir = "asc" | "desc";

export interface ListParams {
  type?: string;
  scope?: string;
  labels?: string[];
  creator?: string;
  slug?: string;
  limit?: number;
  offset?: number;
  include_archived?: boolean;
  // When true, the server does NOT collapse each slug to its latest version —
  // every matching version is returned (`?all_versions=true`). Default (omitted)
  // keeps the latest-per-slug collapse.
  all_versions?: boolean;
  archived?: "only" | "include";
  q?: string;
  order_by?: SortField;
  order_dir?: SortDir;
}

export function buildListQS(params: ListParams): URLSearchParams {
  const qs = new URLSearchParams();
  if (params.type) qs.set("type", params.type);
  if (params.creator) qs.set("creator", params.creator);
  if (params.scope) qs.set("scope", params.scope);
  if (params.slug) qs.set("slug", params.slug);
  if (params.labels) params.labels.forEach((l) => qs.append("label", l));
  if (params.limit) qs.set("limit", String(params.limit));
  if (params.offset) qs.set("offset", String(params.offset));
  if (params.archived) qs.set("archived", params.archived);
  if (params.include_archived) qs.set("include_archived", "true");
  if (params.all_versions) qs.set("all_versions", "true");
  if (params.q) qs.set("q", params.q);
  if (params.order_by) qs.set("order_by", params.order_by);
  if (params.order_dir) qs.set("order_dir", params.order_dir);
  return qs;
}

export async function listArtifacts(
  params: ListParams = {},
  cookie?: string,
): Promise<ArtifactListResponse> {
  const qs = buildListQS(params);
  // The server accepts substring search on /search; bare list on /api/artifacts.
  // Both honor every filter, but only /search runs the ILIKE.
  const path = params.q ? `/api/artifacts/search?${qs}` : `/api/artifacts?${qs}`;
  return http<ArtifactListResponse>(path, undefined, cookie);
}

export async function getMeta(id: string, cookie?: string) {
  return http<ArtifactInfo>(`/api/artifacts/${id}/meta`, undefined, cookie);
}

export async function getBySlug(slug: string, version?: number, cookie?: string) {
  const qs = version ? `?version=${version}` : "";
  return http<ArtifactInfo>(
    `/api/artifacts/by-slug/${encodeURIComponent(slug)}${qs}`,
    undefined,
    cookie,
  );
}

export async function listVersions(slug: string, cookie?: string) {
  return http<{ versions: ArtifactInfo[] }>(
    `/api/artifacts/by-slug/${encodeURIComponent(slug)}/versions`,
    undefined,
    cookie,
  );
}

export async function listPackageFiles(id: string, cookie?: string) {
  return http<PackageManifest>(`/api/artifacts/${id}/files`, undefined, cookie);
}

export async function fetchContent(
  id: string,
  cookie?: string,
): Promise<{ body: string; contentType: string }> {
  const isServer = typeof window === "undefined";
  const headers: HeadersInit = {};
  if (cookie && isServer) (headers as Record<string, string>).Cookie = cookie;
  const resp = await fetch(baseFor(isServer) + `/api/artifacts/${id}`, {
    headers,
    cache: "no-store",
    credentials: "include",
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return { body: await resp.text(), contentType: resp.headers.get("content-type") ?? "" };
}

// encodeFilePath percent-encodes each segment of a package-internal file
// path (keeping the slashes) so spaces and special chars (#, ?, …) in
// filenames survive in the URL. The server percent-decodes the wildcard
// before the zip lookup, so "report (1).pdf" round-trips correctly.
export function encodeFilePath(path: string): string {
  return path.split("/").map(encodeURIComponent).join("/");
}

// fetchPackageFile pulls one file out of a PACKAGE artifact. Used by
// FullPageView when the user asked to fullscreen a package-internal
// markdown (or other text) file via `?v=full&file=…`.
export async function fetchPackageFile(
  id: string,
  path: string,
  cookie?: string,
): Promise<{ body: string; contentType: string }> {
  const isServer = typeof window === "undefined";
  const headers: HeadersInit = {};
  if (cookie && isServer) (headers as Record<string, string>).Cookie = cookie;
  const enc = encodeFilePath(path);
  const resp = await fetch(baseFor(isServer) + `/api/artifacts/${id}/files/${enc}`, {
    headers,
    cache: "no-store",
    credentials: "include",
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return { body: await resp.text(), contentType: resp.headers.get("content-type") ?? "" };
}

export async function getAggregates(cookie?: string): Promise<AggregatesResponse> {
  return http<AggregatesResponse>("/api/artifacts/aggregates", undefined, cookie);
}

export interface BrowseAggregatesParams {
  facet: BrowseFacet;
  sort?: "count" | "name";
  dir?: SortDir;
  page?: number;
  page_size?: number;
}

export async function getBrowseAggregates(
  params: BrowseAggregatesParams,
  cookie?: string,
): Promise<BrowseAggregatesResponse> {
  const qs = new URLSearchParams();
  qs.set("facet", params.facet);
  if (params.sort) qs.set("sort", params.sort);
  if (params.dir) qs.set("dir", params.dir);
  if (params.page) qs.set("page", String(params.page));
  if (params.page_size) qs.set("page_size", String(params.page_size));
  return http<BrowseAggregatesResponse>(`/api/artifacts/aggregates/browse?${qs}`, undefined, cookie);
}

export async function getMe(cookie?: string): Promise<import("./types").Me> {
  return http<import("./types").Me>("/api/me", undefined, cookie);
}

// archiveArtifact soft-deletes — sets deleted_at, hides from default
// catalog. Reversible via unarchiveArtifact.
export async function archiveArtifact(id: string): Promise<void> {
  const resp = await fetch(`/api/artifacts/${id}`, {
    method: "DELETE",
    credentials: "include",
  });
  if (!resp.ok && resp.status !== 204) {
    throw await errorFrom(resp);
  }
}

// unarchiveArtifact clears deleted_at on an archived artifact.
// Creator-or-admin only.
export async function unarchiveArtifact(id: string): Promise<void> {
  const resp = await fetch(`/api/artifacts/${id}/unarchive`, {
    method: "POST",
    credentials: "include",
  });
  if (!resp.ok && resp.status !== 204) {
    throw await errorFrom(resp);
  }
}

// ─── upload ──────────────────────────────────────────────────────────

export interface CreateArtifactInput {
  title: string;
  content_type: string;
  artifact_type: ArtifactType;
  content_base64: string;
  named_slug?: string;
  scopes?: string[];
  labels?: string[];
  // Access at creation time. Omit to inherit from the slug's prior version if
  // there is one, else the server default (everyone authenticated). An empty
  // array is NOT the same as omitting: it means creator-only.
  allowed_access?: string[];
  // Tokens allowed to write (a subset of allowed_access; the server unions it
  // in). Omit for mirror mode, where write follows read; an empty array means
  // creator-only writes.
  allowed_write?: string[];
  // Assert this is a brand-new document: the server rejects the POST with 409
  // `slug-exists` if the slug already has a non-deleted version, instead of
  // silently appending v(N+1) to someone else's slug.
  ensure_new?: boolean;
}

// createArtifact POSTs a new artifact (or a new version of an existing
// slug). Mirrors the CLI's JSON-with-base64 upload. Returns the created
// artifact's metadata (including its url / version).
export async function createArtifact(input: CreateArtifactInput): Promise<ArtifactInfo> {
  const resp = await fetch(`/api/artifacts`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

export interface SuggestMetadataInput {
  filename: string;
  content_type: string;
  artifact_type: ArtifactType;
  sample: string;
}
export interface SuggestMetadataResult {
  title: string;
  slug: string;
  labels: string[];
}

// suggestMetadata asks the server (haiku, for text) to propose a
// title/slug/labels for an about-to-be-uploaded file. Always resolves with
// a usable suggestion — the server degrades to filename-derived values when
// the LLM is unavailable.
export async function suggestMetadata(
  input: SuggestMetadataInput,
): Promise<SuggestMetadataResult> {
  return http<SuggestMetadataResult>(`/api/artifacts/suggest-metadata`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

// latestVersionForSlug returns the latest existing version for a slug, or
// null if the slug is free. The upload modal uses it to warn "uploads as
// v{N+1}" before the user commits.
//
// Best-effort: getBySlug is access-filtered, so the number is the latest
// version the caller can READ, while the server assigns MAX(version)+1 over
// ALL non-deleted versions on create. The hint can therefore undercount (or
// 404→"new") when newer versions exist that the caller can't see. The upload
// is still versioned correctly server-side; this is only the advisory text.
// TODO(arti#56): if precise, expose a creator-safe max-version probe rather
// than leaking exact version counts across access boundaries.
export async function latestVersionForSlug(slug: string): Promise<number | null> {
  try {
    const info = await getBySlug(slug);
    return info.version ?? null;
  } catch (e) {
    if (e instanceof ArtiError && e.status === 404) return null;
    throw e;
  }
}

// ─── comments ────────────────────────────────────────────────────────

export interface CommentDTO {
  id: string;
  author: string;
  author_name: string;
  author_picture?: string;
  body: string;
  created_at: string;
  edited_at?: string;
}
export interface ThreadDTO {
  id: string;
  anchor: { type: "doc" | "text" | "pin"; [k: string]: unknown };
  status: "open" | "resolved";
  created_by: string;
  created_at: string;
  resolved_by?: string;
  comments: CommentDTO[];
}

export async function listComments(artifactId: string): Promise<{ threads: ThreadDTO[] }> {
  return http<{ threads: ThreadDTO[] }>(`/api/artifacts/${artifactId}/comments`);
}

export async function createThread(
  artifactId: string,
  anchor: ThreadDTO["anchor"],
  body: string,
): Promise<ThreadDTO> {
  return http<ThreadDTO>(`/api/artifacts/${artifactId}/comments`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ anchor, body }),
  });
}

export async function replyComment(threadId: string, body: string): Promise<CommentDTO> {
  return http<CommentDTO>(`/api/comments/${threadId}/replies`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ body }),
  });
}

// resolve/reopen return 204 (no body), so use a raw fetch.
async function postVoid(path: string): Promise<void> {
  const resp = await fetch(path, { method: "POST", credentials: "include" });
  if (!resp.ok && resp.status !== 204) throw await errorFrom(resp);
}
export const resolveThread = (threadId: string) => postVoid(`/api/comments/${threadId}/resolve`);
export const reopenThread = (threadId: string) => postVoid(`/api/comments/${threadId}/reopen`);

async function del(path: string): Promise<void> {
  const resp = await fetch(path, { method: "DELETE", credentials: "include" });
  if (!resp.ok && resp.status !== 204) throw await errorFrom(resp);
}
export const deleteComment = (threadId: string, commentId: string) =>
  del(`/api/comments/${threadId}/comments/${commentId}`);

export async function editComment(threadId: string, commentId: string, body: string): Promise<CommentDTO> {
  return http<CommentDTO>(`/api/comments/${threadId}/comments/${commentId}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ body }),
  });
}

// hardDeleteArtifact permanently removes the row. Admin-only. Not
// reversible.
export async function hardDeleteArtifact(id: string): Promise<void> {
  const resp = await fetch(`/api/artifacts/${id}?hard=true`, {
    method: "DELETE",
    credentials: "include",
  });
  if (!resp.ok && resp.status !== 204) {
    throw await errorFrom(resp);
  }
}

// updateArtifactTitle PATCHes a new title onto an artifact. Creator or
// admin only. Returns the updated metadata.
export async function updateArtifactTitle(id: string, title: string): Promise<ArtifactInfo> {
  const resp = await fetch(`/api/artifacts/${id}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ title }),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

// updateArtifactLabels PATCHes a new label set onto an artifact. The
// server trims/dedupes; pass whatever the UI has. Creator or admin only.
export async function updateArtifactLabels(id: string, labels: string[]): Promise<ArtifactInfo> {
  const resp = await fetch(`/api/artifacts/${id}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ labels }),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

// updateArtifactScopes PATCHes a new scope set onto an artifact. The
// server trims/dedupes; pass whatever the UI has. Creator or admin only.
export async function updateArtifactScopes(id: string, scopes: string[]): Promise<ArtifactInfo> {
  const resp = await fetch(`/api/artifacts/${id}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ scopes }),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

// updateArtifactAccess PATCHes a new allowed_access (and optional allowed_write)
// set onto ONE artifact version. Per-version — siblings keep their existing
// access. Creator or admin only. When `write` is provided the server unions it
// into allowed_access (write ⊆ read) and treats [] as creator-only writes;
// omit `write` to leave the write list unchanged.
export async function updateArtifactAccess(
  id: string,
  access: string[],
  write?: string[],
): Promise<ArtifactInfo> {
  const body: { allowed_access: string[]; allowed_write?: string[] } = { allowed_access: access };
  if (write !== undefined) {
    body.allowed_write = write;
  }
  const resp = await fetch(`/api/artifacts/${id}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

// updateArtifactCommentsEnabled PATCHes the per-doc comment switch. Unlike the
// access editor this is per-DOCUMENT: the server writes every version of the
// slug, and accepts it only from the artifact's owner (the earliest version's
// creator) or an admin — a delegated writer gets a 403.
export async function updateArtifactCommentsEnabled(
  id: string,
  enabled: boolean,
): Promise<ArtifactInfo> {
  const resp = await fetch(`/api/artifacts/${id}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ comments_enabled: enabled }),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

// ─── user groups ───────────────────────────────────────────────────────

// listGroups returns all user groups. Readable by any authenticated caller —
// it feeds both the access-editor typeahead and the admin page.
export async function listGroups(cookie?: string): Promise<Group[]> {
  const { groups } = await http<{ groups: Group[] }>("/api/groups", undefined, cookie);
  return groups;
}

// listIdpGroups returns the IdP (SSO) groups captured at login, for the
// access-editor typeahead. Readable by any authenticated caller (names +
// counts only, never rosters). Served by GET /api/idp-groups.
export async function listIdpGroups(cookie?: string): Promise<IdpGroup[]> {
  const { idp_groups } = await http<{ idp_groups: IdpGroup[] }>("/api/idp-groups", undefined, cookie);
  return idp_groups ?? [];
}

// searchPeople returns emails arti already knows (login history, artifact
// creators, group rosters, role assignments) matching a partial address, for
// the access + membership typeaheads. Served by GET /api/people.
//
// Below MIN_PEOPLE_QUERY characters the server answers with an empty list by
// design — there is no query that enumerates the workspace — so the caller
// short-circuits rather than spending a request to be told nothing.
export const MIN_PEOPLE_QUERY = 2;

export async function searchPeople(q: string, cookie?: string): Promise<string[]> {
  if (q.trim().length < MIN_PEOPLE_QUERY) return [];
  const { people } = await http<{ people: { email: string }[] }>(
    `/api/people?q=${encodeURIComponent(q.trim())}`,
    undefined,
    cookie,
  );
  return (people ?? []).map((p) => p.email);
}

// createGroup creates a new group. Admin only (server returns 404 otherwise).
export async function createGroup(input: {
  name: string;
  display_name: string;
  members: string[];
}): Promise<Group> {
  const resp = await fetch(`/api/groups`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

// updateGroup PATCHes a group's display name and/or members (name immutable).
// Omitted fields are left unchanged. Admin only.
export async function updateGroup(
  name: string,
  patch: { display_name?: string; members?: string[] },
): Promise<Group> {
  const resp = await fetch(`/api/groups/${encodeURIComponent(name)}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

// deleteGroup removes a group. Dangling `group:<name>` tokens in any artifact
// resolve to no members (fail-closed). Admin only. Returns 204 (no body).
export async function deleteGroup(name: string): Promise<void> {
  const resp = await fetch(`/api/groups/${encodeURIComponent(name)}`, {
    method: "DELETE",
    credentials: "include",
  });
  if (!resp.ok && resp.status !== 204) {
    throw await errorFrom(resp);
  }
}

// ─── roles / permissions (admin: MANAGE_ROLES) ──────────────────────────

async function mutate(path: string, method: string, body?: unknown): Promise<void> {
  const resp = await fetch(path, {
    method,
    credentials: "include",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!resp.ok && resp.status !== 204) {
    throw await errorFrom(resp);
  }
}

export async function listRoles(cookie?: string): Promise<Role[]> {
  const { roles } = await http<{ roles: Role[] }>("/api/roles", undefined, cookie);
  return roles;
}

export async function listPermissions(cookie?: string): Promise<string[]> {
  const { permissions } = await http<{ permissions: string[] }>("/api/permissions", undefined, cookie);
  return permissions;
}

export async function createRole(input: { name: string; description: string; permissions: string[] }): Promise<void> {
  return mutate("/api/roles", "POST", input);
}

export async function updateRole(
  name: string,
  patch: { description?: string; permissions?: string[] },
): Promise<void> {
  return mutate(`/api/roles/${encodeURIComponent(name)}`, "PATCH", patch);
}

export async function deleteRole(name: string): Promise<void> {
  return mutate(`/api/roles/${encodeURIComponent(name)}`, "DELETE");
}

export async function listAssignments(role: string, cookie?: string): Promise<RoleAssignment[]> {
  const qs = role ? `?role=${encodeURIComponent(role)}` : "";
  const { assignments } = await http<{ assignments: RoleAssignment[] }>(`/api/role-assignments${qs}`, undefined, cookie);
  return assignments;
}

export async function assignRole(input: {
  principal_type: "user" | "group";
  principal_id: string;
  role_name: string;
}): Promise<void> {
  return mutate("/api/role-assignments", "POST", input);
}

export async function unassignRole(input: {
  principal_type: "user" | "group";
  principal_id: string;
  role_name: string;
}): Promise<void> {
  // Params go in the query string (not a body) — DELETE bodies are unreliable.
  const qs = new URLSearchParams(input);
  return mutate(`/api/role-assignments?${qs}`, "DELETE");
}

// ─── users roster (admin: MANAGE_ROLES) ─────────────────────────────────

// listUsers returns every principal arti knows. MANAGE_ROLES only — the server
// answers 404 otherwise, so the surface isn't discoverable. This is the one
// endpoint allowed to enumerate principals; /api/people (the typeahead) refuses
// to, by design.
export async function listUsers(cookie?: string): Promise<RosterResponse> {
  return http<RosterResponse>("/api/users", undefined, cookie);
}

// addUser records a principal so it appears on the roster before it has done
// anything. It grants NOTHING: roles are assigned separately, and this exists
// because a role assignment cannot express baseline privilege (the store rejects
// assigning the USER role, which everyone holds implicitly).
export async function addUser(input: {
  email: string;
  kind?: "human" | "service";
  note?: string;
}): Promise<RosterUser> {
  const resp = await fetch(`/api/users`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

export async function lookupUserAccess(email: string): Promise<UserAccess> {
  return http<UserAccess>(`/api/role-lookup?email=${encodeURIComponent(email)}`);
}

// ─── API keys ──────────────────────────────────────────────────────────────

// listApiKeys returns the caller's own keys. Pass all=true (admin only) to
// fetch every key across all owners.
export function listApiKeys(all = false, cookie?: string): Promise<ApiKey[]> {
  return http<ApiKey[]>(`/api/keys${all ? "?all=true" : ""}`, {}, cookie);
}

// createApiKey mints a new API key. The returned CreatedApiKey.key is the
// plaintext token — it is shown once and never retrievable again.
export function createApiKey(
  name: string,
  scopes: string[],
  ttlDays: number,
): Promise<CreatedApiKey> {
  return http<CreatedApiKey>("/api/keys", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, scopes, ttl_days: ttlDays }),
  });
}

// revokeApiKey soft-deletes a key by id (owner or admin). Returns void on 204.
export function revokeApiKey(id: string): Promise<void> {
  return http<void>(`/api/keys/${encodeURIComponent(id)}`, { method: "DELETE" });
}

// ─── external share links ────────────────────────────────────────────
// The mint response carries the only copy of the URL that will ever exist;
// everything else in this group is deliberately token-free.

export async function mintShare(
  artifactID: string,
  scope: "version" | "slug",
  ttl: string,
  note: string,
): Promise<MintedShare> {
  const resp = await fetch(`/api/artifacts/${artifactID}/shares`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ scope, ttl, note }),
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  return resp.json();
}

export async function listShares(artifactID: string): Promise<ShareLink[]> {
  const resp = await fetch(`/api/artifacts/${artifactID}/shares`, { credentials: "include" });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  const body = await resp.json();
  return body.shares ?? [];
}

export async function revokeShare(shareID: string): Promise<void> {
  const resp = await fetch(`/api/shares/${shareID}`, {
    method: "DELETE",
    credentials: "include",
  });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
}

export async function listShareOpens(shareID: string): Promise<ShareOpen[]> {
  const resp = await fetch(`/api/shares/${shareID}/opens`, { credentials: "include" });
  if (!resp.ok) {
    throw await errorFrom(resp);
  }
  const body = await resp.json();
  return body.opens ?? [];
}
