import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@/components/ui/toast'
import { AdminAuditPage } from '@/pages/AdminAuditPage'
import { jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { AuditEvent } from '@/types/api'

function event(overrides: Partial<AuditEvent> & Pick<AuditEvent, 'id' | 'action'>): AuditEvent {
  return {
    label: 'Signed in',
    outcome: 'success',
    created_at: '2026-08-27T10:00:00Z',
    ...overrides,
  }
}

function stub(handlers: { events?: () => Response } = {}) {
  const fetchMock = vi.fn<typeof fetch>((input) => {
    const url = urlOf(input)
    if (url.includes('/admin/audit/vocabulary')) {
      return Promise.resolve(
        jsonResponse({
          actions: [
            { action: 'auth.login', label: 'Signed in' },
            { action: 'node.purge', label: 'Deleted permanently' },
          ],
          outcomes: ['success', 'failure', 'denied'],
        }),
      )
    }
    if (url.startsWith('/api/v1/admin/audit')) {
      return Promise.resolve(
        handlers.events?.() ??
          jsonResponse({
            events: [
              event({
                id: 'e1',
                action: 'auth.login',
                actor: { id: 'u1', username: 'josh', display_name: 'Josh Moses' },
                actor_label: 'josh',
                ip: '192.0.2.10',
                device: 'Firefox on Linux',
              }),
            ],
            total: 1,
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

function render(route = '/admin/audit') {
  return renderWithProviders(
    <ToastProvider>
      <AdminAuditPage />
    </ToastProvider>,
    { route },
  )
}

describe('AdminAuditPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('lists events with who, what, and from where', async () => {
    stub()
    render()

    expect(await screen.findByText('Josh Moses')).toBeInTheDocument()
    expect(screen.getByText(/192\.0\.2\.10/)).toBeInTheDocument()
    expect(screen.getByText('Firefox on Linux')).toBeInTheDocument()
  })

  // Deleting an account must not make its history anonymous. The recorded
  // label is what keeps the record readable — and the caption says "not
  // signed in" rather than "no account", because a failed sign-in looks
  // identical here and its name may belong to a real account.
  it('still names the actor after their account is deleted', async () => {
    stub({
      events: () =>
        jsonResponse({
          events: [event({ id: 'e1', action: 'auth.login', actor_label: 'departed' })],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        }),
    })
    render()

    expect(await screen.findByText('departed')).toBeInTheDocument()
    expect(screen.getByText('not signed in')).toBeInTheDocument()
  })

  it('marks a failed and a refused event distinctly from a success', async () => {
    stub({
      events: () =>
        jsonResponse({
          events: [
            event({ id: 'e1', action: 'auth.login_failed', label: 'Sign-in failed', outcome: 'failure' }),
            event({ id: 'e2', action: 'node.purge', label: 'Deleted permanently', outcome: 'denied' }),
          ],
          total: 2,
          limit: 50,
          offset: 0,
          has_more: false,
        }),
    })
    render()

    expect(await screen.findByText('Failed')).toBeInTheDocument()
    expect(screen.getByText('Refused')).toBeInTheDocument()
  })

  // The action list comes from the server so the filter cannot offer something
  // that is never recorded, or miss something that is.
  it('builds the action filter from the server vocabulary', async () => {
    stub()
    render()

    await screen.findByText('Josh Moses')

    const options = within(screen.getByLabelText('Action')).getAllByRole('option')
    const labels = options.map((option) => option.textContent)
    expect(labels).toContain('Signed in')
    expect(labels).toContain('Deleted permanently')
  })

  it('puts a chosen filter in the request', async () => {
    const user = userEvent.setup()
    const fetchMock = stub()
    render()

    await screen.findByText('Josh Moses')
    await user.selectOptions(screen.getByLabelText('Action'), 'node.purge')

    await waitFor(() => {
      const filtered = fetchMock.mock.calls.some(([input]) =>
        urlOf(input).includes('action=node.purge'),
      )
      expect(filtered).toBe(true)
    })
  })

  it('reads the filter from the URL, so a view can be shared', async () => {
    const fetchMock = stub()
    render('/admin/audit?outcome=failure')

    await waitFor(() => {
      const requested = fetchMock.mock.calls.some(([input]) =>
        urlOf(input).includes('outcome=failure'),
      )
      expect(requested).toBe(true)
    })
  })

  it('renders a detail object as readable pairs rather than [object Object]', async () => {
    stub({
      events: () =>
        jsonResponse({
          events: [
            event({
              id: 'e1',
              action: 'user.password_reset',
              label: 'Password reset by an administrator',
              detail: { must_change_password: true, sessions_revoked: 3 },
            }),
          ],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        }),
    })
    render()

    expect(await screen.findByText('Password reset by an administrator')).toBeInTheDocument()
    expect(screen.getByText('yes')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.queryByText(/\[object Object\]/)).not.toBeInTheDocument()
  })

  it('reports a failure rather than an empty trail', async () => {
    stub({
      events: () =>
        jsonResponse(
          { error: { code: 'internal_error', message: 'Something went wrong.' } },
          { status: 500 },
        ),
    })
    render()

    expect(await screen.findByText('Could not load the audit trail')).toBeInTheDocument()
    expect(screen.queryByText('Nothing matches')).not.toBeInTheDocument()
  })
})
