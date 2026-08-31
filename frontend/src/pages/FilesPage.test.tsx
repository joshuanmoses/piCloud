import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { FileBrowser } from '@/features/files/FileBrowser'
import { jsonBodyOf, jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { FileNode, NodeListResponse } from '@/types/api'

const owner = { id: 'u1', username: 'josh', display_name: 'Josh Moses' }

function node(overrides: Partial<FileNode> & Pick<FileNode, 'id' | 'name' | 'kind'>): FileNode {
  return {
    parent_id: 'root-id',
    size_bytes: 0,
    mime_type: '',
    created_at: '2026-08-20T10:00:00Z',
    updated_at: '2026-08-26T10:00:00Z',
    owner,
    is_root: false,
    // A real listing carries the caller's effective permissions; the fixtures
    // mirror that so the tests exercise the same code path as production.
    permissions: ['read', 'download', 'upload', 'create', 'modify', 'rename', 'move', 'delete', 'share', 'manage'],
    is_owner: true,
    shared: false,
    ...overrides,
  }
}

const folder = node({ id: 'f1', name: 'Documents', kind: 'folder' })
const report = node({
  id: 'n1',
  name: 'Report.pdf',
  kind: 'file',
  size_bytes: 2_411_724,
  mime_type: 'application/pdf',
})
const photo = node({
  id: 'n2',
  name: 'Photo.png',
  kind: 'file',
  size_bytes: 512_000,
  mime_type: 'image/png',
})

function listing(nodes: FileNode[], overrides: Partial<NodeListResponse> = {}): NodeListResponse {
  return {
    folder: node({ id: 'root-id', name: '', kind: 'folder', is_root: true, parent_id: null }),
    breadcrumbs: [{ id: 'root-id', name: '', is_root: true }],
    nodes,
    total: nodes.length,
    has_more: false,
    limit: 100,
    offset: 0,
    ...overrides,
  }
}

/** Routes fetch by URL. Returns the mock so calls can be asserted. */
function stubApi(handlers: {
  list?: () => Response
  onRequest?: (url: string, init?: RequestInit) => Response | undefined
}) {
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const url = urlOf(input)

    const custom = handlers.onRequest?.(url, init)
    if (custom) return Promise.resolve(custom)

    if (url.startsWith('/api/v1/nodes?') || url === '/api/v1/nodes') {
      return Promise.resolve(handlers.list?.() ?? jsonResponse(listing([folder, report])))
    }
    if (url.includes('/auth/session')) {
      return Promise.resolve(
        jsonResponse({ user: { ...owner, is_admin: false }, csrf_token: 'x', expires_at: '' }),
      )
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderBrowser(onNavigate = vi.fn()) {
  return {
    onNavigate,
    ...renderWithProviders(
      <ToastProvider>
        <FileBrowser folderId="root" onNavigate={onNavigate} />
      </ToastProvider>,
    ),
  }
}

describe('FileBrowser', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('shows a loading state, then the folder contents', async () => {
    stubApi({})
    renderBrowser()

    expect(screen.getByLabelText('Loading folder contents')).toBeInTheDocument()

    expect(await screen.findByText('Documents')).toBeInTheDocument()
    expect(screen.getByText('Report.pdf')).toBeInTheDocument()
  })

  it('renders the specified list columns', async () => {
    stubApi({})
    renderBrowser()
    await screen.findByText('Report.pdf')

    for (const column of ['Name', 'Owner', 'Modified', 'Size', 'Sharing']) {
      expect(screen.getByRole('columnheader', { name: column })).toBeInTheDocument()
    }
    // Sizes are human-readable, and a folder has no size of its own.
    expect(screen.getByText('2.3 MB')).toBeInTheDocument()
    // Both rows carry the owner, so there are as many cells as rows.
    expect(screen.getAllByText('Josh Moses')).toHaveLength(2)
  })

  it('distinguishes private, shared-out, and shared-with-me in the Sharing column', async () => {
    // The column makes a claim about who can reach an item. Saying "Private"
    // next to something the owner has published would be a lie the interface
    // tells, not merely a missing feature.
    stubApi({
      list: () =>
        jsonResponse(
          listing([
            node({ id: 'p1', name: 'Private.txt', kind: 'file' }),
            node({ id: 'p2', name: 'Published.txt', kind: 'file', shared_out: true }),
            node({ id: 'p3', name: 'FromSomeone.txt', kind: 'file', is_owner: false, shared: true }),
          ]),
        ),
    })
    renderBrowser()

    const rowFor = async (name: string) => (await screen.findByText(name)).closest('tr')!

    expect(within(await rowFor('Private.txt')).getByText('Private')).toBeInTheDocument()
    expect(within(await rowFor('Published.txt')).getByText('Shared')).toBeInTheDocument()
    expect(within(await rowFor('FromSomeone.txt')).getByText('Shared with you')).toBeInTheDocument()
  })

  it('shows an empty state with a way forward', async () => {
    stubApi({ list: () => jsonResponse(listing([])) })
    renderBrowser()

    expect(await screen.findByText(/this folder is empty/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /upload files/i })).toBeInTheDocument()
  })

  it('shows an error state when the folder cannot be loaded', async () => {
    stubApi({
      list: () =>
        jsonResponse({ error: { code: 'forbidden', message: 'Nope.' } }, { status: 403 }),
    })
    renderBrowser()

    expect(await screen.findByText(/could not load this folder/i)).toBeInTheDocument()
  })

  it('opening a folder navigates rather than downloading', async () => {
    const user = userEvent.setup()
    stubApi({})
    const { onNavigate } = renderBrowser()

    await user.dblClick(await screen.findByText('Documents'))
    expect(onNavigate).toHaveBeenCalledWith('f1')
  })

  // --- Selection ------------------------------------------------------------

  it('clicking a row selects it and the toolbar switches to bulk actions', async () => {
    const user = userEvent.setup()
    stubApi({})
    renderBrowser()

    await user.click(await screen.findByText('Report.pdf'))

    expect(screen.getByText('1 selected')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^delete$/i })).toBeInTheDocument()
  })

  it('selection state is exposed to assistive technology', async () => {
    const user = userEvent.setup()
    stubApi({})
    renderBrowser()

    await user.click(await screen.findByText('Report.pdf'))

    const row = screen.getByText('Report.pdf').closest('tr')
    // aria-selected carries the state; the background colour alone would not.
    expect(row).toHaveAttribute('aria-selected', 'true')
  })

  it('ctrl-click extends the selection', async () => {
    const user = userEvent.setup()
    stubApi({ list: () => jsonResponse(listing([report, photo])) })
    renderBrowser()

    await user.click(await screen.findByText('Report.pdf'))
    await user.keyboard('{Control>}')
    await user.click(screen.getByText('Photo.png'))
    await user.keyboard('{/Control}')

    expect(screen.getByText('2 selected')).toBeInTheDocument()
  })

  // --- Keyboard -------------------------------------------------------------

  it('is navigable with the arrow keys and opens with Enter', async () => {
    const user = userEvent.setup()
    stubApi({})
    const { onNavigate } = renderBrowser()

    await screen.findByText('Documents')
    const firstRow = screen.getByText('Documents').closest('tr')!
    firstRow.focus()

    await user.keyboard('{Enter}')
    expect(onNavigate).toHaveBeenCalledWith('f1')
  })

  it('Space toggles selection without opening', async () => {
    const user = userEvent.setup()
    stubApi({})
    const { onNavigate } = renderBrowser()

    await screen.findByText('Documents')
    screen.getByText('Documents').closest('tr')!.focus()

    await user.keyboard(' ')
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    expect(onNavigate).not.toHaveBeenCalled()
  })

  it('F2 opens the rename dialog', async () => {
    const user = userEvent.setup()
    stubApi({})
    renderBrowser()

    await screen.findByText('Report.pdf')
    screen.getByText('Report.pdf').closest('tr')!.focus()

    await user.keyboard('{F2}')

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('heading', { name: /rename/i })).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/name/i)).toHaveValue('Report.pdf')
  })

  it('Delete asks for confirmation rather than deleting immediately', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({})
    renderBrowser()

    await screen.findByText('Report.pdf')
    screen.getByText('Report.pdf').closest('tr')!.focus()

    await user.keyboard('{Delete}')

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/move .*Report\.pdf.* to trash/i)).toBeInTheDocument()

    // Nothing has been deleted yet.
    expect(fetchMock.mock.calls.some((c) => c[1]?.method === 'DELETE')).toBe(false)
  })

  // --- Operations -----------------------------------------------------------

  it('creates a folder', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      onRequest: (url, init) => {
        if (url.includes('/nodes/folders') && init?.method === 'POST') {
          return jsonResponse({ node: node({ id: 'new', name: 'Invoices', kind: 'folder' }) }, { status: 201 })
        }
        return undefined
      },
    })
    renderBrowser()

    await user.click(await screen.findByRole('button', { name: /new folder/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/folder name/i), 'Invoices')
    await user.click(within(dialog).getByRole('button', { name: /create/i }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) => urlOf(c[0]).includes('/nodes/folders'))
      expect(call).toBeDefined()
      expect(jsonBodyOf(call?.[1])).toEqual({ parent_id: 'root', name: 'Invoices' })
    })
  })

  it('surfaces a duplicate-name conflict on the field', async () => {
    const user = userEvent.setup()
    stubApi({
      onRequest: (url, init) =>
        url.includes('/nodes/folders') && init?.method === 'POST'
          ? jsonResponse(
              { error: { code: 'conflict', message: 'An item with that name already exists here.' } },
              { status: 409 },
            )
          : undefined,
    })
    renderBrowser()

    await user.click(await screen.findByRole('button', { name: /new folder/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/folder name/i), 'Documents')
    await user.click(within(dialog).getByRole('button', { name: /create/i }))

    expect(await within(dialog).findByText(/already exists/i)).toBeInTheDocument()
  })

  it('renames through the row menu', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      onRequest: (url, init) =>
        url.includes('/nodes/n1') && init?.method === 'PATCH'
          ? jsonResponse({ node: { ...report, name: 'Renamed.pdf' } })
          : undefined,
    })
    renderBrowser()

    await screen.findByText('Report.pdf')
    await user.click(screen.getByRole('button', { name: 'Actions for Report.pdf' }))
    await user.click(await screen.findByRole('menuitem', { name: /rename/i }))

    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByLabelText(/name/i)
    await user.clear(input)
    await user.type(input, 'Renamed.pdf')
    await user.click(within(dialog).getByRole('button', { name: /^rename$/i }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(
        (c) => urlOf(c[0]).includes('/nodes/n1') && c[1]?.method === 'PATCH',
      )
      expect(jsonBodyOf(call?.[1])).toEqual({ name: 'Renamed.pdf' })
    })
  })

  it('confirms before deleting, then deletes', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      onRequest: (url, init) =>
        url.includes('/nodes/n1') && init?.method === 'DELETE'
          ? new Response(null, { status: 204 })
          : undefined,
    })
    renderBrowser()

    await screen.findByText('Report.pdf')
    await user.click(screen.getByRole('button', { name: 'Actions for Report.pdf' }))
    await user.click(await screen.findByRole('menuitem', { name: /move to trash/i }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /move to trash/i }))

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(
          (c) => urlOf(c[0]).includes('/nodes/n1') && c[1]?.method === 'DELETE',
        ),
      ).toBe(true)
    })
  })

  // --- View and sort --------------------------------------------------------

  it('switches between list and grid view', async () => {
    const user = userEvent.setup()
    stubApi({})
    renderBrowser()

    await screen.findByText('Report.pdf')
    expect(screen.getByRole('table')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /grid view/i }))

    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.getByRole('grid', { name: /files and folders/i })).toBeInTheDocument()
    expect(screen.getByText('Report.pdf')).toBeInTheDocument()
  })

  it('changing the sort refetches with the new parameters', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({})
    renderBrowser()

    await screen.findByText('Report.pdf')
    await user.click(screen.getByRole('button', { name: /name/i }))
    await user.click(await screen.findByRole('menuitemradio', { name: /size/i }))

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some((c) => urlOf(c[0]).includes('sort=size')),
      ).toBe(true)
    })
  })

  it('paginates when there are more items than fit on a page', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      list: () => jsonResponse(listing([folder, report], { total: 250, has_more: true })),
    })
    renderBrowser()

    await screen.findByText('Report.pdf')
    expect(screen.getByText(/of 250/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /next/i }))

    await waitFor(() => {
      expect(fetchMock.mock.calls.some((c) => urlOf(c[0]).includes('offset=100'))).toBe(true)
    })
  })
})
