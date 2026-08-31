import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { ApiError } from '@/api/client'
import { authKeys, fetchSession, login as loginRequest, logout as logoutRequest } from '@/api/auth'
import type { LoginRequest, SessionResponse, User } from '@/types/api'

/**
 * Authentication state, derived from the server rather than stored locally.
 *
 * There is deliberately no client-side "am I logged in" flag to fall out of
 * step with reality. The session cookie is HttpOnly and unreadable here, so the
 * only honest answer comes from asking the server — and a cached answer is
 * revalidated rather than trusted indefinitely.
 */
export interface AuthState {
  user: User | undefined
  isAuthenticated: boolean
  /** True while the initial session check is in flight. */
  isLoading: boolean
  /** True when the server is unreachable, as opposed to the user being signed out. */
  isOffline: boolean
  mustChangePassword: boolean
}

export function useAuth(): AuthState {
  const query = useQuery({
    queryKey: authKeys.session(),
    queryFn: ({ signal }) => fetchSession(signal),
    // A 401 is the expected answer for a signed-out visitor, not a failure to
    // retry. Anything else may be worth one retry.
    retry: (failureCount, error) => {
      if (error instanceof ApiError && !error.isRetryable) return false
      return failureCount < 1
    },
    staleTime: 60_000,
    // Re-check when the tab regains focus: the session may have been revoked
    // from another device in the meantime.
    refetchOnWindowFocus: true,
  })

  const error = query.error
  const isAuthError = error instanceof ApiError && error.isAuthError

  return {
    user: query.data?.user,
    isAuthenticated: Boolean(query.data?.user),
    isLoading: query.isPending,
    // A transport failure is not the same as being signed out, and the two
    // need very different messages.
    isOffline: Boolean(error) && !isAuthError,
    mustChangePassword: Boolean(query.data?.must_change_password),
  }
}

/** Signs in and primes the session cache with the response. */
export function useLogin() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (body: LoginRequest) => loginRequest(body),
    onSuccess: (data: SessionResponse) => {
      // The login response is the same shape as the session response, so the
      // cache can be seeded directly rather than triggering another round trip.
      queryClient.setQueryData(authKeys.session(), data)
    },
  })
}

/** Signs out and clears every cached query. */
export function useLogout() {
  const queryClient = useQueryClient()

  const clear = useCallback(() => {
    // Everything cached was fetched as the previous user. Clearing rather than
    // invalidating ensures another account can never briefly see stale data.
    queryClient.clear()
  }, [queryClient])

  return useMutation({
    mutationFn: () => logoutRequest(),
    onSettled: clear,
  })
}
