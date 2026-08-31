import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, LockOpen, Plus, Search, ShieldCheck, Trash2, UserX } from 'lucide-react'
import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  adminKeys,
  createUser,
  deleteUser,
  listUsers,
  resetPassword,
  unlockUser,
  updateUser,
} from '@/api/admin'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  ConfirmDialog,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { useAuth } from '@/features/auth/useAuth'
import { ErrorAlert } from '@/features/files/dialogs'
import { ApiError } from '@/api/client'
import { formatRelative } from '@/lib/format'
import { formatBytes, cn } from '@/lib/utils'
import type { AdminUser } from '@/types/api'

/**
 * Account management.
 *
 * What an administrator can do here is bounded on purpose: create, disable,
 * reset a password, change a quota, delete. There is no "sign in as" and no
 * route into anybody's files — the server does not offer one either.
 *
 * Every destructive action asks first and says what it will actually do,
 * because "disable" and "delete" are easy to confuse and only one of them is
 * recoverable.
 */
export function AdminUsersPage() {
  const [params, setParams] = useSearchParams()
  const queryClient = useQueryClient()
  const toast = useToast()
  const { user: self } = useAuth()

  const search = params.get('q') ?? ''
  const [creating, setCreating] = useState(false)
  const [resetting, setResetting] = useState<AdminUser | null>(null)
  const [deleting, setDeleting] = useState<AdminUser | null>(null)

  const users = useQuery({
    queryKey: adminKeys.users({ q: search }),
    queryFn: ({ signal }) => listUsers({ q: search || undefined }, signal),
    placeholderData: keepPreviousData,
  })

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['admin'] })

  const fail = (title: string) => (error: unknown) => {
    toast.show({
      tone: 'danger',
      title,
      description: error instanceof ApiError ? error.message : undefined,
    })
  }

  const setEnabled = useMutation({
    mutationFn: ({ user, enabled }: { user: AdminUser; enabled: boolean }) =>
      updateUser(user.id, { is_enabled: enabled }),
    onSuccess: async (_r, { user, enabled }) => {
      await refresh()
      toast.show({
        tone: 'success',
        title: enabled ? `${user.username} can sign in again` : `${user.username} is disabled`,
        description: enabled ? undefined : 'Their sessions have been ended.',
      })
    },
    onError: fail('Could not change the account'),
  })

  const setAdmin = useMutation({
    mutationFn: ({ user, admin }: { user: AdminUser; admin: boolean }) =>
      updateUser(user.id, { is_admin: admin }),
    onSuccess: refresh,
    onError: fail('Could not change administrator access'),
  })

  const unlock = useMutation({
    mutationFn: (user: AdminUser) => unlockUser(user.id),
    onSuccess: refresh,
    onError: fail('Could not unlock the account'),
  })

  const remove = useMutation({
    mutationFn: (user: AdminUser) => deleteUser(user.id),
    onSuccess: async (_r, user) => {
      setDeleting(null)
      await refresh()
      toast.show({ tone: 'success', title: `${user.username} has been deleted` })
    },
    onError: (error) => {
      setDeleting(null)
      fail('Could not delete the account')(error)
    },
  })

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Accounts</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Administrators manage accounts. They cannot read anybody&rsquo;s files.
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus aria-hidden="true" />
          New account
        </Button>
      </div>

      <div className="relative max-w-sm">
        <Search
          className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <Input
          type="search"
          value={search}
          onChange={(event) => {
            const next = new URLSearchParams(params)
            if (event.target.value) next.set('q', event.target.value)
            else next.delete('q')
            setParams(next, { replace: true })
          }}
          placeholder="Search by name…"
          aria-label="Search accounts"
          className="pl-8"
        />
      </div>

      {users.isError ? (
        <ErrorAlert error={users.error} title="Could not load the accounts" />
      ) : users.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading accounts">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : users.data.users.length === 0 ? (
        <p className="rounded-card border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground">
          No accounts match &ldquo;{search}&rdquo;.
        </p>
      ) : (
        <div className="overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[44rem] border-collapse">
            <caption className="sr-only">Accounts on this server</caption>
            <thead>
              <tr className="border-b border-border bg-surface-muted text-left">
                <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                  Account
                </th>
                <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                  Status
                </th>
                <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground md:table-cell">
                  Storage
                </th>
                <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground lg:table-cell">
                  Last signed in
                </th>
                <th scope="col" className="w-40 px-3 py-2">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {users.data.users.map((user) => (
                <tr key={user.id} className="group border-b border-border last:border-b-0 hover:bg-muted">
                  <td className="px-3 py-2.5">
                    <p className="text-sm font-medium">{user.display_name}</p>
                    <p className="text-xs text-muted-foreground">
                      {user.username}
                      {user.email ? ` · ${user.email}` : ''}
                      {self?.id === user.id ? ' · you' : ''}
                    </p>
                  </td>

                  <td className="whitespace-nowrap px-3 py-2.5">
                    <div className="flex flex-wrap gap-1">
                      {user.is_admin ? <Badge variant="primary">Administrator</Badge> : null}
                      {!user.is_enabled ? <Badge variant="danger">Disabled</Badge> : null}
                      {user.is_locked ? <Badge variant="warning">Locked out</Badge> : null}
                      {user.must_change_password ? (
                        <Badge variant="neutral">Must change password</Badge>
                      ) : null}
                      {user.is_enabled && !user.is_admin && !user.is_locked && !user.must_change_password ? (
                        <span className="text-xs text-muted-foreground">Active</span>
                      ) : null}
                    </div>
                  </td>

                  <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground md:table-cell">
                    {formatBytes(user.used_bytes)}
                    {user.quota_bytes === null ? ' of unlimited' : ` of ${formatBytes(user.quota_bytes)}`}
                  </td>

                  <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground lg:table-cell">
                    {user.last_login_at ? formatRelative(user.last_login_at) : 'Never'}
                  </td>

                  <td className="whitespace-nowrap px-3 py-2.5 text-right">
                    <div
                      className={cn(
                        'flex items-center justify-end gap-0.5 opacity-0 transition-opacity',
                        'group-hover:opacity-100 group-focus-within:opacity-100',
                        '[@media(hover:none)]:opacity-100',
                      )}
                    >
                      {user.is_locked ? (
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => unlock.mutate(user)}
                          title="Clear the lockout"
                          aria-label={`Unlock ${user.username}`}
                        >
                          <LockOpen aria-hidden="true" />
                        </Button>
                      ) : null}

                      <Button
                        variant="ghost"
                        size="icon-sm"
                        onClick={() => setResetting(user)}
                        title="Set a new password"
                        aria-label={`Reset the password for ${user.username}`}
                      >
                        <KeyRound aria-hidden="true" />
                      </Button>

                      <Button
                        variant="ghost"
                        size="icon-sm"
                        onClick={() => setAdmin.mutate({ user, admin: !user.is_admin })}
                        disabled={self?.id === user.id}
                        title={
                          self?.id === user.id
                            ? 'You cannot change your own administrator access'
                            : user.is_admin
                              ? 'Remove administrator access'
                              : 'Make an administrator'
                        }
                        aria-label={
                          user.is_admin
                            ? `Remove administrator access from ${user.username}`
                            : `Make ${user.username} an administrator`
                        }
                      >
                        <ShieldCheck
                          className={user.is_admin ? 'text-primary' : undefined}
                          aria-hidden="true"
                        />
                      </Button>

                      <Button
                        variant="ghost"
                        size="icon-sm"
                        onClick={() => setEnabled.mutate({ user, enabled: !user.is_enabled })}
                        disabled={self?.id === user.id}
                        title={
                          self?.id === user.id
                            ? 'You cannot disable your own account'
                            : user.is_enabled
                              ? 'Disable this account'
                              : 'Let this account sign in again'
                        }
                        aria-label={
                          user.is_enabled ? `Disable ${user.username}` : `Enable ${user.username}`
                        }
                      >
                        <UserX aria-hidden="true" />
                      </Button>

                      <Button
                        variant="ghost"
                        size="icon-sm"
                        onClick={() => setDeleting(user)}
                        disabled={self?.id === user.id}
                        title={
                          self?.id === user.id
                            ? 'You cannot delete your own account'
                            : 'Delete this account and everything it owns'
                        }
                        aria-label={`Delete ${user.username}`}
                      >
                        <Trash2 aria-hidden="true" />
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <CreateAccountDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={async () => {
          setCreating(false)
          await refresh()
        }}
      />

      <ResetPasswordDialog
        user={resetting}
        onOpenChange={(open) => {
          if (!open) setResetting(null)
        }}
        onDone={async (username) => {
          setResetting(null)
          await refresh()
          toast.show({
            tone: 'success',
            title: `New password set for ${username}`,
            description: 'Every session they had has been signed out.',
          })
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={deleting ? `Delete ${deleting.username}?` : 'Delete this account?'}
        description={
          deleting ? (
            <>
              This permanently removes the account and everything it owns —{' '}
              <strong>{formatBytes(deleting.used_bytes)}</strong> of files. Nothing brings it back.
              <br />
              <br />
              To stop somebody signing in without destroying their files, disable the account
              instead.
            </>
          ) : (
            ''
          )
        }
        confirmLabel="Delete permanently"
        destructive
        pending={remove.isPending}
        onConfirm={() => {
          if (deleting) remove.mutate(deleting)
        }}
      />
    </div>
  )
}

function CreateAccountDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void | Promise<void>
}) {
  const [username, setUsername] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [isAdmin, setIsAdmin] = useState(false)
  const [mustChange, setMustChange] = useState(true)
  const [error, setError] = useState<ApiError | null>(null)

  const create = useMutation({
    mutationFn: () =>
      createUser({
        username: username.trim(),
        display_name: displayName.trim() || undefined,
        email: email.trim() || undefined,
        password,
        is_admin: isAdmin,
        must_change_password: mustChange,
      } as Parameters<typeof createUser>[0]),
    onSuccess: async () => {
      setUsername('')
      setDisplayName('')
      setEmail('')
      setPassword('')
      setIsAdmin(false)
      setMustChange(true)
      setError(null)
      await onCreated()
    },
    onError: (err: unknown) => {
      setError(err instanceof ApiError ? err : null)
    },
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New account</DialogTitle>
        </DialogHeader>

        <form
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault()
            create.mutate()
          }}
        >
          {error ? <ErrorAlert error={error} title="Could not create the account" /> : null}

          <Field label="Username" required error={error?.fields?.['username']}>
            {(props) => (
              <Input
                {...props}
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                autoComplete="off"
              />
            )}
          </Field>

          <Field label="Display name" hint="Defaults to the username.">
            {(props) => (
              <Input
                {...props}
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
              />
            )}
          </Field>

          <Field label="Email" error={error?.fields?.['email']}>
            {(props) => (
              <Input
                {...props}
                type="email"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
                autoComplete="off"
              />
            )}
          </Field>

          <Field
            label="Temporary password"
            required
            error={error?.fields?.['password']}
            hint="Tell them this once. They choose their own on first sign-in."
          >
            {(props) => (
              <Input
                {...props}
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="new-password"
              />
            )}
          </Field>

          <label className="flex items-start gap-2 text-sm">
            <Checkbox checked={mustChange} onChange={() => setMustChange((v) => !v)} />
            <span>
              Require a new password at first sign-in
              <span className="block text-xs text-muted-foreground">
                Leave this on unless you have a reason not to — otherwise you know their password.
              </span>
            </span>
          </label>

          <label className="flex items-start gap-2 text-sm">
            <Checkbox checked={isAdmin} onChange={() => setIsAdmin((v) => !v)} />
            <span>
              Make this account an administrator
              <span className="block text-xs text-muted-foreground">
                They will be able to manage accounts — but still not read other people&rsquo;s
                files.
              </span>
            </span>
          </label>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              Create account
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ResetPasswordDialog({
  user,
  onOpenChange,
  onDone,
}: {
  user: AdminUser | null
  onOpenChange: (open: boolean) => void
  onDone: (username: string) => void | Promise<void>
}) {
  const [password, setPassword] = useState('')
  const [mustChange, setMustChange] = useState(true)
  const [error, setError] = useState<ApiError | null>(null)

  const reset = useMutation({
    mutationFn: () => {
      if (!user) throw new Error('no account selected')
      return resetPassword(user.id, password, mustChange)
    },
    onSuccess: async () => {
      const name = user?.username ?? ''
      setPassword('')
      setError(null)
      await onDone(name)
    },
    onError: (err: unknown) => setError(err instanceof ApiError ? err : null),
  })

  return (
    <Dialog open={user !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Reset the password for {user?.username}</DialogTitle>
        </DialogHeader>

        <form
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault()
            reset.mutate()
          }}
        >
          {error ? <ErrorAlert error={error} title="Could not reset the password" /> : null}

          <p className="text-sm text-muted-foreground">
            Every session this account has open will be signed out.
          </p>

          <Field label="New password" required error={error?.fields?.['password']}>
            {(props) => (
              <Input
                {...props}
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="new-password"
                autoFocus
              />
            )}
          </Field>

          <label className="flex items-start gap-2 text-sm">
            <Checkbox checked={mustChange} onChange={() => setMustChange((v) => !v)} />
            <span>Require them to choose a new one at next sign-in</span>
          </label>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={reset.isPending}>
              Reset password
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
