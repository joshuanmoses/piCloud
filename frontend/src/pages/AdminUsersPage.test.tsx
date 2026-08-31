import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { AdminUsersPage } from '@/pages/AdminUsersPage'
import { jsonBodyOf, jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { AdminUser } from '@/types/api'

const self = {
  id: 'u-self',
  username: 'boss',
  display_name: 'The Boss',
  is_enabled: true,
  is_admin: true,
  quota_bytes: null,
  used_bytes: 0,
  created_at: '2026-01-01T00:00:00Z',
  must_change_password: false,
}

function account(overrides: Partial<AdminUser> & Pick<AdminUser, 'id' | 'username'>): AdminUser {
  return {
    // A distinct display name, so a test asserting on the username is not
    // ambiguous with the row's heading.
    display_name: `${overrides.username.toUpperCase()} Person`,
    is_enabled: true,
    is_admin: false,
    is_locked: false,
    quota_bytes: null,
    used_bytes: 1024,
    created_at: '2026-01-01T00:00:00Z',
    must_change_password: false,
    ...overrides,
  }
}

function stub(handlers: {
  users?: () => Response
  onRequest?: (url: string, init?: RequestInit) => Response | undefined
}) {
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const url = urlOf(input)
    const custom = handlers.onRequest?.(url, init)
    if (custom) return Promise.resolve(custom)

    if (url.includes('/auth/session')) {
      return Promise.resolve(
        jsonResponse({ user: { ...self, is_admin: true }, csrf_token: 'x', expires_at: '' }),
      )
    }
    if (url.startsWith('/api/v1/admin/users')) {
      return Promise.resolve(
        handlers.users?.() ??
          jsonResponse({
            users: [
              account({ id: 'u1', username: 'josh', used_bytes: 5_000_000 }),
              account({ id: 'u2', username: 'ana', is_admin: true }),
            ],
            total: 2,
            limit: 50,
            offset: 0,
            has_more: false,
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
      <AdminUsersPage />
    </ToastProvider>,
    { route: '/admin/users' },
  )
}

describe('AdminUsersPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('lists accounts with their status and storage', async () => {
    stub({})
    render()

    expect(await screen.findByText('JOSH Person')).toBeInTheDocument()
    expect(screen.getByText('josh')).toBeInTheDocument()
    expect(screen.getByText('ANA Person')).toBeInTheDocument()
    expect(screen.getByText('Administrator')).toBeInTheDocument()
    expect(screen.getByText(/4\.8 MB of unlimited/)).toBeInTheDocument()
  })

  // The page states the boundary because it is the thing people assume wrongly.
  it('says plainly that administrators cannot read files', async () => {
    stub({})
    render()

    expect(await screen.findByText(/cannot read anybody’s files/i)).toBeInTheDocument()
  })

  // "Disable" and "delete" are easy to confuse, and only one is recoverable.
  it('distinguishes disabling from deleting when asking', async () => {
    const user = userEvent.setup()
    stub({})
    render()

    await screen.findByText('JOSH Person')
    await user.click(screen.getByRole('button', { name: /^delete josh$/i }))

    expect(await screen.findByText('Delete josh?')).toBeInTheDocument()
    expect(screen.getByText(/Nothing brings it back/)).toBeInTheDocument()
    // And it points at the recoverable alternative.
    expect(screen.getByText(/disable the account instead/i)).toBeInTheDocument()
  })

  it('sends nothing until the deletion is confirmed', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({})
    render()

    await screen.findByText('JOSH Person')
    await user.click(screen.getByRole('button', { name: /^delete josh$/i }))

    const deleted = fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')
    expect(deleted).toBe(false)
  })

  // The server refuses these too; disabling them avoids offering an action that
  // would be rejected, and the tooltip says why.
  it('will not let an administrator act on their own account', async () => {
    stub({
      users: () =>
        jsonResponse({
          users: [account({ id: 'u-self', username: 'boss', is_admin: true })],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        }),
    })
    render()

    await screen.findByText('BOSS Person')
    expect(screen.getByRole('button', { name: /^delete boss$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^disable boss$/i })).toBeDisabled()
    expect(
      screen.getByRole('button', { name: /remove administrator access from boss/i }),
    ).toBeDisabled()
  })

  it('creates an account that must choose its own password by default', async () => {
    const user = userEvent.setup()
    const fetchMock = stub({
      onRequest: (url, init) =>
        url.endsWith('/admin/users') && init?.method === 'POST'
          ? jsonResponse({ user: account({ id: 'u3', username: 'newcomer' }) }, { status: 201 })
          : undefined,
    })
    render()

    await screen.findByText('JOSH Person')
    await user.click(screen.getByRole('button', { name: /new account/i }))

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/username/i), 'newcomer')
    await user.type(within(dialog).getByLabelText(/temporary password/i), 'a temporary passphrase')
    await user.click(within(dialog).getByRole('button', { name: /create account/i }))

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        ([input, init]) => urlOf(input).endsWith('/admin/users') && init?.method === 'POST',
      )
      expect(posted).toBeDefined()
      expect(jsonBodyOf(posted?.[1])).toMatchObject({
        username: 'newcomer',
        must_change_password: true,
        is_admin: false,
      })
    })
  })

  it('warns that a password reset ends every session', async () => {
    const user = userEvent.setup()
    stub({})
    render()

    await screen.findByText('JOSH Person')
    await user.click(screen.getByRole('button', { name: /reset the password for josh/i }))

    expect(await screen.findByText(/Every session this account has open will be signed out/i))
      .toBeInTheDocument()
  })

  it('reports a failure rather than showing an empty list', async () => {
    stub({
      users: () =>
        jsonResponse(
          { error: { code: 'internal_error', message: 'Something went wrong.' } },
          { status: 500 },
        ),
    })
    render()

    expect(await screen.findByText('Could not load the accounts')).toBeInTheDocument()
  })
})
