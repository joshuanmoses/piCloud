import type { ApiErrorBody, ApiErrorCode, ApiErrorResponse } from '@/types/api'

/**
 * The single HTTP client for the application.
 *
 * Design notes:
 *
 * - Requests are same-origin and always send cookies. Authentication is a
 *   server-side session in an HttpOnly cookie, so the browser cannot read the
 *   credential and no token is ever placed in localStorage.
 * - Every failure surfaces as an `ApiError` carrying the backend's stable error
 *   code, so UI code branches on a code rather than on a message string.
 * - Nothing here decides what a user may do. The backend enforces every
 *   permission; this layer only reports what it was told.
 */

/** Base path for the versioned API. Same-origin: Caddy fronts both in production. */
export const API_BASE = '/api/v1'

/** An error returned by the API, or a transport failure normalised to look like one. */
export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode
  readonly fields: Record<string, string> | undefined
  readonly requestId: string | undefined

  constructor(
    status: number,
    body: ApiErrorBody,
    requestId?: string,
    cause?: unknown,
    retryAfterSeconds?: number,
  ) {
    super(body.message, cause !== undefined ? { cause } : undefined)
    this.name = 'ApiError'
    this.status = status
    this.code = body.code
    this.fields = body.fields
    this.requestId = requestId
    this.retryAfterSeconds = retryAfterSeconds
  }

  /** True when signing in again is the appropriate response. */
  get isAuthError(): boolean {
    return this.code === 'unauthorized'
  }

  /**
   * How long the server asked the caller to wait, in seconds.
   *
   * Present on a 429, from the Retry-After header. Surfacing it turns a wall
   * into an instruction: a client that retries immediately makes the situation
   * it is complaining about worse.
   */
  readonly retryAfterSeconds?: number | undefined

  /** True when retrying later might succeed. */
  get isRetryable(): boolean {
    return (
      this.code === 'service_unavailable' ||
      this.code === 'rate_limited' ||
      this.status === 0 ||
      this.status >= 500
    )
  }
}

export interface RequestOptions extends Omit<RequestInit, 'body'> {
  /** Serialized as JSON unless it is already a BodyInit. */
  body?: unknown
  /**
   * Query parameters; undefined and null values are omitted.
   *
   * An array becomes a repeated parameter (`?tag=a&tag=b`) rather than a joined
   * string, because that is what the API reads and joining would make a value
   * containing a comma ambiguous.
   */
  params?: Record<
    string,
    string | number | boolean | undefined | null | readonly (string | number)[]
  >
  /** Abort the request after this many milliseconds. Omit for no timeout. */
  timeoutMs?: number
}

/** Default timeout for ordinary JSON calls. Uploads and downloads opt out. */
const DEFAULT_TIMEOUT_MS = 30_000

/** Name of the cookie carrying the CSRF token. Set by the server on sign-in. */
const CSRF_COOKIE = 'pifiles_csrf'
/** Header the server expects the token echoed in. */
const CSRF_HEADER = 'X-CSRF-Token'

/**
 * Reads the CSRF token the server set.
 *
 * This is the readable half of a signed double-submit pair: the session cookie
 * is HttpOnly and unreadable, this one is not. A cross-site attacker can cause
 * the browser to send the session cookie but cannot read this value, so they
 * cannot populate the header — which is exactly what makes the scheme work.
 */
export function readCSRFToken(): string | undefined {
  if (typeof document === 'undefined') return undefined
  for (const part of document.cookie.split(';')) {
    const [name, ...rest] = part.trim().split('=')
    if (name === CSRF_COOKIE) {
      const value = rest.join('=')
      return value ? decodeURIComponent(value) : undefined
    }
  }
  return undefined
}

/** Methods that change state and therefore need a CSRF token. */
function needsCSRF(method: string | undefined): boolean {
  const upper = (method ?? 'GET').toUpperCase()
  return upper !== 'GET' && upper !== 'HEAD' && upper !== 'OPTIONS'
}

function buildUrl(path: string, params?: RequestOptions['params']): string {
  const url = path.startsWith('/') ? path : `${API_BASE}/${path}`
  if (!params) return url

  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null) continue
    if (Array.isArray(value)) {
      for (const item of value) search.append(key, String(item))
      continue
    }
    search.append(key, String(value))
  }
  const query = search.toString()
  return query ? `${url}?${query}` : url
}

function isBodyInit(value: unknown): value is BodyInit {
  return (
    typeof value === 'string' ||
    value instanceof FormData ||
    value instanceof Blob ||
    value instanceof ArrayBuffer ||
    value instanceof URLSearchParams ||
    value instanceof ReadableStream
  )
}

/**
 * Performs a request and parses the response.
 *
 * @throws {ApiError} for any non-2xx response, and for transport failures.
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { body, params, timeoutMs = DEFAULT_TIMEOUT_MS, headers, signal, ...rest } = options

  const requestHeaders = new Headers(headers)
  requestHeaders.set('Accept', 'application/json')

  if (needsCSRF(rest.method) && !requestHeaders.has(CSRF_HEADER)) {
    const csrf = readCSRFToken()
    if (csrf) requestHeaders.set(CSRF_HEADER, csrf)
  }

  let requestBody: BodyInit | undefined
  if (body !== undefined) {
    if (isBodyInit(body)) {
      requestBody = body
    } else {
      requestBody = JSON.stringify(body)
      requestHeaders.set('Content-Type', 'application/json')
    }
  }

  // Combine the caller's signal with our timeout so either can abort.
  const controller = new AbortController()
  const timeout =
    timeoutMs > 0 ? setTimeout(() => controller.abort(new Error('timeout')), timeoutMs) : undefined
  if (signal) {
    if (signal.aborted) controller.abort(signal.reason)
    else signal.addEventListener('abort', () => controller.abort(signal.reason), { once: true })
  }

  let response: Response
  try {
    response = await fetch(buildUrl(path, params), {
      ...rest,
      headers: requestHeaders,
      // Session cookies are HttpOnly; they must travel with every request.
      credentials: 'same-origin',
      signal: controller.signal,
      ...(requestBody !== undefined ? { body: requestBody } : {}),
    })
  } catch (cause) {
    // Distinguish a deliberate cancellation from a genuine connectivity
    // failure: a cancelled request is not an error the user should see.
    if (cause instanceof DOMException && cause.name === 'AbortError' && signal?.aborted) {
      throw cause
    }
    throw new ApiError(
      0,
      {
        code: 'service_unavailable',
        message:
          'Cannot reach the server. Check that PiFiles is running and that you are connected to the network.',
      },
      undefined,
      cause,
    )
  } finally {
    if (timeout !== undefined) clearTimeout(timeout)
  }

  const requestId = response.headers.get('X-Request-Id') ?? undefined

  if (!response.ok) {
    throw new ApiError(
      response.status,
      await readErrorBody(response),
      requestId,
      undefined,
      parseRetryAfter(response.headers.get('Retry-After')),
    )
  }

  if (response.status === 204 || response.headers.get('Content-Length') === '0') {
    return undefined as T
  }

  const contentType = response.headers.get('Content-Type') ?? ''
  if (!contentType.includes('application/json')) {
    return (await response.text()) as T
  }

  try {
    return (await response.json()) as T
  } catch {
    throw new ApiError(
      response.status,
      { code: 'internal_error', message: 'The server returned a malformed response.' },
      requestId,
    )
  }
}

/**
 * Extracts an error body, falling back to a status-appropriate message when the
 * response is not the expected JSON envelope — which happens when a proxy or a
 * gateway, rather than the application, produced the error.
 */
async function readErrorBody(response: Response): Promise<ApiErrorBody> {
  try {
    const parsed = (await response.json()) as Partial<ApiErrorResponse>
    if (parsed?.error?.code && parsed.error.message) {
      return parsed.error
    }
  } catch {
    // Fall through to the generic message below.
  }
  return { code: codeForStatus(response.status), message: messageForStatus(response.status) }
}

function codeForStatus(status: number): ApiErrorCode {
  switch (status) {
    case 400:
      return 'bad_request'
    case 401:
      return 'unauthorized'
    case 403:
      return 'forbidden'
    case 404:
      return 'not_found'
    case 409:
      return 'conflict'
    case 413:
      return 'payload_too_large'
    case 422:
      return 'validation_failed'
    case 429:
      return 'rate_limited'
    case 503:
      return 'service_unavailable'
    default:
      return status >= 500 ? 'internal_error' : 'bad_request'
  }
}

function messageForStatus(status: number): string {
  switch (status) {
    case 401:
      return 'Your session has expired. Please sign in again.'
    case 403:
      return 'You do not have permission to do that.'
    case 404:
      return 'The requested item was not found.'
    case 413:
      return 'That file is larger than this server accepts.'
    case 429:
      return 'Too many requests. Please wait a moment and try again.'
    case 503:
      return 'The server is temporarily unavailable. Please try again shortly.'
    default:
      return status >= 500
        ? 'Something went wrong on the server. Please try again.'
        : 'The request could not be completed.'
  }
}

/**
 * Reads the Retry-After header.
 *
 * Only the delta-seconds form is handled. The HTTP-date form is legal and
 * PiFiles never sends it; guessing at a date parse for a value this code did
 * not produce would be more failure modes than the feature is worth.
 */
function parseRetryAfter(header: string | null): number | undefined {
  if (!header) return undefined
  const seconds = Number.parseInt(header, 10)
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined
}

/** Convenience wrappers. */
export const api = {
  get: <T>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'GET' }),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'POST', body }),
  patch: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'PATCH', body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'PUT', body }),
  delete: <T>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'DELETE' }),
}
