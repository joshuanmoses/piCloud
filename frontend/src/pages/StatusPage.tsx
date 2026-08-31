import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { ApiError } from '@/api/client'
import { fetchHealth, fetchPublicConfig, systemKeys } from '@/api/system'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusBadge, SubsystemRow } from '@/features/system/StatusCard'
import { formatBytes, formatDuration } from '@/lib/utils'

/**
 * System status.
 *
 * This is the Phase 1 landing page: it proves the browser, the API, the
 * database, and the storage volume are all working together, and it is the
 * page an administrator will return to when something looks wrong. The file
 * browser replaces it as the default route in Phase 4.
 */
export function StatusPage() {
  const health = useQuery({
    queryKey: systemKeys.health(),
    queryFn: ({ signal }) => fetchHealth(signal),
    // A modest interval keeps the page live without hammering a Raspberry Pi.
    // The backend caches health probes for five seconds regardless.
    // Retry policy is deliberately left to the query client, which already
    // knows which ApiError codes are worth retrying.
    refetchInterval: 15_000,
  })

  const config = useQuery({
    queryKey: systemKeys.config(),
    queryFn: ({ signal }) => fetchPublicConfig(signal),
    // Instance settings change rarely; there is no reason to refetch them.
    staleTime: 5 * 60_000,
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">System status</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Live health of this PiFiles instance and the services it depends on.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => void health.refetch()}
          disabled={health.isFetching}
        >
          <RefreshCw className={health.isFetching ? 'animate-spin' : undefined} aria-hidden="true" />
          {health.isFetching ? 'Checking…' : 'Refresh'}
        </Button>
      </div>

      {health.isError ? <HealthError error={health.error} onRetry={() => void health.refetch()} /> : null}

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <div>
              <CardTitle>Service health</CardTitle>
              <CardDescription>Reported by the server at /health.</CardDescription>
            </div>
            {health.data ? <StatusBadge status={health.data.status} /> : null}
          </div>
        </CardHeader>
        <CardContent>
          {health.isPending ? (
            <div className="space-y-3" role="status" aria-label="Loading service health">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : health.data ? (
            <>
              <div>
                {(health.data.checks ?? []).map((check) => (
                  <SubsystemRow
                    key={check.name}
                    name={check.name}
                    status={check.status}
                    detail={check.detail}
                    latencyMs={check.latency_ms}
                  />
                ))}
              </div>
              <dl className="mt-4 grid grid-cols-2 gap-4 border-t border-border pt-4 sm:grid-cols-3">
                <Field label="Version" value={health.data.version} mono />
                <Field label="Uptime" value={formatDuration(health.data.uptime_seconds)} />
              </dl>
            </>
          ) : (
            <p className="py-6 text-center text-sm text-muted-foreground">
              Health information is unavailable.
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Instance settings</CardTitle>
          <CardDescription>
            The limits and features this server is configured with.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {config.isPending ? (
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-3" role="status" aria-label="Loading settings">
              {Array.from({ length: 6 }, (_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : config.isError ? (
            <Alert tone="warning" title="Settings could not be loaded">
              The server is reachable but did not return its configuration.
            </Alert>
          ) : config.data ? (
            <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3">
              <Field label="Instance name" value={config.data.app_name} />
              <Field label="Maximum upload" value={formatBytes(config.data.max_upload_bytes)} />
              <Field label="Upload chunk size" value={formatBytes(config.data.chunk_size_bytes)} />
              <Field
                label="Trash retention"
                value={
                  config.data.trash_retention_days > 0
                    ? `${config.data.trash_retention_days} days`
                    : 'Kept indefinitely'
                }
              />
              <Field label="File versioning" value={config.data.versioning_enabled ? 'On' : 'Off'} />
              <Field label="Sharing" value={config.data.sharing_enabled ? 'On' : 'Off'} />
            </dl>
          ) : null}
        </CardContent>
      </Card>
    </div>
  )
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={`mt-0.5 truncate text-sm font-medium ${mono ? 'font-mono' : ''}`}>{value}</dd>
    </div>
  )
}

function HealthError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const message =
    error instanceof ApiError
      ? error.message
      : 'Something went wrong while contacting the server.'
  const requestId = error instanceof ApiError ? error.requestId : undefined

  return (
    <Alert
      tone="danger"
      title="Cannot reach the PiFiles server"
      action={
        <Button variant="outline" size="sm" onClick={onRetry}>
          Try again
        </Button>
      }
    >
      <p>{message}</p>
      {requestId ? (
        <p className="mt-1 font-mono text-xs">Request ID: {requestId}</p>
      ) : null}
    </Alert>
  )
}
