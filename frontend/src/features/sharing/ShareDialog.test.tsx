import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { ShareDialog } from '@/features/sharing/ShareDialog'
import { jsonBodyOf, jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { FileNode, Permission, Share } from '@/types/api'

const vocabulary = {
  permissions: ['read', 'download', 'upload', 'create', 'modify', 'rename', 'move', 'delete', 'share', 'manage'],
  roles: [
    { name: 'viewer', permissions: ['read', 'download'], description: 'Can open and download, but not change anything.' },
    { name: 'contributor', permissions: ['read', 'download', 'upload', 'create'], description: 'Can add new files and folders, but not change or remove what is already there.' },
    { name: 'editor', permissions: ['read', 'download', 'upload', 'create', 'modify', 'rename', 'move', 'delete'], description: 'Can add, change, and remove content, but cannot share it with anyone else.' },
    { name: 'manager', permissions: ['read', 'download', 'upload', 'create', 'modify', 'rename', 'move', 'delete', 'share', 'manage'], description: 'Can do everything an editor can, and also share it and manage who has access.' },
  ],
  enabled: true,
}

const everything: Permission[] = [
  'read', 'download', 'upload', 'create', 'modify', 'rename', 'move', 'delete', 'share', 'manage',
]

function node(permissions: Permission[] = everything): FileNode {
  return {
    id: 'n1',
    parent_id: 'p1',
    kind: 'folder',
    name: 'Team Documents',
    size_bytes: 0,
    created_at: '2026-08-20T10:00:00Z',
    updated_at: '2026-08-26T10:00:00Z',
    is_root: false,
    permissions,
    is_owner: true,
    shared: false,
  }
}

const existingShare: Share = {
  id: 's1',
  node: { id: 'n1', name: 'Team Documents', kind: 'folder', size_bytes: 0 },
  subject_type: 'user',
  user: { id: 'u2', username: 'bob', display_name: 'Bob Smith' },
  permissions: ['read', 'download'],
  role: 'viewer',
  deny: false,
  inherit: true,
  created_at: '2026-08-25T10:00:00Z',
}

function stubApi(overrides: { shares?: Share[]; onRequest?: (url: string, init?: RequestInit) => Response | undefined } = {}) {
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const url = urlOf(input)

    const custom = overrides.onRequest?.(url, init)
    if (custom) return Promise.resolve(custom)

    if (url.includes('/sharing/vocabulary')) return Promise.resolve(jsonResponse(vocabulary))
    if (url.includes('/shares') && url.includes('/nodes/')) {
      return Promise.resolve(jsonResponse({ shares: overrides.shares ?? [] }))
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function render(target: FileNode) {
  return renderWithProviders(
    <ToastProvider>
      <ShareDialog node={target} open onOpenChange={() => {}} />
    </ToastProvider>,
  )
}

describe('ShareDialog', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('offers the roles the server publishes, not a hardcoded list', async () => {
    stubApi()
    render(node())

    // Each role from the vocabulary appears, named by the role itself and
    // described by its explanatory sentence.
    for (const role of vocabulary.roles) {
      const option = await screen.findByRole('radio', { name: role.name })
      expect(option).toBeInTheDocument()
      expect(option).toHaveAccessibleDescription(role.description)
    }
  })

  it('shares with a person', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      onRequest: (url, init) =>
        url.endsWith('/api/v1/shares') && init?.method === 'POST'
          ? jsonResponse({ share: existingShare }, { status: 201 })
          : undefined,
    })
    render(node())

    await user.type(await screen.findByLabelText(/username/i), 'bob')
    await user.click(screen.getByRole('radio', { name: 'editor' }))
    await user.click(screen.getByRole('button', { name: /^share$/i }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(
        (c) => urlOf(c[0]).endsWith('/api/v1/shares') && c[1]?.method === 'POST',
      )
      expect(call).toBeDefined()
      expect(jsonBodyOf(call?.[1])).toEqual({
        node_id: 'n1',
        username: 'bob',
        permissions: ['editor'],
      })
    })
  })

  it('switches between sharing with a person and with a group', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      onRequest: (url, init) =>
        url.endsWith('/api/v1/shares') && init?.method === 'POST'
          ? jsonResponse({ share: existingShare }, { status: 201 })
          : undefined,
    })
    render(node())

    await user.click(await screen.findByRole('radio', { name: /a group/i }))
    await user.type(screen.getByLabelText(/group name/i), 'Accounting')
    await user.click(screen.getByRole('button', { name: /^share$/i }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(
        (c) => urlOf(c[0]).endsWith('/api/v1/shares') && c[1]?.method === 'POST',
      )
      expect(jsonBodyOf(call?.[1])).toMatchObject({ group_name: 'Accounting' })
    })
  })

  it('lists who already has access', async () => {
    stubApi({ shares: [existingShare] })
    render(node())

    const entry = (await screen.findByText('Bob Smith')).closest('li')
    expect(entry).not.toBeNull()
    // Scoped to the access list: "viewer" also names a role option above.
    expect(within(entry as HTMLElement).getByText('viewer')).toBeInTheDocument()
    expect(within(entry as HTMLElement).getByText('bob')).toBeInTheDocument()
  })

  it('revokes access', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      shares: [existingShare],
      onRequest: (url, init) =>
        url.includes('/shares/s1') && init?.method === 'DELETE'
          ? new Response(null, { status: 204 })
          : undefined,
    })
    render(node())

    await user.click(await screen.findByRole('button', { name: /remove access for Bob Smith/i }))

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(
          (c) => urlOf(c[0]).includes('/shares/s1') && c[1]?.method === 'DELETE',
        ),
      ).toBe(true)
    })
  })

  // Sharing is not transitive: a viewer holds no right to pass access on.
  it('explains why a user without share permission cannot share', async () => {
    stubApi()
    render(node(['read', 'download']))

    expect(await screen.findByText(/you cannot share this item/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^share$/i })).not.toBeInTheDocument()
  })

  // Without Manage, the list is limited to the caller's own entry, and the
  // revoke control must not be offered.
  it('hides the revoke control from somebody who cannot manage access', async () => {
    stubApi({ shares: [existingShare] })
    render(node(['read', 'download', 'share']))

    expect(await screen.findByText('Bob Smith')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /remove access/i })).not.toBeInTheDocument()
    // And the heading says whose access is shown.
    expect(screen.getByRole('heading', { name: /your access/i })).toBeInTheDocument()
  })

  it('reports that sharing is switched off on the server', async () => {
    stubApi({
      onRequest: (url) =>
        url.includes('/sharing/vocabulary')
          ? jsonResponse({ ...vocabulary, enabled: false })
          : undefined,
    })
    render(node())

    expect(await screen.findByText(/sharing is switched off/i)).toBeInTheDocument()
  })

  it('surfaces an unknown recipient on the field', async () => {
    const user = userEvent.setup()
    stubApi({
      onRequest: (url, init) =>
        url.endsWith('/api/v1/shares') && init?.method === 'POST'
          ? jsonResponse(
              {
                error: {
                  code: 'validation_failed',
                  message: 'No user or group with that name was found.',
                  fields: { username: 'Check the spelling and try again.' },
                },
              },
              { status: 422 },
            )
          : undefined,
    })
    render(node())

    await user.type(await screen.findByLabelText(/username/i), 'nosuchperson')
    await user.click(screen.getByRole('button', { name: /^share$/i }))

    expect(await screen.findByText(/check the spelling/i)).toBeInTheDocument()
  })

  it('shows an empty state when nothing is shared yet', async () => {
    stubApi({ shares: [] })
    render(node())

    expect(await screen.findByText(/not shared with anyone yet/i)).toBeInTheDocument()
  })
})

/**
 * Taking access away.
 *
 * A denial is the only way to express "everyone in Accounting may edit this,
 * except Bob". It is also the one control in this dialog that *removes* access
 * rather than granting it, so the interface has to be unambiguous about which
 * one is happening.
 */
describe('ShareDialog denials', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  // Mirrors the server's rule: denying needs manage, not share. Offering it
  // otherwise would present an action that would be refused.
  it('offers the option only to somebody who may manage access', async () => {
    stubApi()
    render(node(['read', 'download', 'share']))

    // The form is present — they may share — but the denial option is not.
    expect(await screen.findByRole('button', { name: /^share$/i })).toBeInTheDocument()
    expect(screen.queryByText(/take access away instead/i)).not.toBeInTheDocument()
  })

  it('offers the option to somebody who may manage access', async () => {
    stubApi()
    render(node())

    expect(await screen.findByText(/take access away instead/i)).toBeInTheDocument()
    // And explains the one case it does not cover.
    expect(screen.getByText(/owner cannot be denied/i)).toBeInTheDocument()
  })

  it('sends deny and changes the button so the two are not confused', async () => {
    const user = userEvent.setup()
    const fetchMock = stubApi({
      onRequest: (url, init) =>
        url.endsWith('/api/v1/shares') && init?.method === 'POST'
          ? jsonResponse({ share: { ...existingShare, deny: true } }, { status: 201 })
          : undefined,
    })
    render(node())

    await user.type(await screen.findByLabelText(/username/i), 'bob')
    await user.click(screen.getByLabelText(/take access away instead/i))

    // The button says what it will do, and stops saying "Share".
    expect(screen.getByRole('button', { name: /take access away/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^share$/i })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /take access away/i }))

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        ([input, init]) => urlOf(input).endsWith('/api/v1/shares') && init?.method === 'POST',
      )
      expect(posted).toBeDefined()
      expect(jsonBodyOf(posted?.[1])).toMatchObject({ deny: true })
    })
  })

  // Showing "editor" against somebody who has been *denied* editing would say
  // the opposite of what the entry does.
  it('shows a denial in the access list as a restriction, not a grant', async () => {
    stubApi({
      shares: [
        { ...existingShare, id: 's2', deny: true, role: 'editor', permissions: ['modify'] },
      ],
    })
    render(node())

    // "No editor", not "editor" — the badge must not read as a grant. (The
    // form's own role options legitimately say "Editor", so the assertion is
    // on the badge's own text rather than on the word's absence.)
    expect(await screen.findByText('No editor')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /remove the restriction on Bob Smith/i }),
    ).toBeInTheDocument()
  })

  it('describes removing a grant and removing a restriction differently', async () => {
    stubApi({ shares: [existingShare] })
    render(node())

    await screen.findByText('Bob Smith')
    expect(screen.getByRole('button', { name: /remove access for Bob Smith/i })).toBeInTheDocument()
  })
})
