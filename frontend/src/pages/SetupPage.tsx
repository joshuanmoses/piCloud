import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { HardDrive, Loader2, ShieldCheck } from 'lucide-react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useMutation } from '@tanstack/react-query'
import { authKeys, completeSetup, fetchSetupStatus } from '@/api/auth'
import { ApiError } from '@/api/client'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import type { SetupRequest } from '@/types/api'

/**
 * First-run setup.
 *
 * PiFiles ships with no accounts and no default password, so the very first
 * administrator is created here. The endpoint behind this page is available
 * only while the instance has zero users; afterwards it returns 404 and this
 * page redirects to sign-in.
 */
export function SetupPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const status = useQuery({
    queryKey: authKeys.setup(),
    queryFn: ({ signal }) => fetchSetupStatus(signal),
    staleTime: 0,
  })

  const [username, setUsername] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [confirmError, setConfirmError] = useState<string>()

  const setup = useMutation({
    mutationFn: (body: SetupRequest) => completeSetup(body),
    onSuccess: (data) => {
      queryClient.setQueryData(authKeys.session(), data)
      setPassword('')
      setConfirm('')
      void navigate('/', { replace: true })
    },
  })

  if (status.isPending) {
    return (
      <div className="mx-auto w-full max-w-md space-y-4 py-12" role="status" aria-label="Loading">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    )
  }

  // Setup already done, or unreachable: sign-in is the right destination.
  if (status.isError || !status.data?.setup_required) {
    return <Navigate to="/login" replace />
  }

  const minLength = status.data.password_min_length

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()

    // A mistyped password here would lock the administrator out of their own
    // brand-new instance, so it is confirmed before submitting.
    if (password !== confirm) {
      setConfirmError('The passwords do not match.')
      return
    }
    setConfirmError(undefined)

    setup.mutate({
      username: username.trim(),
      display_name: displayName.trim() || undefined,
      email: email.trim() || undefined,
      password,
    } as SetupRequest)
  }

  const fieldErrors = setup.error instanceof ApiError ? setup.error.fields : undefined

  return (
    <div className="mx-auto w-full max-w-md py-12">
      <div className="mb-8 flex flex-col items-center text-center">
        <span className="flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
          <HardDrive className="size-5" aria-hidden="true" />
        </span>
        <h1 className="mt-4 text-lg font-semibold tracking-tight">Welcome to PiFiles</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Create the administrator account for this server.
        </p>
      </div>

      <Alert tone="warning" title="Finish this now" className="mb-4">
        Until an administrator exists, anyone who can reach this server could claim it. Complete
        setup before connecting the machine to an untrusted network.
      </Alert>

      <Card>
        <CardContent className="pt-5">
          {setup.error && !fieldErrors ? (
            <Alert tone="danger" title="Could not create the account" className="mb-4">
              {setup.error instanceof ApiError ? setup.error.message : 'Please try again.'}
            </Alert>
          ) : null}

          <form onSubmit={handleSubmit} className="space-y-4" noValidate>
            <Field
              label="Username"
              required
              error={fieldErrors?.username}
              hint="Letters, numbers, dots, hyphens, and underscores."
            >
              {(props) => (
                <Input
                  {...props}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="username"
                  autoFocus
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                  disabled={setup.isPending}
                />
              )}
            </Field>

            <Field label="Display name" error={fieldErrors?.display_name} hint="Optional. Defaults to your username.">
              {(props) => (
                <Input
                  {...props}
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  autoComplete="name"
                  disabled={setup.isPending}
                />
              )}
            </Field>

            <Field label="Email" error={fieldErrors?.email} hint="Optional.">
              {(props) => (
                <Input
                  {...props}
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  autoComplete="email"
                  disabled={setup.isPending}
                />
              )}
            </Field>

            <Field
              label="Password"
              required
              error={fieldErrors?.password}
              hint={`At least ${minLength} characters. A memorable phrase of several words is stronger and easier to type than a short complex password.`}
            >
              {(props) => (
                <Input
                  {...props}
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="new-password"
                  disabled={setup.isPending}
                />
              )}
            </Field>

            <Field label="Confirm password" required error={confirmError}>
              {(props) => (
                <Input
                  {...props}
                  type="password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  autoComplete="new-password"
                  disabled={setup.isPending}
                />
              )}
            </Field>

            <Button type="submit" className="w-full" disabled={setup.isPending}>
              {setup.isPending ? (
                <>
                  <Loader2 className="animate-spin" aria-hidden="true" />
                  Creating…
                </>
              ) : (
                <>
                  <ShieldCheck aria-hidden="true" />
                  Create administrator
                </>
              )}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
