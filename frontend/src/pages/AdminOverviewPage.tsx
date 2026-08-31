import { useQuery } from '@tanstack/react-query'
import { Activity, Database, HardDrive, Users } from 'lucide-react'
import { Link } from 'react-router-dom'
import { adminKeys, getOverview } from '@/api/admin'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorAlert } from '@/features/files/dialogs'
import { formatBytes, cn } from '@/lib/utils'
import type { HealthStatus } from '@/types/api'

/**
 * The administration landing page.
 *
 * Everything here describes the *instance*: how many accounts, how much disk,
 * how the queue is doing. There is deliberately nothing about what anybody is
 * storing — no recent uploads, no largest files, no names. An administration
 * page that listed other people's work would be a window into it, and running
 * a server is not the same as being entitled to look.
 */
/**
 * Renders a round-trip time.
 *
 * The server reports whole milliseconds, so a healthy local database rounds to
 * zero — and "0 ms" next to a health indicator reads as "not measured" or
 * "broken" rather than "faster than the unit can express".
 */
function formatLatency(ms: number | undefined): string {
  if (ms === undefined) return '—'
  return ms < 1 ? '<1 ms' : `${ms} ms`
}

export function AdminOverviewPage() {
  const overview = useQuery({
    queryKey: adminKeys.overview(),
    queryFn: ({ signal }) => getOverview(signal),
    // The numbers move. Refreshing on an interval means the page is worth
    // leaving open on a second screen, which is how it will be used.
    refetchInterval: 30_000,
  })

  if (overview.isError) {
    return <ErrorAlert error={overview.error} title="Could not load the administration overview" />
  }

  if (overview.isPending) {
    return (
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4" role="status" aria-label="Loading">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-28 w-full" />
        ))}
      </div>
    )
  }

  const data = overview.data

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Administration</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The state of this server. It does not show what anybody has stored.
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          icon={Users}
          label="Accounts"
          value={String(data.users.total)}
          detail={`${data.users.administrators} ${data.users.administrators === 1 ? 'administrator' : 'administrators'}`}
          to="/admin/users"
        />
        <StatCard
          icon={Activity}
          label="Signed in now"
          value={String(data.sessions.live)}
          detail="Live sessions"
          to="/admin/sessions"
        />
        <StatCard
          icon={HardDrive}
          label="Storage"
          value={data.storage.free_bytes !== undefined ? formatBytes(data.storage.free_bytes) : '—'}
          detail={data.storage.detail ?? 'free'}
          status={data.storage.status}
        />
        <StatCard
          icon={Database}
          label="Database"
          value={formatLatency(data.database.latency_ms)}
          detail={data.database.detail ?? 'responding'}
          status={data.database.status}
        />
      </div>

      {data.jobs ? (
        <section className="space-y-2">
          <h2 className="text-sm font-medium">Background work</h2>
          <Card className="p-4">
            <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Figure label="Pending" value={data.jobs.pending} />
              <Figure label="Running" value={data.jobs.running} />
              <Figure label="Succeeded" value={data.jobs.succeeded} />
              <Figure
                label="Failed"
                value={data.jobs.failed}
                tone={data.jobs.failed > 0 ? 'danger' : undefined}
              />
            </dl>

            {/* A number that keeps climbing means the workers are not keeping
                up, which no other figure here would reveal. */}
            {data.jobs.oldest_pending_seconds > 300 ? (
              <p className="mt-3 text-xs text-warning-foreground">
                The oldest queued job has been waiting{' '}
                {Math.round(data.jobs.oldest_pending_seconds / 60)} minutes. The workers may not be
                keeping up.
              </p>
            ) : null}

            {data.jobs.failed > 0 ? (
              <p className="mt-3 text-xs">
                <Link to="/admin/jobs" className="text-primary hover:underline">
                  Review the {data.jobs.failed} failed {data.jobs.failed === 1 ? 'job' : 'jobs'}
                </Link>
              </p>
            ) : null}
          </Card>
        </section>
      ) : null}

      {data.audit_24h && data.audit_24h.length > 0 ? (
        <section className="space-y-2">
          <div className="flex items-baseline justify-between">
            <h2 className="text-sm font-medium">Activity in the last 24 hours</h2>
            <Link to="/admin/audit" className="text-xs text-primary hover:underline">
              Open the audit trail
            </Link>
          </div>
          <div className="overflow-x-auto rounded-card border border-border">
            <table className="w-full min-w-[28rem] border-collapse">
              <caption className="sr-only">Recorded actions in the last 24 hours</caption>
              <thead>
                <tr className="border-b border-border bg-surface-muted text-left">
                  <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                    Action
                  </th>
                  <th scope="col" className="px-3 py-2 text-right text-xs font-medium text-muted-foreground">
                    Total
                  </th>
                  <th scope="col" className="px-3 py-2 text-right text-xs font-medium text-muted-foreground">
                    Failed
                  </th>
                  <th scope="col" className="px-3 py-2 text-right text-xs font-medium text-muted-foreground">
                    Refused
                  </th>
                </tr>
              </thead>
              <tbody>
                {data.audit_24h.map((row) => (
                  <tr key={row.action} className="border-b border-border last:border-b-0">
                    <td className="px-3 py-2 text-sm">{row.label}</td>
                    <td className="px-3 py-2 text-right text-sm tabular-nums">{row.count}</td>
                    <td
                      className={cn(
                        'px-3 py-2 text-right text-sm tabular-nums',
                        row.failed > 0 && 'font-medium text-danger',
                      )}
                    >
                      {row.failed || '—'}
                    </td>
                    <td
                      className={cn(
                        'px-3 py-2 text-right text-sm tabular-nums',
                        row.denied > 0 && 'font-medium text-warning-foreground',
                      )}
                    >
                      {row.denied || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      ) : null}
    </div>
  )
}

function StatCard({
  icon: Icon,
  label,
  value,
  detail,
  status,
  to,
}: {
  icon: typeof Users
  label: string
  value: string
  detail?: string | undefined
  status?: HealthStatus | undefined
  to?: string | undefined
}) {
  const body = (
    <Card className="h-full p-4">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-xs text-muted-foreground">{label}</p>
          <p className="mt-1 truncate text-xl font-semibold tabular-nums">{value}</p>
          {detail ? <p className="mt-0.5 truncate text-xs text-muted-foreground">{detail}</p> : null}
        </div>
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      </div>
      {status && status !== 'healthy' ? (
        <Badge variant={status === 'unhealthy' ? 'danger' : 'warning'} className="mt-2">
          {status}
        </Badge>
      ) : null}
    </Card>
  )

  if (!to) return body
  return (
    <Link to={to} className="rounded-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
      {body}
    </Link>
  )
}

function Figure({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone?: 'danger' | undefined
}) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          'mt-0.5 text-lg font-semibold tabular-nums',
          tone === 'danger' && 'text-danger',
        )}
      >
        {value}
      </dd>
    </div>
  )
}
