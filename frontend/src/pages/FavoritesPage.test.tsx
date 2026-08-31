import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { FavoritesPage } from '@/pages/FavoritesPage'
import { RecentPage } from '@/pages/RecentPage'
import { jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { FileNode, RecentEntry } from '@/types/api'

const owner = { id: 'u1', username: 'josh', display_name: 'Josh Moses' }

function node(overrides: Partial<FileNode> & Pick<FileNode, 'id' | 'name' | 'kind'>): FileNode {
  return {
    parent_id: 'root-id',
    size_bytes: 1024,
    mime_type: '',
    created_at: '2026-08-20T10:00:00Z',
    updated_at: '2026-08-26T10:00:00Z',
    owner,
    is_root: false,
    permissions: ['read', 'download'],
    is_owner: true,
    shared: false,
    ...overrides,
  }
}

function stub(routes: Record<string, () => Response>) {
  const fetchMock = vi.fn<typeof fetch>((input) => {
    const url = urlOf(input)
    for (const [prefix, respond] of Object.entries(routes)) {
      if (url.startsWith(prefix)) return Promise.resolve(respond())
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('FavoritesPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('lists the starred items', async () => {
    stub({
      '/api/v1/favorites': () =>
        jsonResponse({
          nodes: [node({ id: 'n1', name: 'Quarterly Report.pdf', kind: 'file', favorite: true })],
          total: 1,
          limit: 100,
          offset: 0,
          has_more: false,
        }),
    })

    renderWithProviders(
      <ToastProvider>
        <FavoritesPage />
      </ToastProvider>,
    )

    expect(await screen.findByText('Quarterly Report.pdf')).toBeInTheDocument()
    expect(screen.getByText('1 item')).toBeInTheDocument()
  })

  // Favouriting is private, and the empty state should say so rather than
  // implying the user has simply not looked in the right place.
  it('explains what favourites are when there are none', async () => {
    stub({
      '/api/v1/favorites': () =>
        jsonResponse({ nodes: [], total: 0, limit: 100, offset: 0, has_more: false }),
    })

    renderWithProviders(
      <ToastProvider>
        <FavoritesPage />
      </ToastProvider>,
    )

    expect(await screen.findByText('No favourites yet')).toBeInTheDocument()
  })

  it('reports a failure rather than showing an empty list', async () => {
    stub({
      '/api/v1/favorites': () =>
        jsonResponse(
          { error: { code: 'internal_error', message: 'Something went wrong.' } },
          { status: 500 },
        ),
    })

    renderWithProviders(
      <ToastProvider>
        <FavoritesPage />
      </ToastProvider>,
    )

    expect(await screen.findByText('Could not load your favourites')).toBeInTheDocument()
    expect(screen.queryByText('No favourites yet')).not.toBeInTheDocument()
  })

  // The star is a toggle, and its pressed state is what a screen reader uses to
  // announce whether the item is already a favourite.
  it('unstars an item and refreshes the list', async () => {
    const user = userEvent.setup()
    let removed = false

    const fetchMock = stub({
      '/api/v1/nodes/n1/favorite': () => {
        removed = true
        return jsonResponse({ favorite: false })
      },
      '/api/v1/favorites': () =>
        removed
          ? jsonResponse({ nodes: [], total: 0, limit: 100, offset: 0, has_more: false })
          : jsonResponse({
              nodes: [node({ id: 'n1', name: 'Report.pdf', kind: 'file', favorite: true })],
              total: 1,
              limit: 100,
              offset: 0,
              has_more: false,
            }),
    })

    renderWithProviders(
      <ToastProvider>
        <FavoritesPage />
      </ToastProvider>,
    )

    const star = await screen.findByRole('button', { name: /remove report\.pdf from favourites/i })
    expect(star).toHaveAttribute('aria-pressed', 'true')

    await user.click(star)

    await waitFor(() => {
      expect(screen.getByText('No favourites yet')).toBeInTheDocument()
    })

    // "/favorite", not "/favorites" — the listing URL contains the singular
    // as a substring, and matching on it would find the wrong call.
    const call = fetchMock.mock.calls.find(([input]) =>
      urlOf(input).includes('/nodes/n1/favorite'),
    )
    expect(call?.[1]?.method).toBe('DELETE')
  })
})

describe('RecentPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  function entry(
    overrides: Partial<RecentEntry> & Pick<RecentEntry, 'id' | 'name' | 'action'>,
  ): RecentEntry {
    return {
      ...node({ id: overrides.id, name: overrides.name, kind: 'file' }),
      occurred_at: '2026-08-26T09:00:00Z',
      ...overrides,
    }
  }

  it('says what you did, in words a person would use', async () => {
    stub({
      '/api/v1/recent': () =>
        jsonResponse({
          items: [
            entry({ id: 'n1', name: 'Report.pdf', action: 'downloaded' }),
            entry({ id: 'n2', name: 'Notes.txt', action: 'viewed' }),
          ],
          total: 2,
          limit: 100,
          offset: 0,
          has_more: false,
        }),
    })

    renderWithProviders(
      <ToastProvider>
        <RecentPage />
      </ToastProvider>,
    )

    expect(await screen.findByText('Report.pdf')).toBeInTheDocument()
    // "Opened", not "viewed": the enum value is not what a person says.
    expect(screen.getByText(/Opened/)).toBeInTheDocument()
    expect(screen.getByText(/Downloaded/)).toBeInTheDocument()
  })

  it('shows an empty state when nothing has been touched', async () => {
    stub({
      '/api/v1/recent': () =>
        jsonResponse({ items: [], total: 0, limit: 100, offset: 0, has_more: false }),
    })

    renderWithProviders(
      <ToastProvider>
        <RecentPage />
      </ToastProvider>,
    )

    expect(await screen.findByText('Nothing here yet')).toBeInTheDocument()
  })
})
