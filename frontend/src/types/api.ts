/**
 * Types mirroring the backend REST API.
 *
 * These are hand-maintained rather than generated, and the backend's own tests
 * assert the shape of the payloads they describe. See docs/api.md.
 */

/** Condition of a subsystem, as reported by the backend `health` package. */
export type HealthStatus = 'healthy' | 'degraded' | 'unhealthy'

export interface HealthCheck {
  name: string
  status: HealthStatus
  latency_ms?: number
  detail?: string
}

/** Body of `GET /health`. */
export interface HealthResponse {
  status: HealthStatus
  database: HealthStatus
  storage: HealthStatus
  version: string
  uptime_seconds: number
  checks?: HealthCheck[]
}

/** Body of `GET /api/v1/version`. */
export interface VersionInfo {
  version: string
  commit: string
  build_date: string
  go: string
  platform: string
}

/**
 * Body of `GET /api/v1/config` — the subset of settings the browser needs.
 * It never contains deployment detail such as hosts, paths, or secrets.
 */
export interface PublicConfig {
  app_name: string
  version: string
  registration_enabled: boolean
  sharing_enabled: boolean
  public_links_enabled: boolean
  upload_requests_enabled: boolean
  max_upload_bytes: number
  chunk_size_bytes: number
  password_min_length: number
  trash_retention_days: number
  versioning_enabled: boolean
}

/** Stable, machine-readable error identifiers returned by the API. */
export type ApiErrorCode =
  | 'bad_request'
  | 'validation_failed'
  | 'unauthorized'
  | 'forbidden'
  | 'not_found'
  | 'conflict'
  | 'payload_too_large'
  | 'unsupported_media_type'
  | 'rate_limited'
  | 'quota_exceeded'
  | 'internal_error'
  | 'service_unavailable'
  | 'not_implemented'
  | 'account_locked'
  | 'account_disabled'
  | 'csrf_failed'
  | 'password_change_required'

export interface ApiErrorBody {
  code: ApiErrorCode
  message: string
  /** Per-field messages, present only for validation failures. */
  fields?: Record<string, string>
}

export interface ApiErrorResponse {
  error: ApiErrorBody
  request_id?: string
}

// --- Authentication ---------------------------------------------------------

/** An application account. Never carries a password hash. */
export interface User {
  id: string
  username: string
  display_name: string
  email?: string
  is_enabled: boolean
  is_admin: boolean
  /** null means unlimited. */
  quota_bytes: number | null
  used_bytes: number
  created_at: string
  updated_at: string
  last_login_at?: string
  must_change_password: boolean
}

/** Body of `GET /api/v1/auth/session` and `POST /api/v1/auth/login`. */
export interface SessionResponse {
  user: User
  /** Echoed back in the X-CSRF-Token header on state-changing requests. */
  csrf_token: string
  expires_at: string
  must_change_password: boolean
}

export interface LoginRequest {
  username: string
  password: string
  remember?: boolean
}

/** One of the caller's active sessions. */
export interface UserSession {
  id: string
  created_at: string
  last_seen_at: string
  expires_at: string
  remember: boolean
  ip_address?: string
  user_agent?: string
  /**
   * The user agent reduced to something readable, supplied by the server.
   *
   * Server-side so this page and the administrator's cannot describe the same
   * session differently, which they would if each parsed the string itself.
   */
  device?: string
  /** True for the session making the request. */
  current: boolean
  revoked_at?: string
  revoked_reason?: string
}

/** Body of `GET /api/v1/setup`. */
export interface SetupStatus {
  setup_required: boolean
  password_min_length: number
}

export interface SetupRequest {
  username: string
  display_name?: string
  email?: string
  password: string
}

// --- Files and folders ------------------------------------------------------

export type NodeKind = 'file' | 'folder'

/** A file or folder. Content hashes are never exposed to the client. */
export interface FileNode {
  id: string
  parent_id: string | null
  kind: NodeKind
  name: string
  size_bytes: number
  mime_type?: string
  created_at: string
  updated_at: string
  deleted_at?: string
  owner?: PublicUser
  is_root: boolean
  /**
   * What the caller may do to this item.
   *
   * Used to decide which actions to offer. This is a usability affordance,
   * never a control: the server re-checks on every request.
   */
  permissions?: Permission[]
  is_owner?: boolean
  /** True when reached through a share rather than by owning it. */
  shared?: boolean
  /**
   * True when the caller owns this item and has shared it with somebody else
   * or published it as a link. Sent only to the owner.
   */
  shared_out?: boolean

  /**
   * The caller's own view of the item, populated by the listings that
   * batch-load it. A bare node response leaves these undefined rather than
   * issuing a query per row.
   */
  favorite?: boolean
  has_preview?: boolean
  tags?: Tag[]
  /** How many earlier states this file has. Absent when there are none. */
  version_count?: number
}

export interface PublicUser {
  id: string
  username: string
  display_name: string
}

export interface Breadcrumb {
  id: string
  name: string
  is_root: boolean
}

/** Body of `GET /api/v1/nodes`. */
export interface NodeListResponse {
  folder: FileNode
  breadcrumbs: Breadcrumb[]
  nodes: FileNode[]
  total: number
  has_more: boolean
  limit: number
  offset: number
}

/** Body of `GET /api/v1/nodes/{id}`. */
export interface NodeDetailResponse {
  node: FileNode
  breadcrumbs: Breadcrumb[]
  subtree_size_bytes?: number
  subtree_file_count?: number
}

export type SortField = 'name' | 'size' | 'modified' | 'created'
export type SortOrder = 'asc' | 'desc'

export interface ListNodesParams {
  parentId?: string
  limit?: number
  offset?: number
  sort?: SortField
  order?: SortOrder
  kind?: NodeKind
}

/** An in-flight resumable upload session. */
export interface UploadSession {
  id: string
  name: string
  declared_size: number
  received_bytes: number
  chunk_size: number
  expires_at: string
  complete: boolean
}

export interface UploadResult {
  node: FileNode
  deduplicated: boolean
}

// --- Sharing ----------------------------------------------------------------

export type Permission =
  | 'read'
  | 'download'
  | 'upload'
  | 'create'
  | 'modify'
  | 'rename'
  | 'move'
  | 'delete'
  | 'share'
  | 'manage'

export type ShareRole = 'viewer' | 'contributor' | 'editor' | 'manager'

export interface Group {
  id: string
  name: string
  description: string
  created_at: string
  updated_at: string
  member_count: number
}

export interface GroupMember {
  user: PublicUser
  role: 'member' | 'manager'
  added_at: string
}

/** A permission grant, presented for people. */
export interface Share {
  id: string
  node: {
    id: string
    name: string
    kind: NodeKind
    size_bytes: number
    mime_type?: string
    owner?: PublicUser
  }
  subject_type: 'user' | 'group'
  user?: PublicUser
  group?: { id: string; name: string }
  permissions: Permission[]
  role?: ShareRole
  deny: boolean
  inherit: boolean
  expires_at?: string
  created_at: string
  message?: string
  shared_by?: PublicUser
}

export interface CreateShareRequest {
  node_id: string
  username?: string
  user_id?: string
  group_id?: string
  group_name?: string
  permissions: string[]
  inherit?: boolean
  expires_at?: string
  message?: string
  /**
   * Take access away instead of granting it.
   *
   * The only way to express "everyone in Accounting may edit this, except
   * Bob": a denial beats every grant, including one inherited through a group.
   * It needs the manage permission rather than share, and it takes away
   * exactly what it names — denying `download` does not also remove `read`.
   */
  deny?: boolean
}

/** Body of `GET /api/v1/sharing/vocabulary`. */
export interface SharingVocabulary {
  permissions: Permission[]
  roles: Array<{
    name: ShareRole
    permissions: Permission[]
    description: string
  }>
  enabled: boolean
}

// --- Public links -----------------------------------------------------------

export type LinkKind = 'view' | 'download' | 'upload' | 'upload_view'

/** A published link, as its owner sees it. */
export interface PublicLink {
  id: string
  kind: LinkKind
  /**
   * The address to hand out. Present only in the response that created the
   * link: only a hash is stored, so it can never be shown again.
   */
  url?: string
  node?: { id: string; name: string; kind: NodeKind; size_bytes: number }
  has_password: boolean
  expires_at?: string
  max_uses?: number
  use_count: number
  max_upload_bytes?: number
  allowed_extensions?: string[]
  title?: string
  message?: string
  created_at: string
  revoked_at?: string
  last_used_at?: string
  /** Whether the link currently works. */
  live: boolean
}

export interface CreatePublicLinkRequest {
  node_id: string
  kind: LinkKind
  password?: string
  expires_at?: string
  max_uses?: number
  max_upload_bytes?: number
  allowed_extensions?: string[]
  title?: string
  message?: string
}

/** One item as an anonymous visitor sees it: no owner, no permissions. */
export interface PortalItem {
  id: string
  name: string
  kind: NodeKind
  size_bytes: number
  mime_type?: string
  modified?: string
}

/** Body of `GET /api/v1/portal/{token}`. */
export interface PortalView {
  /** When true, nothing else is populated. */
  needs_password: boolean
  kind?: LinkKind
  title?: string
  message?: string
  requested_by?: string
  destination?: string
  item?: PortalItem
  items?: PortalItem[]
  can_download: boolean
  can_upload: boolean
  max_upload_bytes?: number
  allowed_extensions?: string[]
  expires_at?: string
  /** -1 when unlimited. */
  remaining_uses: number
}

// --- Search -----------------------------------------------------------------

/** One search hit: a node plus its relevance. */
export interface SearchResult extends FileNode {
  /** Trigram similarity. A display hint, not a security-relevant value. */
  score: number
}

/** Body of `GET /api/v1/search`. */
export interface SearchResponse {
  results: SearchResult[]
  total: number
  query: string
  limit?: number
  offset?: number
}

/** The friendly categories the search filter offers, mapped server-side. */
export type SearchCategory = 'image' | 'video' | 'audio' | 'pdf' | 'text'

export interface SearchParams {
  q: string
  kind?: NodeKind
  type?: SearchCategory
  inFolder?: string
  ownedByMe?: boolean
  modifiedAfter?: string
  modifiedBefore?: string
  minSize?: number
  maxSize?: number
  tags?: string[]
  limit?: number
  offset?: number
}

// --- Favourites, recent, tags ------------------------------------------------

/** Body of `GET /api/v1/favorites`. */
export interface FavoritesResponse {
  nodes: FileNode[]
  total: number
  limit: number
  offset: number
  has_more: boolean
}

/** How a node was last touched. */
export type RecentAction = 'viewed' | 'downloaded' | 'uploaded' | 'modified'

export interface RecentEntry extends FileNode {
  action: RecentAction
  occurred_at: string
}

/** Body of `GET /api/v1/recent`. */
export interface RecentResponse {
  items: RecentEntry[]
  total: number
  limit: number
  offset: number
  has_more: boolean
}

/** A user-defined label. Tags are private to the person who created them. */
export interface Tag {
  id: string
  name: string
  /** "#rrggbb", or absent for the interface default. */
  color?: string
  created_at: string
  /** How many live files carry this tag. Present only in the tag listing. */
  file_count?: number
}

// --- Previews ----------------------------------------------------------------

/** Whether a thumbnail exists for a node. */
export type PreviewStatus = 'pending' | 'ready' | 'unavailable'

/** Body of `GET /api/v1/nodes/{id}/preview/status`. */
export interface PreviewStatusResponse {
  status: PreviewStatus
  available: boolean
  width?: number
  height?: number
}

// --- Version history ---------------------------------------------------------

/** Why a version exists. */
export type VersionReason = 'replaced' | 'restored'

/** One previous state of a file. */
export interface FileVersion {
  id: string
  version: number
  size_bytes: number
  mime_type?: string
  /** What the file was called when this version was made. */
  name: string
  created_at: string
  created_by?: PublicUser
  reason: VersionReason
  /** The reason written for a person, supplied by the server. */
  reason_label: string
}

/** Body of `GET /api/v1/nodes/{id}/versions`. */
export interface VersionListResponse {
  versions: FileVersion[]
  total: number
  /** The file as it stands now. Not a version, but shown alongside them. */
  current: {
    size_bytes: number
    mime_type?: string
    name: string
    updated_at: string
  }
}

// --- Administration ----------------------------------------------------------

/** An account as an administrator sees it. */
export interface AdminUser {
  id: string
  username: string
  display_name: string
  email?: string
  is_enabled: boolean
  is_admin: boolean
  is_locked: boolean
  quota_bytes: number | null
  used_bytes: number
  created_at: string
  last_login_at?: string
  must_change_password: boolean
}

export interface AdminUserListResponse {
  users: AdminUser[]
  total: number
  limit: number
  offset: number
  has_more: boolean
}

/**
 * One sign-in session.
 *
 * The same shape whether a person is looking at their own or an administrator
 * is looking at everybody's: what differs is whose they may see, which the
 * route decides, not the payload.
 */
export type SessionDetail = UserSession

export interface AdminSession extends SessionDetail {
  user?: PublicUser
}

/** Whether a recorded action succeeded. */
export type AuditOutcome = 'success' | 'failure' | 'denied'

/** One recorded event. */
export interface AuditEvent {
  id: string
  action: string
  /** The action written for a person, supplied by the server. */
  label: string
  outcome: AuditOutcome
  /** Absent when the account has since been deleted; actor_label survives. */
  actor?: PublicUser
  actor_label?: string
  target_id?: string
  target_type?: string
  target_label?: string
  ip?: string
  device?: string
  request_id?: string
  detail?: Record<string, unknown>
  created_at: string
}

export interface AuditListResponse {
  events: AuditEvent[]
  total: number
  limit: number
  offset: number
  has_more: boolean
}

export interface AuditVocabulary {
  actions: Array<{ action: string; label: string }>
  outcomes: AuditOutcome[]
}

export interface AuditQuery {
  action?: string
  outcome?: AuditOutcome
  actorId?: string
  targetId?: string
  q?: string
  since?: string
  until?: string
  limit?: number
  offset?: number
}

/** One background job. */
export interface BackgroundJob {
  id: string
  kind: string
  status: 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  attempts: number
  max_attempts: number
  last_error?: string
  run_after: string
  created_at: string
}

export interface JobStats {
  pending: number
  running: number
  succeeded: number
  failed: number
  /**
   * How long the oldest pending job has been waiting, in seconds. A number
   * that keeps climbing means the workers are not keeping up.
   */
  oldest_pending_seconds: number
}

export interface AdminJobsResponse {
  stats: JobStats
  failed: BackgroundJob[]
  running: BackgroundJob[]
}

/** Body of `GET /api/v1/admin/overview`. Describes the instance, never content. */
export interface AdminOverview {
  users: { total: number; administrators: number }
  sessions: { live: number }
  storage: {
    status: HealthStatus
    detail?: string
    free_bytes?: number
    used_percent?: number
  }
  database: { status: HealthStatus; latency_ms: number; detail?: string }
  uptime_seconds: number
  jobs?: JobStats
  audit_24h?: Array<{ action: string; label: string; count: number; failed: number; denied: number }>
}

// --- Group management --------------------------------------------------------

export interface CreateGroupRequest {
  name: string
  description?: string | undefined
}

/** Body of `GET /api/v1/groups/{id}`. */
export interface GroupDetail {
  group: Group
  members: GroupMember[]
}
