import { useQuery } from '@tanstack/react-query'
import { Clock } from 'lucide-react'
import { listRecent, productivityKeys } from '@/api/productivity'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorAlert } from '@/features/files/dialogs'
import { NodeResultTable } from '@/features/productivity/NodeResultTable'
import { formatRelative } from '@/lib/format'
import type { FileNode, RecentAction, RecentEntry } from '@/types/api'

/**
 * What the signed-in user last touched.
 *
 * One entry per file per kind of action, not an activity log: five downloads of
 * the same file are one row saying when you last downloaded it, which is what
 * this view is actually for. Anything the user can no longer see is filtered
 * out server-side.
 */
export function RecentPage() {
  const recent = useQuery({
    queryKey: productivityKeys.recent(),
    queryFn: ({ signal }) => listRecent({}, signal),
  })

  const byId = new Map<string, RecentEntry>()
  for (const entry of recent.data?.items ?? []) byId.set(entry.id, entry)

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Recent</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Files you have opened, uploaded, or downloaded lately.
        </p>
      </div>

      {recent.isError ? (
        <ErrorAlert error={recent.error} title="Could not load your recent activity" />
      ) : recent.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading recent activity">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : recent.data.items.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <Clock className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">Nothing here yet</h2>
          <p className="mt-1 max-w-sm text-sm text-muted-foreground">
            As you open, upload, and download files, the ones you touched most recently will
            appear here.
          </p>
        </div>
      ) : (
        <NodeResultTable
          nodes={recent.data.items}
          caption="Your recent activity"
          extraColumn={{
            header: 'What you did',
            render: (node: FileNode) => {
              const entry = byId.get(node.id)
              if (!entry) return null
              return (
                <span>
                  {describeAction(entry.action)}{' '}
                  <span className="text-muted-foreground/70">
                    {formatRelative(entry.occurred_at)}
                  </span>
                </span>
              )
            },
          }}
        />
      )}
    </div>
  )
}

/** The past tense a person would use, rather than the enum value. */
function describeAction(action: RecentAction): string {
  switch (action) {
    case 'viewed':
      return 'Opened'
    case 'downloaded':
      return 'Downloaded'
    case 'uploaded':
      return 'Uploaded'
    case 'modified':
      return 'Edited'
  }
}
