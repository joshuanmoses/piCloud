import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Activity, LogOut } from 'lucide-react'
import { adminKeys, listAllSessions, revokeUserSessions } from '@/api/admin'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { ErrorAlert } from '@/features/files/dialogs'
import { formatRelative } from '@/lib/format'
import type { AdminSession } from '@/types/api'

/**
 * Who is signed in right now.
 *
 * Live sessions only. History belongs in the audit trail, which records every
 * sign-in and sign-out and keeps them long after the session is gone — this
 * page answers "who is here", not "who has been".
 */
export function AdminSessionsPage() {
  const queryClient = useQueryClient()
  const toast = useToast()

  const sessions = useQuery({
    queryKey: adminKeys.sessions(),
    queryFn: ({ signal }) => listAllSessions(signal),
    refetchInterval: 30_000,
  })

  const signOut = useMutation({
    mutationFn: (session: AdminSession) => {
      if (!session.user) throw new Error('this session has no account')
      return revokeUserSessions(session.user.id)
    },
    onSuccess: async (result, session) => {
      await queryClient.invalidateQueries({ queryKey: ['admin'] })
      toast.show({
        tone: 'success',
        title: `${session.user?.display_name ?? 'That account'} has been signed out`,
        description: `${result.revoked} ${result.revoked === 1 ? 'session' : 'sessions'} ended.`,
      })
    },
    onError: (error: unknown) => {
      toast.show({
        tone: 'danger',
        title: 'Could not end those sessions',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Signed in now</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Live sessions across every account. Past sign-ins are in the audit trail.
        </p>
      </div>

      {sessions.isError ? (
        <ErrorAlert error={sessions.error} title="Could not load the sessions" />
      ) : sessions.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading sessions">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : sessions.data.sessions.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <Activity className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">Nobody is signed in</h2>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[40rem] border-collapse">
            <caption className="sr-only">Live sessions</caption>
            <thead>
              <tr className="border-b border-border bg-surface-muted text-left">
                <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                  Account
                </th>
                <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                  Device
                </th>
                <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground md:table-cell">
                  From
                </th>
                <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                  Last seen
                </th>
                <th scope="col" className="w-32 px-3 py-2">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {sessions.data.sessions.map((session) => (
                <tr key={session.id} className="group border-b border-border last:border-b-0 hover:bg-muted">
                  <td className="px-3 py-2.5">
                    <p className="text-sm">{session.user?.display_name ?? 'Unknown'}</p>
                    <p className="text-xs text-muted-foreground">{session.user?.username}</p>
                  </td>

                  <td className="px-3 py-2.5">
                    <span className="text-sm">{session.device || 'Unknown'}</span>
                    {session.current ? (
                      <Badge variant="primary" className="ml-1.5">
                        This session
                      </Badge>
                    ) : null}
                    {session.remember ? (
                      <span className="block text-[11px] text-muted-foreground">Stays signed in</span>
                    ) : null}
                  </td>

                  <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground md:table-cell">
                    {session.ip_address || '—'}
                  </td>

                  <td className="whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground">
                    {formatRelative(session.last_seen_at)}
                  </td>

                  <td className="whitespace-nowrap px-3 py-2.5 text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => signOut.mutate(session)}
                      disabled={session.current || signOut.isPending}
                      title={
                        session.current
                          ? 'This is your own session — sign out from the account menu'
                          : 'Sign this account out everywhere'
                      }
                      className="opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 [@media(hover:none)]:opacity-100"
                    >
                      <LogOut aria-hidden="true" />
                      Sign out
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
