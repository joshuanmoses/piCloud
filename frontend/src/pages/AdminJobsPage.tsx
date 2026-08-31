import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, RotateCcw } from 'lucide-react'
import { adminKeys, getJobs, retryJob } from '@/api/admin'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { ErrorAlert } from '@/features/files/dialogs'
import { formatRelative } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { BackgroundJob } from '@/types/api'

/**
 * The background queue.
 *
 * Without this page the only evidence that thumbnails have stopped generating,
 * or that a sweep has been failing for a week, is in the log — and nobody reads
 * the log until they already suspect something.
 *
 * A failed job shows its handler's own error, which is safe to display:
 * handlers are written not to put user content or secrets in an error message.
 */
export function AdminJobsPage() {
  const queryClient = useQueryClient()
  const toast = useToast()

  const jobs = useQuery({
    queryKey: adminKeys.jobs(),
    queryFn: ({ signal }) => getJobs(signal),
    refetchInterval: 15_000,
  })

  const retry = useMutation({
    mutationFn: (job: BackgroundJob) => retryJob(job.id),
    onSuccess: async (_result, job) => {
      await queryClient.invalidateQueries({ queryKey: adminKeys.jobs() })
      toast.show({
        tone: 'success',
        title: `${job.kind} has been queued again`,
        description: 'Its attempt count was reset, so it gets the full budget.',
      })
    },
    onError: (error: unknown) => {
      toast.show({
        tone: 'danger',
        title: 'Could not retry that job',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Background work</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Thumbnails, retention sweeps, and reconciliation. Queued in the database, so nothing is
          lost across a restart.
        </p>
      </div>

      {jobs.isError ? (
        <ErrorAlert error={jobs.error} title="Could not load the job queue" />
      ) : jobs.isPending ? (
        <Skeleton className="h-28 w-full" role="status" aria-label="Loading the job queue" />
      ) : (
        <>
          <Card className="p-4">
            <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Figure label="Pending" value={jobs.data.stats.pending} />
              <Figure label="Running" value={jobs.data.stats.running} />
              <Figure label="Succeeded" value={jobs.data.stats.succeeded} />
              <Figure
                label="Failed"
                value={jobs.data.stats.failed}
                tone={jobs.data.stats.failed > 0 ? 'danger' : undefined}
              />
            </dl>
            {jobs.data.stats.oldest_pending_seconds > 300 ? (
              <p className="mt-3 text-xs text-warning-foreground">
                The oldest queued job has been waiting{' '}
                {Math.round(jobs.data.stats.oldest_pending_seconds / 60)} minutes.
              </p>
            ) : null}
          </Card>

          {jobs.data.running.length > 0 ? (
            <section className="space-y-2">
              <h2 className="text-sm font-medium">Running now</h2>
              <JobTable jobs={jobs.data.running} caption="Jobs currently running" />
            </section>
          ) : null}

          <section className="space-y-2">
            <h2 className="text-sm font-medium">Failed</h2>
            {jobs.data.failed.length === 0 ? (
              <div className="flex items-center gap-2 rounded-card border border-dashed border-border px-4 py-8 text-sm text-muted-foreground">
                <CheckCircle2 className="size-4 text-success" aria-hidden="true" />
                Nothing has failed.
              </div>
            ) : (
              <JobTable
                jobs={jobs.data.failed}
                caption="Jobs that gave up"
                onRetry={(job) => retry.mutate(job)}
                retrying={retry.isPending}
              />
            )}
          </section>
        </>
      )}
    </div>
  )
}

function JobTable({
  jobs,
  caption,
  onRetry,
  retrying,
}: {
  jobs: BackgroundJob[]
  caption: string
  onRetry?: (job: BackgroundJob) => void
  retrying?: boolean
}) {
  return (
    <div className="overflow-x-auto rounded-card border border-border">
      <table className="w-full min-w-[36rem] border-collapse">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr className="border-b border-border bg-surface-muted text-left">
            <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
              Job
            </th>
            <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
              Attempts
            </th>
            <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground sm:table-cell">
              Queued
            </th>
            {onRetry ? (
              <th scope="col" className="w-28 px-3 py-2">
                <span className="sr-only">Actions</span>
              </th>
            ) : null}
          </tr>
        </thead>
        <tbody>
          {jobs.map((job) => (
            <tr key={job.id} className="border-b border-border align-top last:border-b-0">
              <td className="px-3 py-2">
                <p className="font-mono text-xs">{job.kind}</p>
                {job.last_error ? (
                  <p className="mt-1 break-words text-xs text-danger">{job.last_error}</p>
                ) : null}
              </td>
              <td className="whitespace-nowrap px-3 py-2 text-xs tabular-nums text-muted-foreground">
                {job.attempts} of {job.max_attempts}
              </td>
              <td className="hidden whitespace-nowrap px-3 py-2 text-xs text-muted-foreground sm:table-cell">
                {formatRelative(job.created_at)}
              </td>
              {onRetry ? (
                <td className="whitespace-nowrap px-3 py-2 text-right">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onRetry(job)}
                    disabled={retrying}
                  >
                    <RotateCcw aria-hidden="true" />
                    Retry
                  </Button>
                </td>
              ) : null}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Figure({ label, value, tone }: { label: string; value: number; tone?: 'danger' | undefined }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={cn('mt-0.5 text-lg font-semibold tabular-nums', tone === 'danger' && 'text-danger')}>
        {value}
      </dd>
    </div>
  )
}
