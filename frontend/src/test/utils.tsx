import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderOptions, type RenderResult } from '@testing-library/react'
import type { ReactElement, ReactNode } from 'react'
import { MemoryRouter } from 'react-router-dom'

/**
 * Renders a component inside the providers the application supplies, with
 * retries disabled so a failing request surfaces immediately instead of after
 * several backoff delays.
 */
export function renderWithProviders(
  ui: ReactElement,
  options: RenderOptions & { route?: string } = {},
): RenderResult & { queryClient: QueryClient } {
  const { route = '/', ...renderOptions } = options

  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  })

  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[route]}>{children}</MemoryRouter>
      </QueryClientProvider>
    )
  }

  return { ...render(ui, { wrapper: Wrapper, ...renderOptions }), queryClient }
}

/** Builds a fetch Response for stubbing the API client. */
export function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

/**
 * Extracts the URL from whatever shape a fetch mock was called with. Using
 * String() directly would stringify a Request object as "[object Object]".
 */
export function urlOf(input: RequestInfo | URL | undefined): string {
  if (input === undefined) return ''
  if (typeof input === 'string') return input
  if (input instanceof URL) return input.href
  return input.url
}

/**
 * Parses the JSON body a fetch mock was called with.
 *
 * `BodyInit` may be a Blob, a stream, or FormData, so stringifying it blindly
 * can silently produce "[object Object]" and make an assertion pass against
 * nonsense. This narrows to the string case the API client actually produces.
 */
export function jsonBodyOf(init: RequestInit | undefined): unknown {
  const body = init?.body
  if (typeof body !== 'string') {
    throw new Error(`expected a string request body, got ${Object.prototype.toString.call(body)}`)
  }
  return JSON.parse(body)
}
