import { api } from './client'
import type {
  LoginRequest,
  SessionResponse,
  SetupRequest,
  SetupStatus,
  UserSession,
} from '@/types/api'

/**
 * Authentication endpoints.
 *
 * The session lives in an HttpOnly cookie the browser manages. Nothing here
 * reads, writes, or stores a credential — there is deliberately no token in
 * localStorage for a cross-site script to steal.
 */

/** `POST /api/v1/auth/login` */
export function login(body: LoginRequest): Promise<SessionResponse> {
  return api.post<SessionResponse>('auth/login', body)
}

/** `POST /api/v1/auth/logout` */
export function logout(): Promise<void> {
  return api.post<void>('auth/logout')
}

/** `GET /api/v1/auth/session` — throws ApiError with code `unauthorized` when signed out. */
export function fetchSession(signal?: AbortSignal): Promise<SessionResponse> {
  return api.get<SessionResponse>('auth/session', signal ? { signal } : {})
}

/** `POST /api/v1/auth/password` */
export function changePassword(body: {
  current_password: string
  new_password: string
}): Promise<void> {
  return api.post<void>('auth/password', body)
}

/** `GET /api/v1/auth/sessions` */
export function fetchMySessions(signal?: AbortSignal): Promise<{ sessions: UserSession[] }> {
  return api.get<{ sessions: UserSession[] }>('auth/sessions', signal ? { signal } : {})
}

/** `DELETE /api/v1/auth/sessions/{id}` */
export function revokeSession(id: string): Promise<void> {
  return api.delete<void>(`auth/sessions/${encodeURIComponent(id)}`)
}

/** `GET /api/v1/setup` */
export function fetchSetupStatus(signal?: AbortSignal): Promise<SetupStatus> {
  return api.get<SetupStatus>('setup', signal ? { signal } : {})
}

/** `POST /api/v1/setup` — creates the first administrator and signs them in. */
export function completeSetup(body: SetupRequest): Promise<SessionResponse> {
  return api.post<SessionResponse>('setup', body)
}

/** React Query keys for authentication state. */
export const authKeys = {
  all: ['auth'] as const,
  session: () => [...authKeys.all, 'session'] as const,
  sessions: () => [...authKeys.all, 'sessions'] as const,
  setup: () => [...authKeys.all, 'setup'] as const,
}
