import { useQuery } from '@tanstack/react-query'
import { Download, FolderOpen, Share2, User, Users } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { downloadUrl } from '@/api/nodes'
import { listSharedWithMe, sharingKeys } from '@/api/sharing'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorAlert } from '@/features/files/dialogs'
import { iconFor } from '@/features/files/fileIcons'
import { formatRelative } from '@/lib/format'
import { formatBytes, cn } from '@/lib/utils'
import type { FileNode, Share } from '@/types/api'

/** Everything other people have shared with the signed-in user. */
export function SharedWithMePage() {
  const navigate = useNavigate()

  const shares = useQuery({
    queryKey: sharingKeys.sharedWithMe(),
    queryFn: ({ signal }) => listSharedWithMe(signal),
  })

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Shared with me</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Files and folders other people have given you access to.
        </p>
      </div>

      {shares.isError ? (
        <ErrorAlert error={shares.error} title="Could not load what has been shared with you" />
      ) : shares.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading shared items">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : shares.data.shares.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <Share2 className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">Nothing shared with you yet</h2>
          <p className="mt-1 max-w-sm text-sm text-muted-foreground">
            When somebody shares a file or folder with you, or with a group you belong to, it
            will appear here.
          </p>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[36rem] border-collapse">
            <caption className="sr-only">Items shared with you</caption>
            <thead>
              <tr className="border-b border-border bg-surface-muted text-left">
                <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                  Item
                </th>
                <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground sm:table-cell">
                  Owner
                </th>
                <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground md:table-cell">
                  Shared with
                </th>
                <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
                  You can
                </th>
                <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground lg:table-cell">
                  Shared
                </th>
                <th scope="col" className="w-24 px-3 py-2">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {shares.data.shares.map((share) => (
                <SharedRow
                  key={share.id}
                  share={share}
                  onOpen={() => {
                    if (share.node.kind === 'folder') {
                      void navigate(`/files/${share.node.id}`)
                    } else {
                      window.location.assign(downloadUrl(share.node.id))
                    }
                  }}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function SharedRow({ share, onOpen }: { share: Share; onOpen: () => void }) {
  // iconFor takes a FileNode; the share carries the same shape for what it
  // needs, so a minimal projection avoids duplicating the icon logic.
  const asNode = {
    kind: share.node.kind,
    mime_type: share.node.mime_type,
  } as FileNode
  const { Icon, className } = iconFor(asNode)

  const canDownload = share.permissions.includes('download')
  const viaGroup = share.subject_type === 'group'

  return (
    <tr className="border-b border-border last:border-b-0 hover:bg-muted">
      <td className="min-w-0 px-3 py-2.5">
        <div className="flex min-w-0 items-center gap-2.5">
          <Icon className={cn('size-4 shrink-0', className)} aria-hidden="true" />
          <div className="min-w-0">
            <p className="truncate text-sm font-medium">{share.node.name}</p>
            {share.node.kind === 'file' ? (
              <p className="text-xs text-muted-foreground">{formatBytes(share.node.size_bytes)}</p>
            ) : null}
            {share.message ? (
              <p className="mt-0.5 truncate text-xs italic text-muted-foreground">
                “{share.message}”
              </p>
            ) : null}
          </div>
        </div>
      </td>

      <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground sm:table-cell">
        {share.node.owner?.display_name ?? '—'}
      </td>

      <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground md:table-cell">
        <span className="inline-flex items-center gap-1.5">
          {viaGroup ? (
            <>
              <Users className="size-3" aria-hidden="true" />
              {share.group?.name}
            </>
          ) : (
            <>
              <User className="size-3" aria-hidden="true" />
              You
            </>
          )}
        </span>
      </td>

      <td className="whitespace-nowrap px-3 py-2.5">
        <Badge variant="neutral" className="capitalize">
          {share.role ?? `${share.permissions.length} permissions`}
        </Badge>
      </td>

      <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground lg:table-cell">
        {formatRelative(share.created_at)}
        {share.shared_by ? ` by ${share.shared_by.display_name}` : ''}
      </td>

      <td className="whitespace-nowrap px-3 py-2.5 text-right">
        <Button
          variant="outline"
          size="sm"
          onClick={onOpen}
          disabled={share.node.kind === 'file' && !canDownload}
        >
          {share.node.kind === 'folder' ? (
            <>
              <FolderOpen aria-hidden="true" />
              Open
            </>
          ) : (
            <>
              <Download aria-hidden="true" />
              Get
            </>
          )}
        </Button>
      </td>
    </tr>
  )
}
