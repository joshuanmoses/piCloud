import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { VersionHistory } from '@/features/versions/VersionHistory'
import { jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { FileNode, FileVersion, Permission, VersionListResponse } from '@/types/api'

const owner = { id: 'u1', username: 'josh', display_name: 'Josh Moses' }

const everything: Permission[] = [
  'read', 'download', 'upload', 'create', 'modify', 'rename', 'move', 'delete', 'share', 'manage',
]

function file(permissions: Permission[] = everything): FileNode {
  return {
    id: 'n1',
    parent_id: 'root-id',
    kind: 'file',
    name: 'Report.txt',
    size_bytes: 120,
    mime_type: 'text/plain',
    created_at: '2026-08-20T10:00:00Z',
    updated_at: '2026-08-27T10:00:00Z',
    owner,
    is_root: false,
    permissions,
    is_owner: true,
    shared: false,
  }
}

function version(overrides: Partial<FileVersion> & Pick<FileVersion, 'id' | 'version'>): FileVersion {
  return {
    size_bytes: 100,
    mime_type: 'text/plain',
    name: 'Report.txt',
    created_at: '2026-08-26T10:00:00Z',
    created_by: owner,
    reason: 'replaced',
    reason_label: 'Replaced',
    ...overrides,
  }
}

function history(versions: FileVersion[]): VersionListResponse {
  return {
    versions,
    total: versions.length,
    current: {
      size_bytes: 120,
      mime_type: 'text/plain',
      name: 'Report.txt',
      updated_at: '2026-08-27T10:00:00Z',
    },
  }
}

function stub(handlers: {
  list?: () => Response
  onRequest?: (url: string, init?: RequestInit) => Response | undefined
}) {
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const url = urlOf(input)
    const custom = handlers.onRequest?.(url, init)
    if (custom) return Promise.resolve(custom)
    if (url.includes('/versions')) {
      return Promise.resolve(
        handlers.list?.() ?? jsonResponse(history([version({ id: 'v1', version: 1 })])),
      )
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function render(node = file()) {
  return renderWithProviders(
    <ToastProvider>
      <VersionHistory node={node} />
    </ToastProvider>,
  )
}

describe('VersionHistory', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  // "Which one am I looking at now" is the first question anybody asks of a
  // version list. Leaving it implicit makes the numbering read as off-by-one.
  it('marks the current state before the earlier ones', async () => {
    stub({
      list: () =>
        jsonResponse(
          history([
            version({ id: 'v2', version: 2 }),
            version({ id: 'v1', version: 1 }),
          ]),
        ),
    })
    render()

    expect(await screen.findByText('Now')).toBeInTheDocument()

    const entries = screen.getAllByRole('listitem')
    expect(within(entries[0] as HTMLElement).getByText('Now')).toBeInTheDocument()
    expect(within(entries[1] as HTMLElement).getByText('v2')).toBeInTheDocument()
    expect(within(entries[2] as HTMLElement).getByText('v1')).toBeInTheDocument()
  })

  it('explains what makes a version when there are none', async () => {
    stub({ list: () => jsonResponse(history([])) })
    render()

    expect(await screen.findByText(/No earlier versions yet/)).toBeInTheDocument()
    expect(screen.getByText(/each time the file is replaced/)).toBeInTheDocument()
  })

  it('shows who made a version and why', async () => {
    stub({
      list: () =>
        jsonResponse(
          history([
            version({ id: 'v1', version: 1, reason: 'restored', reason_label: 'Superseded by a restore' }),
          ]),
        ),
    })
    render()

    expect(await screen.findByText('Superseded by a restore')).toBeInTheDocument()
    expect(screen.getByText(/Josh Moses/)).toBeInTheDocument()
  })

  it('restores a version and says the change can be undone', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({
      onRequest: (url, init) =>
        url.includes('/restore') && init?.method === 'POST'
          ? jsonResponse({ node: file() })
          : undefined,
    })
    render()

    await user.click(await screen.findByRole('button', { name: /restore version 1/i }))

    await waitFor(() => {
      expect(screen.getByText('Restored version 1')).toBeInTheDocument()
    })
    // The reassurance matters: a person restoring the wrong thing needs to know
    // immediately that they have not destroyed anything.
    expect(screen.getByText(/can be undone/i)).toBeInTheDocument()

    const restored = fetchMock.mock.calls.some(([input]) => urlOf(input).includes('/restore'))
    expect(restored).toBe(true)
  })

  // Deleting a version is the one action here that destroys data.
  it('asks before deleting a version, and says why that one is different', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({})
    render()

    await user.click(await screen.findByRole('button', { name: /delete version 1/i }))

    expect(await screen.findByText('Delete version 1?')).toBeInTheDocument()
    expect(screen.getByText(/unlike a restore, which can be undone/i)).toBeInTheDocument()

    // Nothing has been sent yet.
    const deleted = fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')
    expect(deleted).toBe(false)
  })

  it('sends the delete once confirmed', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({
      onRequest: (_url, init) =>
        init?.method === 'DELETE' ? new Response(null, { status: 204 }) : undefined,
    })
    render()

    await user.click(await screen.findByRole('button', { name: /delete version 1/i }))
    await user.click(await screen.findByRole('button', { name: /delete permanently/i }))

    await waitFor(() => {
      const deleted = fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')
      expect(deleted).toBe(true)
    })
  })

  // The server refuses these regardless; disabling them only avoids offering an
  // action that would be rejected.
  it('does not offer restore or delete to somebody who may only read', async () => {
    stub({})
    render(file(['read', 'download']))

    await screen.findByText('v1')
    expect(screen.getByRole('button', { name: /restore version 1/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /delete version 1/i })).toBeDisabled()
    // Downloading it is still fine.
    expect(screen.getByRole('link', { name: /download version 1/i })).toBeInTheDocument()
  })

  it('reports a failure rather than showing an empty history', async () => {
    stub({
      list: () =>
        jsonResponse(
          { error: { code: 'internal_error', message: 'Something went wrong.' } },
          { status: 500 },
        ),
    })
    render()

    expect(await screen.findByText('Could not load the version history')).toBeInTheDocument()
    expect(screen.queryByText(/No earlier versions yet/)).not.toBeInTheDocument()
  })

  it('renders nothing at all for a folder', () => {
    stub({})
    const folder = { ...file(), kind: 'folder' as const, name: 'Project' }
    const { container } = renderWithProviders(
      <ToastProvider>
        <VersionHistory node={folder} />
      </ToastProvider>,
    )
    expect(container.querySelector('ul')).toBeNull()
  })
})
