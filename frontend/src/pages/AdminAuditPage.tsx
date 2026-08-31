import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { ScrollText, X } from 'lucide-react'
import { useSearchParams } from 'react-router-dom'
import { adminKeys, getAuditVocabulary, listAudit } from '@/api/admin'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorAlert } from '@/features/files/dialogs'
import { formatRelative } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { AuditEvent, AuditOutcome, AuditQuery } from '@/types/api'

/** How many events one page shows. */
const PAGE_SIZE = 50

/**
 * The audit trail.
 *
 * Filters live in the URL, so a view an administrator arrived at during an
 * incident can be bookmarked and shared with a colleague — which is exactly
 * when somebody wants to.
 *
 * An event whose actor has since been deleted keeps its recorded name and
 * simply has no profile to link to. That is the point of storing the name
 * alongside the identifier: deleting an account must not make its history
 * anonymous.
 */
export function AdminAuditPage() {
  const [params, setParams] = useSearchParams()

  const query: AuditQuery = {
    limit: PAGE_SIZE,
    offset: parseInt(params.get('offset') ?? '0', 10) || 0,
  }
  const action = params.get('action') ?? ''
  const outcome = (params.get('outcome') as AuditOutcome | null) ?? undefined
  const search = params.get('q') ?? ''
  if (action) query.action = action
  if (outcome) query.outcome = outcome
  if (search) query.q = search

  const events = useQuery({
    queryKey: adminKeys.audit(query),
    queryFn: ({ signal }) => listAudit(query, signal),
    placeholderData: keepPreviousData,
  })

  // The action list comes from the server, so the filter cannot offer something
  // that is never recorded or miss something that is.
  const vocabulary = useQuery({
    queryKey: adminKeys.auditVocabulary(),
    queryFn: ({ signal }) => getAuditVocabulary(signal),
    staleTime: 60 * 60 * 1000,
  })

  const setParam = (key: string, value: string | undefined) => {
    const next = new URLSearchParams(params)
    if (value === undefined || value === '') next.delete(key)
    else next.set(key, value)
    // Any filter change returns to the first page; staying on page 4 of a
    // different result set is never what somebody meant.
    next.delete('offset')
    setParams(next, { replace: true })
  }

  const activeFilters = (action ? 1 : 0) + (outcome ? 1 : 0) + (search ? 1 : 0)

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Audit trail</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Who did what, and when. Kept separately from the log so it survives rotation.
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-48 flex-1">
          <label htmlFor="audit-action" className="mb-1 block text-xs text-muted-foreground">
            Action
          </label>
          <select
            id="audit-action"
            value={action}
            onChange={(event) => setParam('action', event.target.value)}
            className="h-9 w-full rounded-md border border-border bg-surface px-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">Everything</option>
            {(vocabulary.data?.actions ?? []).map((entry) => (
              <option key={entry.action} value={entry.action}>
                {entry.label}
              </option>
            ))}
          </select>
        </div>

        <div className="min-w-36">
          <label htmlFor="audit-outcome" className="mb-1 block text-xs text-muted-foreground">
            Outcome
          </label>
          <select
            id="audit-outcome"
            value={outcome ?? ''}
            onChange={(event) => setParam('outcome', event.target.value)}
            className="h-9 w-full rounded-md border border-border bg-surface px-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">Any</option>
            <option value="success">Succeeded</option>
            <option value="failure">Failed</option>
            <option value="denied">Refused</option>
          </select>
        </div>

        <div className="min-w-48 flex-1">
          <label htmlFor="audit-search" className="mb-1 block text-xs text-muted-foreground">
            Person or item
          </label>
          <Input
            id="audit-search"
            type="search"
            value={search}
            onChange={(event) => setParam('q', event.target.value)}
            placeholder="Name…"
          />
        </div>

        {activeFilters > 0 ? (
          <Button variant="ghost" size="sm" onClick={() => setParams({}, { replace: true })}>
            <X aria-hidden="true" />
            Clear
          </Button>
        ) : null}
      </div>

      {events.isError ? (
        <ErrorAlert error={events.error} title="Could not load the audit trail" />
      ) : events.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading the audit trail">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : events.data.events.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <ScrollText className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">Nothing matches</h2>
          <p className="mt-1 max-w-sm text-sm text-muted-foreground">
            {activeFilters > 0
              ? 'No recorded events match these filters.'
              : 'Nothing has been recorded yet.'}
          </p>
        </div>
      ) : (
        <>
          <p className="text-sm text-muted-foreground" role="status">
            {events.data.total} {events.data.total === 1 ? 'event' : 'events'}
          </p>

          <div className="overflow-x-auto rounded-card border border-border">
            <table className="w-full min-w-[48rem] border-collapse">
              <caption className="sr-only">Recorded events</caption>
              <thead>
                <tr className="border-b border-border bg-surface-muted text-left">
                  <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                    When
                  </th>
                  <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                    Action
                  </th>
                  <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                    Who
                  </th>
                  <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                    What
                  </th>
                  <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground lg:table-cell">
                    From
                  </th>
                </tr>
              </thead>
              <tbody>
                {events.data.events.map((event) => (
                  <AuditRow key={event.id} event={event} />
                ))}
              </tbody>
            </table>
          </div>

          <div className="flex items-center justify-between gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={(query.offset ?? 0) === 0}
              onClick={() =>
                setParams((current) => {
                  const next = new URLSearchParams(current)
                  const back = Math.max(0, (query.offset ?? 0) - PAGE_SIZE)
                  if (back === 0) next.delete('offset')
                  else next.set('offset', String(back))
                  return next
                })
              }
            >
              Newer
            </Button>
            <span className="text-xs text-muted-foreground">
              {(query.offset ?? 0) + 1}–{(query.offset ?? 0) + events.data.events.length} of{' '}
              {events.data.total}
            </span>
            <Button
              variant="outline"
              size="sm"
              disabled={!events.data.has_more}
              onClick={() =>
                setParams((current) => {
                  const next = new URLSearchParams(current)
                  next.set('offset', String((query.offset ?? 0) + PAGE_SIZE))
                  return next
                })
              }
            >
              Older
            </Button>
          </div>
        </>
      )}
    </div>
  )
}

function AuditRow({ event }: { event: AuditEvent }) {
  return (
    <tr className="border-b border-border align-top last:border-b-0">
      <td className="whitespace-nowrap px-3 py-2 text-xs text-muted-foreground">
        <time dateTime={event.created_at} title={new Date(event.created_at).toLocaleString()}>
          {formatRelative(event.created_at)}
        </time>
      </td>

      <td className="px-3 py-2">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-sm">{event.label}</span>
          {event.outcome !== 'success' ? (
            <Badge variant={event.outcome === 'denied' ? 'warning' : 'danger'}>
              {event.outcome === 'denied' ? 'Refused' : 'Failed'}
            </Badge>
          ) : null}
        </div>
        {event.detail && Object.keys(event.detail).length > 0 ? (
          <dl className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
            {Object.entries(event.detail).map(([key, value]) => (
              <div key={key} className="flex gap-1">
                <dt>{key.replace(/_/g, ' ')}:</dt>
                <dd className="font-medium">{describeValue(value)}</dd>
              </div>
            ))}
          </dl>
        ) : null}
      </td>

      <td className="px-3 py-2 text-sm">
        {event.actor ? (
          <span>{event.actor.display_name}</span>
        ) : event.actor_label ? (
          // A recorded name with no profile to link to. Two different things
          // look like this and the interface cannot tell them apart: the
          // account was deleted, or the action was never authenticated — a
          // failed sign-in records the name that was *attempted*, which may
          // belong to nobody. "Not signed in" is true of both; "no account"
          // would be a guess, and wrong half the time.
          <span className="text-muted-foreground">
            {event.actor_label}
            <span className="block text-[11px]">not signed in</span>
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>

      <td className="min-w-0 px-3 py-2 text-sm">
        {event.target_label ? (
          <span className="break-words">{event.target_label}</span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
        {event.target_type ? (
          <span className="block text-[11px] text-muted-foreground">{event.target_type}</span>
        ) : null}
      </td>

      <td
        className={cn(
          'hidden whitespace-nowrap px-3 py-2 text-xs text-muted-foreground lg:table-cell',
        )}
      >
        {event.ip || '—'}
        {event.device ? <span className="block text-[11px]">{event.device}</span> : null}
      </td>
    </tr>
  )
}

/** Renders a detail value without letting an object become "[object Object]". */
function describeValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'boolean') return value ? 'yes' : 'no'
  if (typeof value === 'string' || typeof value === 'number') return String(value)
  if (Array.isArray(value)) return value.map(describeValue).join(', ')
  return JSON.stringify(value)
}
