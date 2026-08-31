import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { jsonBodyOf, jsonResponse, renderWithProviders, urlOf } from '@/test/utils'
import { LoginPage } from './LoginPage'

const sessionBody = {
  user: {
    id: '11111111-1111-1111-1111-111111111111',
    username: 'josh',
    display_name: 'Josh Moses',
    is_enabled: true,
    is_admin: false,
    quota_bytes: null,
    used_bytes: 0,
    created_at: '2026-08-26T12:00:00Z',
    updated_at: '2026-08-26T12:00:00Z',
    must_change_password: false,
  },
  csrf_token: 'csrf-token-value',
  expires_at: '2026-08-27T12:00:00Z',
  must_change_password: false,
}

function errorResponse(status: number, code: string, message: string) {
  return jsonResponse({ error: { code, message } }, { status })
}

/** Routes fetch by URL: the session probe fails until login succeeds. */
function stubAuth(loginResponse: Response) {
  let signedIn = false
  // Typed as `typeof fetch` so mock.calls carries both the URL and the init,
  // and assertions about the request body are checked at compile time.
  const fetchMock = vi.fn<typeof fetch>((input) => {
    const url = urlOf(input)
    if (url.includes('/auth/login')) {
      if (loginResponse.status === 200) signedIn = true
      return Promise.resolve(loginResponse.clone())
    }
    if (url.includes('/auth/session')) {
      return Promise.resolve(
        signedIn
          ? jsonResponse(sessionBody)
          : errorResponse(401, 'unauthorized', 'Not signed in.'),
      )
    }
    return Promise.resolve(new Response('', { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('LoginPage', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.unstubAllGlobals())

  it('renders an accessible sign-in form', async () => {
    stubAuth(jsonResponse(sessionBody))
    renderWithProviders(<LoginPage />)

    // Queried by accessible name: a field that cannot be found this way is
    // one a screen-reader user cannot identify either.
    expect(await screen.findByLabelText(/username/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /sign in/i })).toBeInTheDocument()
    expect(screen.getByLabelText(/keep me signed in/i)).toBeInTheDocument()
  })

  it('uses the correct autocomplete and input types', async () => {
    stubAuth(jsonResponse(sessionBody))
    renderWithProviders(<LoginPage />)

    const username = await screen.findByLabelText(/username/i)
    const password = screen.getByLabelText(/password/i)

    expect(username).toHaveAttribute('autocomplete', 'username')
    // A password field must mask input and tell the password manager what it is.
    expect(password).toHaveAttribute('type', 'password')
    expect(password).toHaveAttribute('autocomplete', 'current-password')
  })

  it('submits the credentials and signs in', async () => {
    const user = userEvent.setup()
    const fetchMock = stubAuth(jsonResponse(sessionBody))
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'josh')
    await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => {
      const loginCall = fetchMock.mock.calls.find((c) => urlOf(c[0]).includes('/auth/login'))
      expect(loginCall).toBeDefined()
      expect(jsonBodyOf(loginCall?.[1])).toEqual({
        username: 'josh',
        password: 'correct horse battery staple',
        remember: false,
      })
    })
  })

  it('trims whitespace from the username', async () => {
    const user = userEvent.setup()
    const fetchMock = stubAuth(jsonResponse(sessionBody))
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), '  josh  ')
    await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) => urlOf(c[0]).includes('/auth/login'))
      expect((jsonBodyOf(call?.[1]) as { username: string }).username).toBe('josh')
    })
  })

  it('sends remember when the box is ticked', async () => {
    const user = userEvent.setup()
    const fetchMock = stubAuth(jsonResponse(sessionBody))
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'josh')
    await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
    await user.click(screen.getByLabelText(/keep me signed in/i))
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) => urlOf(c[0]).includes('/auth/login'))
      expect((jsonBodyOf(call?.[1]) as { remember: boolean }).remember).toBe(true)
    })
  })

  it('shows the server message for bad credentials', async () => {
    const user = userEvent.setup()
    stubAuth(errorResponse(401, 'unauthorized', 'Incorrect username or password.'))
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'josh')
    await user.type(screen.getByLabelText(/password/i), 'wrong')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/incorrect username or password/i)
  })

  // The interface must not add detail the API deliberately withholds: saying
  // "no such user" would reintroduce the enumeration leak.
  it('does not distinguish an unknown user from a wrong password', async () => {
    const user = userEvent.setup()
    stubAuth(errorResponse(401, 'unauthorized', 'Incorrect username or password.'))
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'nosuchperson')
    await user.type(screen.getByLabelText(/password/i), 'anything')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    const alert = await screen.findByRole('alert')
    for (const phrase of [/no such user/i, /user not found/i, /does not exist/i, /unknown account/i]) {
      expect(alert.textContent ?? '').not.toMatch(phrase)
    }
  })

  it('explains a locked account distinctly', async () => {
    const user = userEvent.setup()
    stubAuth(
      errorResponse(429, 'account_locked', 'This account is temporarily locked after too many failed sign-in attempts.'),
    )
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'josh')
    await user.type(screen.getByLabelText(/password/i), 'wrong')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByText(/account temporarily locked/i)).toBeInTheDocument()
  })

  it('explains a disabled account distinctly', async () => {
    const user = userEvent.setup()
    stubAuth(errorResponse(403, 'account_disabled', 'This account has been disabled. Contact an administrator.'))
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'josh')
    await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByText(/account disabled/i)).toBeInTheDocument()
  })

  it('distinguishes an unreachable server from a rejected sign-in', async () => {
    const user = userEvent.setup()
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input) =>
        urlOf(input).includes('/auth/session')
          ? Promise.resolve(errorResponse(401, 'unauthorized', 'Not signed in.'))
          : Promise.reject(new TypeError('Failed to fetch')),
      ),
    )
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'josh')
    await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    // The alert title and its body both mention reachability, so assert on the
    // alert as a whole rather than on a single matching node.
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/cannot reach the server/i)
  })

  it('disables the form while the request is in flight', async () => {
    const user = userEvent.setup()
    let release: (r: Response) => void = () => {}
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input) => {
        if (urlOf(input).includes('/auth/session')) {
          return Promise.resolve(errorResponse(401, 'unauthorized', 'Not signed in.'))
        }
        return new Promise<Response>((resolve) => {
          release = resolve
        })
      }),
    )
    renderWithProviders(<LoginPage />)

    await user.type(await screen.findByLabelText(/username/i), 'josh')
    await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    // Prevents a double submit, which would consume two rate-limit slots.
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /signing in/i })).toBeDisabled()
    })
    expect(screen.getByLabelText(/username/i)).toBeDisabled()

    release(jsonResponse(sessionBody))
  })
})
