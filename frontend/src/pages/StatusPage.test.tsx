import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { HealthResponse, PublicConfig } from '@/types/api'
import { StatusPage } from './StatusPage'

const healthyResponse: HealthResponse = {
  status: 'healthy',
  database: 'healthy',
  storage: 'healthy',
  version: '1.0.0',
  uptime_seconds: 3725,
  checks: [
    { name: 'database', status: 'healthy', latency_ms: 2 },
    { name: 'storage', status: 'healthy' },
  ],
}

const configResponse: PublicConfig = {
  app_name: 'PiFiles',
  version: '1.0.0',
  registration_enabled: false,
  sharing_enabled: true,
  public_links_enabled: true,
  upload_requests_enabled: true,
  max_upload_bytes: 20 * 1024 ** 3,
  chunk_size_bytes: 16 * 1024 ** 2,
  password_min_length: 12,
  trash_retention_days: 30,
  versioning_enabled: true,
}

/** Routes stubbed fetch responses by URL so both queries resolve. */
function stubApi(overrides: { health?: Response; config?: Response } = {}) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = urlOf(input)
    if (url.startsWith('/health')) {
      return Promise.resolve(overrides.health ?? jsonResponse(healthyResponse))
    }
    if (url.includes('/config')) {
      return Promise.resolve(overrides.config ?? jsonResponse(configResponse))
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('StatusPage', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows a loading state before data arrives', () => {
    stubApi()
    renderWithProviders(<StatusPage />)
    expect(screen.getByLabelText('Loading service health')).toBeInTheDocument()
  })

  it('renders health, subsystems, and uptime once loaded', async () => {
    stubApi()
    renderWithProviders(<StatusPage />)

    const healthCard = await screen.findByText('Service health')
    const card = healthCard.closest('div[class*="rounded-card"]')
    expect(card).not.toBeNull()

    await waitFor(() => {
      expect(within(card as HTMLElement).getAllByText('Healthy').length).toBeGreaterThan(0)
    })

    expect(screen.getByText('database')).toBeInTheDocument()
    expect(screen.getByText('storage')).toBeInTheDocument()
    expect(screen.getByText('2 ms')).toBeInTheDocument()
    // 3725 seconds is 1h 2m.
    expect(screen.getByText('1h 2m')).toBeInTheDocument()
    expect(screen.getByText('1.0.0')).toBeInTheDocument()
  })

  it('renders instance settings with human-readable sizes', async () => {
    stubApi()
    renderWithProviders(<StatusPage />)

    expect(await screen.findByText('20 GB')).toBeInTheDocument()
    expect(screen.getByText('16 MB')).toBeInTheDocument()
    expect(screen.getByText('30 days')).toBeInTheDocument()
  })

  it('surfaces a degraded subsystem with its explanation', async () => {
    stubApi({
      health: jsonResponse({
        ...healthyResponse,
        status: 'degraded',
        storage: 'degraded',
        checks: [
          { name: 'database', status: 'healthy', latency_ms: 3 },
          {
            name: 'storage',
            status: 'degraded',
            detail: 'free space is approaching the configured minimum',
          },
        ],
      } satisfies HealthResponse),
    })

    renderWithProviders(<StatusPage />)

    expect(await screen.findByText(/free space is approaching/i)).toBeInTheDocument()
    expect(screen.getAllByText('Degraded').length).toBeGreaterThan(0)
  })

  it('shows an actionable error when the server is unreachable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))),
    )

    renderWithProviders(<StatusPage />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/cannot reach the pifiles server/i)
    expect(within(alert).getByRole('button', { name: /try again/i })).toBeInTheDocument()
  })

  it('retries when the user clicks Try again', async () => {
    const user = userEvent.setup()
    let attempt = 0
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = urlOf(input)
        if (url.startsWith('/health')) {
          attempt += 1
          if (attempt === 1) return Promise.reject(new TypeError('Failed to fetch'))
          return Promise.resolve(jsonResponse(healthyResponse))
        }
        return Promise.resolve(jsonResponse(configResponse))
      }),
    )

    renderWithProviders(<StatusPage />)

    const alert = await screen.findByRole('alert')
    await user.click(within(alert).getByRole('button', { name: /try again/i }))

    await waitFor(() => {
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    })
    expect(screen.getByText('database')).toBeInTheDocument()
  })

  it('still renders health when only the settings request fails', async () => {
    stubApi({ config: new Response('', { status: 500 }) })
    renderWithProviders(<StatusPage />)

    expect(await screen.findByText('database')).toBeInTheDocument()
    expect(await screen.findByText(/settings could not be loaded/i)).toBeInTheDocument()
  })

  it('refetches on demand', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi()
    renderWithProviders(<StatusPage />)

    await screen.findByText('database')
    const healthCallsBefore = fetchMock.mock.calls.filter((c) => urlOf(c[0]).startsWith('/health')).length

    await user.click(screen.getByRole('button', { name: /refresh/i }))

    await waitFor(() => {
      const after = fetchMock.mock.calls.filter((c) => urlOf(c[0]).startsWith('/health')).length
      expect(after).toBeGreaterThan(healthCallsBefore)
    })
  })
})
