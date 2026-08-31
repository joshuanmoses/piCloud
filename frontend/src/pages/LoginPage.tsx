import { useState, type FormEvent } from 'react'
import { HardDrive, Loader2 } from 'lucide-react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { ApiError } from '@/api/client'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useAuth, useLogin } from '@/features/auth/useAuth'

interface LocationState {
  from?: string
}

export function LoginPage() {
  const { isAuthenticated, isLoading } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const loginMutation = useLogin()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [remember, setRemember] = useState(false)

  // Where the user was headed before being redirected here.
  const from = (location.state as LocationState | null)?.from ?? '/'

  if (isLoading) return null
  if (isAuthenticated) return <Navigate to={from} replace />

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    loginMutation.mutate(
      { username: username.trim(), password, remember },
      {
        onSuccess: () => {
          // Clear the password from component state immediately; there is no
          // reason for it to outlive the request.
          setPassword('')
          void navigate(from, { replace: true })
        },
      },
    )
  }

  const error = loginMutation.error
  const isPending = loginMutation.isPending

  return (
    <div className="mx-auto flex w-full max-w-sm flex-col justify-center py-12">
      <div className="mb-8 flex flex-col items-center text-center">
        <span className="flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
          <HardDrive className="size-5" aria-hidden="true" />
        </span>
        <h1 className="mt-4 text-lg font-semibold tracking-tight">Sign in to PiFiles</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Your files, on your own hardware.
        </p>
      </div>

      <Card>
        <CardContent className="pt-5">
          {error ? <LoginError error={error} /> : null}

          <form onSubmit={handleSubmit} className="space-y-4" noValidate>
            <Field label="Username" required>
              {(fieldProps) => (
                <Input
                  {...fieldProps}
                  name="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="username"
                  autoFocus
                  disabled={isPending}
                  // Usernames are ASCII-only and case-insensitive server-side.
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                />
              )}
            </Field>

            <Field label="Password" required>
              {(fieldProps) => (
                <Input
                  {...fieldProps}
                  name="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="current-password"
                  disabled={isPending}
                />
              )}
            </Field>

            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={remember}
                onChange={(e) => setRemember(e.target.checked)}
                disabled={isPending}
              />
              <span>Keep me signed in on this device</span>
            </label>

            <Button type="submit" className="w-full" disabled={isPending}>
              {isPending ? (
                <>
                  <Loader2 className="animate-spin" aria-hidden="true" />
                  Signing in…
                </>
              ) : (
                'Sign in'
              )}
            </Button>
          </form>
        </CardContent>
      </Card>

      <p className="mt-6 text-center text-xs text-muted-foreground">
        Accounts are created by an administrator.
      </p>
    </div>
  )
}

/**
 * Renders a sign-in failure.
 *
 * The server returns the same message for an unknown username as for a wrong
 * password, and this component does not embellish it — guessing at "no such
 * user" would reintroduce the account-enumeration leak the API avoids.
 */
function LoginError({ error }: { error: unknown }) {
  if (!(error instanceof ApiError)) {
    return (
      <Alert tone="danger" title="Something went wrong" className="mb-4">
        Please try again.
      </Alert>
    )
  }

  if (error.code === 'account_locked') {
    return (
      <Alert tone="warning" title="Account temporarily locked" className="mb-4">
        {error.message}
      </Alert>
    )
  }
  if (error.code === 'account_disabled') {
    return (
      <Alert tone="warning" title="Account disabled" className="mb-4">
        {error.message}
      </Alert>
    )
  }
  if (error.code === 'rate_limited') {
    return (
      <Alert tone="warning" title="Too many attempts" className="mb-4">
        {error.message}
      </Alert>
    )
  }
  if (error.status === 0) {
    return (
      <Alert tone="danger" title="Cannot reach the server" className="mb-4">
        {error.message}
      </Alert>
    )
  }

  return (
    <Alert tone="danger" title="Sign-in failed" className="mb-4">
      {error.message}
    </Alert>
  )
}
