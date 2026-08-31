import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { AdminGroupsPage } from '@/pages/AdminGroupsPage'
import { jsonBodyOf, jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { Group } from '@/types/api'

function group(overrides: Partial<Group> & Pick<Group, 'id' | 'name'>): Group {
  return {
    description: '',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    member_count: 0,
    ...overrides,
  }
}

function stub(handlers: {
  groups?: () => Response
  detail?: () => Response
  onRequest?: (url: string, init?: RequestInit) => Response | undefined
}) {
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const url = urlOf(input)
    const custom = handlers.onRequest?.(url, init)
    if (custom) return Promise.resolve(custom)

    if (url.includes('/auth/session')) {
      return Promise.resolve(
        jsonResponse({
          user: { id: 'u1', username: 'boss', display_name: 'Boss', is_admin: true },
          csrf_token: 'x',
          expires_at: '',
        }),
      )
    }
    // A detail URL has a segment after /groups/.
    if (/\/api\/v1\/groups\/[^/?]+$/.test(url)) {
      return Promise.resolve(
        handlers.detail?.() ??
          jsonResponse({
            group: group({ id: 'g1', name: 'Accounting', member_count: 1 }),
            members: [
              {
                user: { id: 'u2', username: 'ana', display_name: 'Ana Silva' },
                role: 'member',
                added_at: '2026-01-01T00:00:00Z',
              },
            ],
          }),
      )
    }
    if (url.startsWith('/api/v1/groups')) {
      return Promise.resolve(
        handlers.groups?.() ??
          jsonResponse({
            groups: [
              group({ id: 'g1', name: 'Accounting', member_count: 3 }),
              group({ id: 'g2', name: 'Household', member_count: 0 }),
            ],
          }),
      )
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function render() {
  return renderWithProviders(
    <ToastProvider>
      <AdminGroupsPage />
    </ToastProvider>,
    { route: '/admin/groups' },
  )
}

describe('AdminGroupsPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('lists groups with their membership counts', async () => {
    stub({})
    render()

    expect(await screen.findByText('Accounting')).toBeInTheDocument()
    expect(screen.getByText(/3 members/)).toBeInTheDocument()
    expect(screen.getByText('Household')).toBeInTheDocument()
  })

  // An empty group grants nobody anything, which is not obvious from a count
  // of zero sitting next to a share that appears to work.
  it('says plainly that an empty group grants nobody anything', async () => {
    const user = userEvent.setup()
    stub({
      detail: () =>
        jsonResponse({ group: group({ id: 'g2', name: 'Household' }), members: [] }),
    })
    render()

    await user.click(await screen.findByText('Household'))
    expect(await screen.findByText(/sharing with it grants nobody anything/i)).toBeInTheDocument()
  })

  it('shows a group’s members when it is opened', async () => {
    const user = userEvent.setup()
    stub({})
    render()

    await user.click(await screen.findByText('Accounting'))
    expect(await screen.findByText('Ana Silva')).toBeInTheDocument()
    expect(screen.getByText('ana')).toBeInTheDocument()
  })

  // The consequence people do not expect: deleting a group silently removes
  // access for everyone who reached a folder through it.
  it('warns that deleting a group removes access nobody will be told about', async () => {
    const user = userEvent.setup()
    stub({})
    render()

    await screen.findByText('Accounting')
    await user.click(screen.getByRole('button', { name: /delete the Accounting group/i }))

    expect(await screen.findByText('Delete the Accounting group?')).toBeInTheDocument()
    expect(screen.getByText(/lose access immediately/i)).toBeInTheDocument()
    expect(screen.getByText(/nothing will tell them why/i)).toBeInTheDocument()
    // And it says what is *not* affected, because that is the other half of
    // the question.
    expect(screen.getByText(/accounts themselves are not affected/i)).toBeInTheDocument()
  })

  it('sends nothing until the deletion is confirmed', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({})
    render()

    await screen.findByText('Accounting')
    await user.click(screen.getByRole('button', { name: /delete the Accounting group/i }))

    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
  })

  it('creates a group', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({
      onRequest: (url, init) =>
        url.endsWith('/api/v1/groups') && init?.method === 'POST'
          ? jsonResponse({ group: group({ id: 'g3', name: 'Photos' }) }, { status: 201 })
          : undefined,
    })
    render()

    await screen.findByText('Accounting')
    await user.click(screen.getByRole('button', { name: /new group/i }))

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'Photos')
    await user.click(within(dialog).getByRole('button', { name: /create group/i }))

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        ([input, init]) => urlOf(input).endsWith('/api/v1/groups') && init?.method === 'POST',
      )
      expect(posted).toBeDefined()
      expect(jsonBodyOf(posted?.[1])).toMatchObject({ name: 'Photos' })
    })
  })

  it('adds somebody to a group by username', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({
      onRequest: (url, init) =>
        url.includes('/members') && init?.method === 'POST'
          ? new Response(null, { status: 204 })
          : undefined,
    })
    render()

    await user.click(await screen.findByText('Accounting'))
    const field = await screen.findByLabelText(/add somebody to Accounting/i)
    await user.type(field, 'bob')
    await user.click(screen.getByRole('button', { name: /^add$/i }))

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        ([input, init]) => urlOf(input).includes('/members') && init?.method === 'POST',
      )
      expect(posted).toBeDefined()
      expect(jsonBodyOf(posted?.[1])).toMatchObject({ username: 'bob' })
    })
  })

  it('reports a failure rather than showing no groups', async () => {
    stub({
      groups: () =>
        jsonResponse(
          { error: { code: 'internal_error', message: 'Something went wrong.' } },
          { status: 500 },
        ),
    })
    render()

    expect(await screen.findByText('Could not load the groups')).toBeInTheDocument()
    expect(screen.queryByText('No groups yet')).not.toBeInTheDocument()
  })
})
