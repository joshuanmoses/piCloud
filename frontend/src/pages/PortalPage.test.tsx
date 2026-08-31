import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PortalPage } from '@/pages/PortalPage'
import { jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import type { PortalView } from '@/types/api'

const shareView: PortalView = {
  needs_password: false,
  kind: 'download',
  item: { id: 'n1', name: 'Report.pdf', kind: 'file', size_bytes: 2_411_724, mime_type: 'application/pdf' },
  can_download: true,
  can_upload: false,
  remaining_uses: -1,
}

const uploadView: PortalView = {
  needs_password: false,
  kind: 'upload',
  title: 'Upload your documents',
  message: 'Please send this year’s receipts.',
  requested_by: 'Accounting',
  destination: '2026 Tax Documents',
  can_download: false,
  can_upload: true,
  max_upload_bytes: 20 * 1024 ** 3,
  allowed_extensions: ['.pdf', '.jpg'],
  remaining_uses: -1,
}

function stubPortal(response: Response | (() => Response)) {
  const fetchMock = vi.fn<typeof fetch>((input) => {
    const url = urlOf(input)
    if (url.includes('/portal/')) {
      return Promise.resolve(typeof response === 'function' ? response() : response.clone())
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function render(route = '/s/abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG') {
  return renderWithProviders(<PortalPage />, { route })
}

describe('PortalPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('shows a shared file with a download button', async () => {
    stubPortal(jsonResponse(shareView))
    render()

    expect(await screen.findByText('Report.pdf')).toBeInTheDocument()
    expect(screen.getByText('2.3 MB')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /download/i })).toBeInTheDocument()
  })

  it('does not offer downloading on a view-only link', async () => {
    stubPortal(jsonResponse({ ...shareView, can_download: false }))
    render()

    expect(await screen.findByText('Report.pdf')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /download/i })).not.toBeInTheDocument()
    expect(screen.getByText(/does not allow downloading/i)).toBeInTheDocument()
  })

  // Every reason a link does not work is reported identically by the server,
  // so the page must not speculate about which one applies.
  it('reports an unavailable link without guessing why', async () => {
    stubPortal(
      jsonResponse(
        { error: { code: 'not_found', message: 'The requested resource was not found.' } },
        { status: 404 },
      ),
    )
    render()

    expect(await screen.findByText(/this link is not available/i)).toBeInTheDocument()
    // It must not claim to know the reason.
    const body = document.body.textContent ?? ''
    expect(body).not.toMatch(/revoked by/i)
    expect(body).not.toMatch(/does not exist/i)
  })

  it('distinguishes an unreachable server from a dead link', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
    render()

    expect(await screen.findByText(/cannot reach the server/i)).toBeInTheDocument()
  })

  // --- Password ------------------------------------------------------------

  it('prompts for a password and reveals nothing before it', async () => {
    stubPortal(jsonResponse({ needs_password: true, can_download: false, can_upload: false, remaining_uses: -1 }))
    render()

    expect(await screen.findByText(/password protected/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/password/i)).toHaveAttribute('type', 'password')

    // Nothing about the content.
    const body = document.body.textContent ?? ''
    expect(body).not.toContain('Report.pdf')
    expect(screen.queryByRole('link', { name: /download/i })).not.toBeInTheDocument()
  })

  it('unlocks after the correct password', async () => {
    const user = userEvent.setup()
    let unlocked = false

    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input, init) => {
        const url = urlOf(input)
        if (url.includes('/password') && init?.method === 'POST') {
          unlocked = true
          return Promise.resolve(new Response(null, { status: 204 }))
        }
        if (url.includes('/portal/')) {
          return Promise.resolve(
            jsonResponse(
              unlocked
                ? shareView
                : { needs_password: true, can_download: false, can_upload: false, remaining_uses: -1 },
            ),
          )
        }
        return Promise.resolve(new Response('', { status: 404 }))
      }),
    )
    render()

    await user.type(await screen.findByLabelText(/password/i), 'the link password')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    expect(await screen.findByText('Report.pdf')).toBeInTheDocument()
  })

  it('reports a wrong password without locking the visitor out', async () => {
    const user = userEvent.setup()
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input, init) => {
        const url = urlOf(input)
        if (url.includes('/password') && init?.method === 'POST') {
          return Promise.resolve(
            jsonResponse(
              { error: { code: 'unauthorized', message: 'Incorrect password.' } },
              { status: 401 },
            ),
          )
        }
        return Promise.resolve(
          jsonResponse({ needs_password: true, can_download: false, can_upload: false, remaining_uses: -1 }),
        )
      }),
    )
    render()

    await user.type(await screen.findByLabelText(/password/i), 'wrong')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    expect(await screen.findByText(/incorrect password/i)).toBeInTheDocument()
    // The form is still usable for another attempt.
    expect(screen.getByLabelText(/password/i)).toBeEnabled()
  })

  it('explains rate limiting differently from a wrong password', async () => {
    const user = userEvent.setup()
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input, init) => {
        const url = urlOf(input)
        if (url.includes('/password') && init?.method === 'POST') {
          return Promise.resolve(
            jsonResponse(
              { error: { code: 'rate_limited', message: 'Too many attempts. Please wait a few minutes.' } },
              { status: 429 },
            ),
          )
        }
        return Promise.resolve(
          jsonResponse({ needs_password: true, can_download: false, can_upload: false, remaining_uses: -1 }),
        )
      }),
    )
    render()

    await user.type(await screen.findByLabelText(/password/i), 'guess')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    expect(await screen.findByText(/too many attempts/i)).toBeInTheDocument()
  })

  // --- File request --------------------------------------------------------

  it('shows who is asking and where files are going', async () => {
    stubPortal(jsonResponse(uploadView))
    render('/u/abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG')

    expect(await screen.findByText('Upload your documents')).toBeInTheDocument()
    expect(screen.getByText(/please send this year/i)).toBeInTheDocument()
    expect(screen.getByText('Accounting')).toBeInTheDocument()
    expect(screen.getByText('2026 Tax Documents')).toBeInTheDocument()
    expect(screen.getByText('20 GB')).toBeInTheDocument()
    expect(screen.getByText('.pdf, .jpg')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /select files/i })).toBeInTheDocument()
  })

  // The server does not disclose the folder's contents to an upload portal, so
  // the page has nothing to show — and must not imply otherwise.
  it('shows no file listing on an upload portal', async () => {
    stubPortal(jsonResponse(uploadView))
    render('/u/abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG')

    await screen.findByText('Upload your documents')
    expect(screen.queryByRole('list')).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /download/i })).not.toBeInTheDocument()
  })

  it('reports a usage limit', async () => {
    stubPortal(jsonResponse({ ...shareView, remaining_uses: 3 }))
    render()

    expect(await screen.findByText(/3 uses remaining/i)).toBeInTheDocument()
  })

  // The portal is for people with no account: it must not show anything
  // implying they have one.
  it('shows no account interface', async () => {
    stubPortal(jsonResponse(shareView))
    render()

    await screen.findByText('Report.pdf')
    expect(screen.queryByRole('navigation', { name: /main/i })).not.toBeInTheDocument()
    expect(screen.queryByText(/my files/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /sign out/i })).not.toBeInTheDocument()
  })

  it('lists a shared folder with per-file download links', async () => {
    stubPortal(
      jsonResponse({
        ...shareView,
        item: { id: 'f1', name: 'Photos', kind: 'folder', size_bytes: 0 },
        items: [
          { id: 'a', name: 'one.jpg', kind: 'file', size_bytes: 1000 },
          { id: 'b', name: 'two.jpg', kind: 'file', size_bytes: 2000 },
        ],
      } satisfies PortalView),
    )
    render()

    expect(await screen.findByText('one.jpg')).toBeInTheDocument()
    expect(screen.getByText('two.jpg')).toBeInTheDocument()

    const list = screen.getByRole('list')
    expect(within(list).getAllByRole('link', { name: /download/i })).toHaveLength(2)
  })

  it('shows an empty shared folder honestly', async () => {
    stubPortal(
      jsonResponse({
        ...shareView,
        item: { id: 'f1', name: 'Photos', kind: 'folder', size_bytes: 0 },
        items: [],
      } satisfies PortalView),
    )
    render()

    expect(await screen.findByText(/this folder is empty/i)).toBeInTheDocument()
  })

  it('does not retry a dead link repeatedly', async () => {
    const fetchMock = stubPortal(
      jsonResponse({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 }),
    )
    render()

    await screen.findByText(/this link is not available/i)
    await waitFor(() => {
      const portalCalls = fetchMock.mock.calls.filter((c) => urlOf(c[0]).includes('/portal/'))
      expect(portalCalls.length).toBe(1)
    })
  })
})
