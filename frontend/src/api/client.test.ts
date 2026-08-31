import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, request } from './client'

/**
 * Stubs global fetch. Typing the mock as `typeof fetch` keeps `mock.calls`
 * strongly typed, so assertions about the request are checked at compile time.
 */
function mockFetch(response: Response | Error) {
  const fn = vi.fn<typeof fetch>(() =>
    response instanceof Error ? Promise.reject(response) : Promise.resolve(response),
  )
  vi.stubGlobal('fetch', fn)
  return fn
}

function json(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

describe('request', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('parses a successful JSON response', async () => {
    mockFetch(json({ status: 'healthy' }))
    await expect(request('/health')).resolves.toEqual({ status: 'healthy' })
  })

  it('prefixes bare paths with the versioned API base', async () => {
    const fetchMock = mockFetch(json({}))
    await request('config')
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/config')
  })

  it('leaves absolute paths alone so /health stays outside the API version', async () => {
    const fetchMock = mockFetch(json({}))
    await request('/health')
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/health')
  })

  it('always sends cookies, because the session is an HttpOnly cookie', async () => {
    const fetchMock = mockFetch(json({}))
    await request('/health')
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ credentials: 'same-origin' })
  })

  it('serializes an object body as JSON and sets the content type', async () => {
    const fetchMock = mockFetch(json({}))
    await api.post('folders', { name: 'Photos' })

    const init = fetchMock.mock.calls[0]?.[1]
    expect(init).toBeDefined()
    if (!init) return
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{"name":"Photos"}')
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
  })

  it('passes FormData through untouched so the browser sets the boundary', async () => {
    const fetchMock = mockFetch(json({}))
    const form = new FormData()
    form.append('file', new Blob(['x']))
    await api.post('files/upload', form)

    const init = fetchMock.mock.calls[0]?.[1]
    expect(init).toBeDefined()
    if (!init) return
    expect(init.body).toBe(form)
    expect(new Headers(init.headers).has('Content-Type')).toBe(false)
  })

  it('builds a query string and omits null and undefined values', async () => {
    const fetchMock = mockFetch(json({}))
    await request('search', { params: { q: 'report', page: 2, archived: undefined, owner: null } })
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/search?q=report&page=2')
  })

  it('returns undefined for a 204 response', async () => {
    mockFetch(new Response(null, { status: 204 }))
    await expect(request('/thing')).resolves.toBeUndefined()
  })

  describe('error handling', () => {
    it('throws ApiError carrying the backend error code', async () => {
      mockFetch(
        json(
          { error: { code: 'forbidden', message: 'You do not have permission.' }, request_id: 'abc123' },
          { status: 403, headers: { 'Content-Type': 'application/json', 'X-Request-Id': 'abc123' } },
        ),
      )

      const error = await request('/files/1').catch((e: unknown) => e)
      expect(error).toBeInstanceOf(ApiError)
      expect((error as ApiError).code).toBe('forbidden')
      expect((error as ApiError).status).toBe(403)
      expect((error as ApiError).message).toBe('You do not have permission.')
      expect((error as ApiError).requestId).toBe('abc123')
    })

    it('surfaces per-field validation messages', async () => {
      mockFetch(
        json(
          {
            error: {
              code: 'validation_failed',
              message: 'Check the form.',
              fields: { username: 'already taken' },
            },
          },
          { status: 422, headers: { 'Content-Type': 'application/json' } },
        ),
      )

      const error = (await request('/users').catch((e: unknown) => e)) as ApiError
      expect(error.fields).toEqual({ username: 'already taken' })
    })

    it('falls back to a status-appropriate message when a proxy returns HTML', async () => {
      mockFetch(new Response('<html>502 Bad Gateway</html>', { status: 502 }))

      const error = (await request('/health').catch((e: unknown) => e)) as ApiError
      expect(error).toBeInstanceOf(ApiError)
      expect(error.code).toBe('internal_error')
      // The user must never see raw gateway HTML.
      expect(error.message).not.toContain('<html>')
    })

    it('reports a transport failure as an actionable message', async () => {
      mockFetch(new TypeError('Failed to fetch'))

      const error = (await request('/health').catch((e: unknown) => e)) as ApiError
      expect(error).toBeInstanceOf(ApiError)
      expect(error.status).toBe(0)
      expect(error.message).toMatch(/cannot reach the server/i)
    })

    it('classifies retryable and non-retryable failures', () => {
      const forbidden = new ApiError(403, { code: 'forbidden', message: 'no' })
      expect(forbidden.isRetryable).toBe(false)
      expect(forbidden.isAuthError).toBe(false)

      const unauthorized = new ApiError(401, { code: 'unauthorized', message: 'no' })
      expect(unauthorized.isAuthError).toBe(true)

      expect(new ApiError(503, { code: 'service_unavailable', message: 'x' }).isRetryable).toBe(true)
      expect(new ApiError(0, { code: 'service_unavailable', message: 'x' }).isRetryable).toBe(true)
      expect(new ApiError(429, { code: 'rate_limited', message: 'x' }).isRetryable).toBe(true)
    })

    it('maps common statuses to friendly messages without a JSON envelope', async () => {
      const cases: Array<[number, RegExp]> = [
        [401, /session has expired/i],
        [403, /permission/i],
        [404, /not found/i],
        [413, /larger than/i],
        [429, /too many requests/i],
      ]
      for (const [status, pattern] of cases) {
        mockFetch(new Response('', { status }))
        const error = (await request('/x').catch((e: unknown) => e)) as ApiError
        expect(error.message).toMatch(pattern)
      }
    })

    it('reports a malformed JSON body rather than throwing a parse error', async () => {
      mockFetch(new Response('{not json', { status: 200, headers: { 'Content-Type': 'application/json' } }))

      const error = (await request('/health').catch((e: unknown) => e)) as ApiError
      expect(error).toBeInstanceOf(ApiError)
      expect(error.message).toMatch(/malformed/i)
    })
  })

  it('aborts when the caller cancels', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(
        (_input, init) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () => {
              reject(new DOMException('The operation was aborted.', 'AbortError'))
            })
          }),
      ),
    )

    const controller = new AbortController()
    const pending = request('/health', { signal: controller.signal })
    controller.abort()

    const error = (await pending.catch((e: unknown) => e)) as Error
    expect(error.name).toBe('AbortError')
  })
})

describe('array query parameters', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('repeats an array parameter rather than joining it', async () => {
    const fetchMock = mockFetch(json({}))
    await request('search', { params: { q: 'report', tag: ['Urgent', 'Q4'] } })
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/search?q=report&tag=Urgent&tag=Q4')
  })

  it('omits an empty array entirely', async () => {
    const fetchMock = mockFetch(json({}))
    await request('search', { params: { q: 'report', tag: [] } })
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/search?q=report')
  })

  // A tag name containing a comma would be ambiguous if the values were joined.
  it('encodes each value independently', async () => {
    const fetchMock = mockFetch(json({}))
    await request('search', { params: { q: 'x', tag: ['a,b', 'c&d'] } })
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/search?q=x&tag=a%2Cb&tag=c%26d')
  })
})

describe('rate limiting', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // Surfacing the wait turns a wall into an instruction: a client that retries
  // immediately makes the condition it is reporting worse.
  it('reads Retry-After from a 429', async () => {
    mockFetch(
      new Response('', { status: 429, headers: { 'Retry-After': '42' } }),
    )

    const error = (await request('/nodes').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('rate_limited')
    expect(error.retryAfterSeconds).toBe(42)
    expect(error.isRetryable).toBe(true)
  })

  it('leaves the wait undefined when the header is absent or unusable', async () => {
    for (const headers of [{}, { 'Retry-After': 'Wed, 21 Oct 2026 07:28:00 GMT' }]) {
      mockFetch(new Response('', { status: 429, headers }))
      const error = (await request('/nodes').catch((e: unknown) => e)) as ApiError
      expect(error.retryAfterSeconds).toBeUndefined()
    }
  })
})
