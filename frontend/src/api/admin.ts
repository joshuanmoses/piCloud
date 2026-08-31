import { api, request } from './client'
import type {
  AdminJobsResponse,
  AdminOverview,
  AdminSession,
  AdminUser,
  AdminUserListResponse,
  AuditListResponse,
  AuditQuery,
  AuditVocabulary,
  BackgroundJob,
  SessionDetail,
} from '@/types/api'

/**
 * Administration.
 *
 * Everything here manages accounts and the instance. There is deliberately no
 * endpoint that reads anybody's content: the server does not offer one, and an
 * administrator has no more access to somebody's files than any other user.
 */

// --- Overview ----------------------------------------------------------------

/** `GET /api/v1/admin/overview` */
export function getOverview(signal?: AbortSignal): Promise<AdminOverview> {
  return api.get<AdminOverview>('admin/overview', signal ? { signal } : {})
}

// --- Accounts ----------------------------------------------------------------

/** `GET /api/v1/admin/users` */
export function listUsers(
  params: {
    q?: string | undefined
    enabled?: boolean | undefined
    limit?: number | undefined
    offset?: number | undefined
  } = {},
  signal?: AbortSignal,
): Promise<AdminUserListResponse> {
  return request<AdminUserListResponse>('admin/users', {
    method: 'GET',
    params: {
      q: params.q,
      // The server reads presence, so a false must be sent explicitly rather
      // than omitted — "show me the disabled ones" is a real filter.
      enabled: params.enabled === undefined ? undefined : params.enabled ? '1' : '0',
      limit: params.limit,
      offset: params.offset,
    },
    ...(signal ? { signal } : {}),
  })
}

/** `GET /api/v1/admin/users/{id}` */
export function getUser(
  id: string,
  signal?: AbortSignal,
): Promise<{ user: AdminUser; sessions: SessionDetail[] }> {
  return api.get<{ user: AdminUser; sessions: SessionDetail[] }>(
    `admin/users/${encodeURIComponent(id)}`,
    signal ? { signal } : {},
  )
}

export interface CreateUserInput {
  username: string
  display_name?: string
  email?: string
  password: string
  is_admin?: boolean
  quota_bytes?: number | null
  must_change_password?: boolean
}

/** `POST /api/v1/admin/users` */
export function createUser(input: CreateUserInput): Promise<{ user: AdminUser }> {
  return api.post<{ user: AdminUser }>('admin/users', input)
}

/**
 * `PATCH /api/v1/admin/users/{id}`
 *
 * Omitting `quota_bytes` leaves it alone; passing null makes it unlimited.
 * The two are genuinely different, so this takes the whole object rather than
 * stripping undefined values.
 */
export function updateUser(
  id: string,
  changes: Partial<{
    display_name: string
    email: string
    is_enabled: boolean
    is_admin: boolean
    quota_bytes: number | null
    must_change_password: boolean
  }>,
): Promise<{ user: AdminUser }> {
  return api.patch<{ user: AdminUser }>(`admin/users/${encodeURIComponent(id)}`, changes)
}

/** `DELETE /api/v1/admin/users/{id}` */
export function deleteUser(id: string): Promise<void> {
  return api.delete<void>(`admin/users/${encodeURIComponent(id)}`)
}

/** `POST /api/v1/admin/users/{id}/password` */
export function resetPassword(
  id: string,
  password: string,
  mustChange = true,
): Promise<{ must_change_password: boolean }> {
  return api.post<{ must_change_password: boolean }>(
    `admin/users/${encodeURIComponent(id)}/password`,
    { password, must_change_password: mustChange },
  )
}

/** `POST /api/v1/admin/users/{id}/unlock` */
export function unlockUser(id: string): Promise<{ unlocked: boolean }> {
  return api.post<{ unlocked: boolean }>(`admin/users/${encodeURIComponent(id)}/unlock`)
}

/** `POST /api/v1/admin/users/{id}/sessions/revoke` */
export function revokeUserSessions(id: string): Promise<{ revoked: number }> {
  return api.post<{ revoked: number }>(`admin/users/${encodeURIComponent(id)}/sessions/revoke`)
}

// --- Sessions ----------------------------------------------------------------

/** `GET /api/v1/admin/sessions` — everybody who is signed in right now. */
export function listAllSessions(
  signal?: AbortSignal,
): Promise<{ sessions: AdminSession[]; total: number }> {
  return api.get<{ sessions: AdminSession[]; total: number }>(
    'admin/sessions',
    signal ? { signal } : {},
  )
}

/** `GET /api/v1/auth/sessions` — the caller's own. */
export function listMySessions(
  signal?: AbortSignal,
): Promise<{ sessions: SessionDetail[] }> {
  return api.get<{ sessions: SessionDetail[] }>('auth/sessions', signal ? { signal } : {})
}

/** `DELETE /api/v1/auth/sessions/{id}` */
export function revokeMySession(id: string): Promise<void> {
  return api.delete<void>(`auth/sessions/${encodeURIComponent(id)}`)
}

/** `POST /api/v1/auth/sessions/revoke-others` */
export function revokeMyOtherSessions(): Promise<{ revoked: number }> {
  return api.post<{ revoked: number }>('auth/sessions/revoke-others')
}

// --- Audit -------------------------------------------------------------------

/** `GET /api/v1/admin/audit` */
export function listAudit(
  query: AuditQuery = {},
  signal?: AbortSignal,
): Promise<AuditListResponse> {
  return request<AuditListResponse>('admin/audit', {
    method: 'GET',
    params: {
      action: query.action,
      outcome: query.outcome,
      actor_id: query.actorId,
      target_id: query.targetId,
      q: query.q,
      since: query.since,
      until: query.until,
      limit: query.limit,
      offset: query.offset,
    },
    ...(signal ? { signal } : {}),
  })
}

/**
 * `GET /api/v1/admin/audit/vocabulary`
 *
 * The action list comes from the server so the filter cannot drift from what
 * is actually recorded.
 */
export function getAuditVocabulary(signal?: AbortSignal): Promise<AuditVocabulary> {
  return api.get<AuditVocabulary>('admin/audit/vocabulary', signal ? { signal } : {})
}

// --- Jobs --------------------------------------------------------------------

/** `GET /api/v1/admin/jobs` */
export function getJobs(signal?: AbortSignal): Promise<AdminJobsResponse> {
  return api.get<AdminJobsResponse>('admin/jobs', signal ? { signal } : {})
}

/** `POST /api/v1/admin/jobs/{id}/retry` */
export function retryJob(id: string): Promise<{ job: BackgroundJob }> {
  return api.post<{ job: BackgroundJob }>(`admin/jobs/${encodeURIComponent(id)}/retry`)
}

/** React Query keys. */
export const adminKeys = {
  overview: () => ['admin', 'overview'] as const,
  users: (params: object = {}) => ['admin', 'users', params] as const,
  user: (id: string) => ['admin', 'users', id] as const,
  sessions: () => ['admin', 'sessions'] as const,
  mySessions: () => ['auth', 'sessions'] as const,
  audit: (query: AuditQuery = {}) => ['admin', 'audit', query] as const,
  auditVocabulary: () => ['admin', 'audit', 'vocabulary'] as const,
  jobs: () => ['admin', 'jobs'] as const,
}
