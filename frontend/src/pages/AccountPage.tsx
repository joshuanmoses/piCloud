import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { Loader2, LogOut, Monitor } from 'lucide-react'
import { revokeMyOtherSessions } from '@/api/admin'
import { authKeys, changePassword, fetchMySessions, revokeSession } from '@/api/auth'
import { ApiError } from '@/api/client'
import { Alert } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/features/auth/useAuth'
import { describeUserAgent, formatRelative } from '@/lib/format'
import { formatBytes } from '@/lib/utils'

/** Personal account settings: profile, password, and active devices. */
export function AccountPage() {
  const { user } = useAuth()

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Your account</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Manage your password and see where you are signed in.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Profile</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3">
            <Detail label="Username" value={user?.username ?? '—'} />
            <Detail label="Display name" value={user?.display_name ?? '—'} />
            <Detail label="Email" value={user?.email || 'Not set'} />
            <Detail
              label="Role"
              value={user?.is_admin ? 'Administrator' : 'Standard user'}
            />
            <Detail
              label="Storage used"
              value={
                user
                  ? user.quota_bytes === null
                    ? `${formatBytes(user.used_bytes)} of unlimited`
                    : `${formatBytes(user.used_bytes)} of ${formatBytes(user.quota_bytes)}`
                  : '—'
              }
            />
          </dl>
        </CardContent>
      </Card>

      <ChangePasswordCard />
      <SessionsCard />
    </div>
  )
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate text-sm font-medium">{value}</dd>
    </div>
  )
}

function ChangePasswordCard() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [confirmError, setConfirmError] = useState<string>()
  const [succeeded, setSucceeded] = useState(false)

  const mutation = useMutation({
    mutationFn: (body: { current_password: string; new_password: string }) =>
      changePassword(body),
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setConfirm('')
      setSucceeded(true)
    },
  })

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSucceeded(false)

    if (next !== confirm) {
      setConfirmError('The passwords do not match.')
      return
    }
    setConfirmError(undefined)
    mutation.mutate({ current_password: current, new_password: next })
  }

  const fieldErrors = mutation.error instanceof ApiError ? mutation.error.fields : undefined

  return (
    <Card>
      <CardHeader>
        <CardTitle>Change password</CardTitle>
        <CardDescription>
          Changing your password signs you out everywhere else.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {succeeded ? (
          <Alert tone="success" title="Password changed" className="mb-4">
            Your other devices have been signed out.
          </Alert>
        ) : null}

        {mutation.error && !fieldErrors ? (
          <Alert tone="danger" title="Could not change your password" className="mb-4">
            {mutation.error instanceof ApiError ? mutation.error.message : 'Please try again.'}
          </Alert>
        ) : null}

        <form onSubmit={handleSubmit} className="max-w-sm space-y-4" noValidate>
          <Field label="Current password" required error={fieldErrors?.current_password}>
            {(props) => (
              <Input
                {...props}
                type="password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                autoComplete="current-password"
                disabled={mutation.isPending}
              />
            )}
          </Field>

          <Field label="New password" required error={fieldErrors?.new_password}>
            {(props) => (
              <Input
                {...props}
                type="password"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                autoComplete="new-password"
                disabled={mutation.isPending}
              />
            )}
          </Field>

          <Field label="Confirm new password" required error={confirmError}>
            {(props) => (
              <Input
                {...props}
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                autoComplete="new-password"
                disabled={mutation.isPending}
              />
            )}
          </Field>

          <Button type="submit" disabled={mutation.isPending}>
            {mutation.isPending ? (
              <>
                <Loader2 className="animate-spin" aria-hidden="true" />
                Saving…
              </>
            ) : (
              'Change password'
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

function SessionsCard() {
  const queryClient = useQueryClient()

  const query = useQuery({
    queryKey: authKeys.sessions(),
    queryFn: ({ signal }) => fetchMySessions(signal),
  })

  const revoke = useMutation({
    mutationFn: (id: string) => revokeSession(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: authKeys.sessions() })
    },
  })

  const revokeOthers = useMutation({
    mutationFn: () => revokeMyOtherSessions(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: authKeys.sessions() })
    },
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>Active sessions</CardTitle>
        <CardDescription>
          Devices currently signed in to your account. Sign out anything you do not recognise.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {query.isPending ? (
          <div className="space-y-2" role="status" aria-label="Loading sessions">
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
          </div>
        ) : query.isError ? (
          <Alert tone="warning" title="Could not load your sessions">
            Reload the page to try again.
          </Alert>
        ) : query.data.sessions.length === 0 ? (
          <p className="py-4 text-sm text-muted-foreground">No active sessions.</p>
        ) : (
          <ul className="divide-y divide-border">
            {query.data.sessions.map((session) => (
              <li key={session.id} className="flex items-center justify-between gap-4 py-3">
                <div className="flex min-w-0 items-start gap-3">
                  <Monitor className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <div className="min-w-0">
                    <p className="flex items-center gap-2 text-sm font-medium">
                      {/* The label comes from the server, so this page and the
                          administrator's cannot describe the same session
                          differently. */}
                      <span className="truncate">
                        {session.device || describeUserAgent(session.user_agent)}
                      </span>
                      {session.current ? <Badge variant="primary">This device</Badge> : null}
                    </p>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {session.ip_address ? `${session.ip_address} · ` : ''}
                      Last active {formatRelative(session.last_seen_at)}
                    </p>
                  </div>
                </div>

                {session.current ? null : (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => revoke.mutate(session.id)}
                    disabled={revoke.isPending}
                  >
                    <LogOut aria-hidden="true" />
                    Sign out
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}

        {/* One click, because this is what somebody reaches for when they think
            their password has been taken — and it deliberately keeps the
            current session, or they could not then change it. */}
        {query.data && query.data.sessions.length > 1 ? (
          <div className="mt-4 border-t border-border pt-4">
            <Button
              variant="outline"
              size="sm"
              onClick={() => revokeOthers.mutate()}
              disabled={revokeOthers.isPending}
            >
              <LogOut aria-hidden="true" />
              Sign out everywhere else
            </Button>
            <p className="mt-1.5 text-xs text-muted-foreground">
              Ends every other session and keeps this one.
            </p>
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}
