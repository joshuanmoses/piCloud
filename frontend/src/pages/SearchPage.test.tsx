import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { HeaderSearch } from '@/features/productivity/HeaderSearch'
import { SearchPage } from '@/pages/SearchPage'
import { jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { SearchResult } from '@/types/api'

const owner = { id: 'u1', username: 'josh', display_name: 'Josh Moses' }

function result(overrides: Partial<SearchResult> & Pick<SearchResult, 'id' | 'name'>): SearchResult {
  return {
    parent_id: 'root-id',
    kind: 'file',
    size_bytes: 2048,
    mime_type: 'application/pdf',
    created_at: '2026-08-20T10:00:00Z',
    updated_at: '2026-08-26T10:00:00Z',
    owner,
    is_root: false,
    permissions: ['read', 'download'],
    is_owner: true,
    shared: false,
    score: 0.8,
    ...overrides,
  }
}

function stub(results: SearchResult[], total = results.length) {
  const fetchMock = vi.fn<typeof fetch>((input) => {
    const url = urlOf(input)
    if (url.startsWith('/api/v1/search')) {
      return Promise.resolve(jsonResponse({ results, total, query: 'report' }))
    }
    if (url.startsWith('/api/v1/tags')) {
      return Promise.resolve(jsonResponse({ tags: [] }))
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderSearch(route: string) {
  return renderWithProviders(
    <ToastProvider>
      <SearchPage />
    </ToastProvider>,
    { route },
  )
}

describe('SearchPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  // The query lives in the URL, so a search is a link.
  it('runs the query from the URL and shows the matches', async () => {
    const fetchMock = stub([result({ id: 'n1', name: 'Quarterly Report.pdf' })])
    renderSearch('/search?q=report')

    expect(await screen.findByText('Quarterly Report.pdf')).toBeInTheDocument()
    expect(screen.getByText(/1 match for “report”/)).toBeInTheDocument()
    expect(urlOf(fetchMock.mock.calls[0]?.[0])).toContain('q=report')
  })

  // A one-character query would match almost everything; the server refuses it
  // and the interface should say so rather than issuing the request.
  it('does not search until the query is long enough', async () => {
    const fetchMock = stub([])
    renderSearch('/search?q=r')

    expect(await screen.findByText(/Type at least 2 characters/)).toBeInTheDocument()

    const searched = fetchMock.mock.calls.some(([input]) =>
      urlOf(input).startsWith('/api/v1/search'),
    )
    expect(searched).toBe(false)
  })

  it('says plainly when nothing matches', async () => {
    stub([])
    renderSearch('/search?q=report')

    expect(await screen.findByText('No matches')).toBeInTheDocument()
  })

  it('reports a failed search rather than showing it as empty', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input) => {
        if (urlOf(input).startsWith('/api/v1/search')) {
          return Promise.resolve(
            jsonResponse(
              { error: { code: 'internal_error', message: 'Something went wrong.' } },
              { status: 500 },
            ),
          )
        }
        return Promise.resolve(jsonResponse({ tags: [] }))
      }),
    )

    renderSearch('/search?q=report')

    expect(await screen.findByText('The search could not be completed')).toBeInTheDocument()
    expect(screen.queryByText('No matches')).not.toBeInTheDocument()
  })

  // Filters belong in the URL for the same reason the query does.
  it('puts a chosen filter in the request and in the URL', async () => {
    const user = userEvent.setup()
    const fetchMock = stub([result({ id: 'n1', name: 'Report.pdf' })])
    renderSearch('/search?q=report')

    await screen.findByText('Report.pdf')

    await user.click(screen.getByRole('button', { name: /filters/i }))
    await user.click(screen.getByRole('button', { name: 'Folders' }))

    await waitFor(() => {
      const searched = fetchMock.mock.calls.some(([input]) =>
        urlOf(input).includes('kind=folder'),
      )
      expect(searched).toBe(true)
    })
  })

  it('marks an item reached through a share', async () => {
    stub([
      result({
        id: 'n1',
        name: 'Team Roster.csv',
        shared: true,
        is_owner: false,
        owner: { id: 'u2', username: 'ana', display_name: 'Ana Silva' },
      }),
    ])
    renderSearch('/search?q=roster')

    expect(await screen.findByText('Team Roster.csv')).toBeInTheDocument()
    expect(screen.getByText('Ana Silva')).toBeInTheDocument()
  })

  // The count comes from the same query as the rows, so it counts only what
  // the caller may see. Rendering it is how the user knows there is more.
  it('shows the total when it exceeds the page', async () => {
    stub([result({ id: 'n1', name: 'Report.pdf' })], 42)
    renderSearch('/search?q=report')

    expect(await screen.findByText(/42 matches for “report”/)).toBeInTheDocument()
  })
})

describe('HeaderSearch', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('offers a search box on an ordinary page', () => {
    renderWithProviders(<HeaderSearch />, { route: '/files' })
    expect(screen.getByRole('searchbox', { name: /search files/i })).toBeInTheDocument()
  })

  // The search page carries its own box, with the filters attached to it.
  // Two identical inputs on screen would leave the user guessing which one the
  // filters belong to.
  it('renders nothing on the search page itself', () => {
    renderWithProviders(<HeaderSearch />, { route: '/search?q=report' })
    expect(screen.queryByRole('searchbox')).not.toBeInTheDocument()
  })

  it('navigates to a search URL on submit, so the query is a link', async () => {
    const user = userEvent.setup()
    renderWithProviders(<HeaderSearch />, { route: '/files' })

    const box = screen.getByRole('searchbox', { name: /search files/i })
    await user.type(box, 'quarterly report{Enter}')

    await waitFor(() => {
      // MemoryRouter has no window.location, so the assertion is that the box
      // is now on the search page — where it renders nothing.
      expect(screen.queryByRole('searchbox')).not.toBeInTheDocument()
    })
  })

  it('ignores an empty submit rather than navigating to a blank search', async () => {
    const user = userEvent.setup()
    renderWithProviders(<HeaderSearch />, { route: '/files' })

    const box = screen.getByRole('searchbox', { name: /search files/i })
    await user.type(box, '   {Enter}')

    expect(screen.getByRole('searchbox', { name: /search files/i })).toBeInTheDocument()
  })

  // The shortcut every implementation gets wrong once: "/" must not steal a
  // keystroke from someone typing a filename into another field.
  it('focuses on "/" but not while the user is typing elsewhere', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <>
        <input aria-label="Another field" />
        <HeaderSearch />
      </>,
      { route: '/files' },
    )

    const other = screen.getByLabelText('Another field')
    const box = screen.getByRole('searchbox', { name: /search files/i })

    await user.click(other)
    await user.keyboard('/')
    expect(other).toHaveFocus()
    expect(other).toHaveValue('/')

    other.blur()
    await user.keyboard('/')
    expect(box).toHaveFocus()
  })
})
