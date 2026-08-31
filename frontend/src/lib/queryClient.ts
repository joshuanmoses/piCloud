import { QueryClient } from '@tanstack/react-query'
import { ApiError } from '@/api/client'

/**
 * Query defaults are tuned for a small server on a home network: retry only
 * what is worth retrying, and never poll in a background tab.
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        gcTime: 5 * 60_000,
        // Refetching on every window focus is a meaningful load on a Pi and
        // rarely what the user wants.
        refetchOnWindowFocus: false,
        refetchIntervalInBackground: false,
        retry: (failureCount, error) => {
          // Retrying a 401 or a 403 cannot succeed and only delays the error.
          if (error instanceof ApiError && !error.isRetryable) return false
          return failureCount < 2
        },
        retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
      },
      mutations: { retry: false },
    },
  })
}
