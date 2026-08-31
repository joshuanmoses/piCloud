import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, History, RotateCcw, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { nodeKeys } from '@/api/nodes'
import {
  deleteVersion,
  listVersions,
  restoreVersion,
  versionDownloadUrl,
  versionKeys,
} from '@/api/versions'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { can } from '@/features/files/can'
import { ErrorAlert } from '@/features/files/dialogs'
import { formatRelative } from '@/lib/format'
import { formatBytes, cn } from '@/lib/utils'
import type { FileNode, FileVersion } from '@/types/api'

/**
 * A file's version history.
 *
 * The current state is listed first and marked as such, because "which one am I
 * looking at now" is the first question anybody asks of a version list, and
 * leaving it implicit makes the numbering read as off-by-one.
 *
 * Restoring is presented as an ordinary action rather than behind a warning: it
 * is not destructive. The content it displaces becomes a version of its own, so
 * a mistaken restore is one more click from being undone. Deleting a version
 * *is* destructive, and asks first.
 */
export function VersionHistory({ node }: { node: FileNode }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [deleting, setDeleting] = useState<FileVersion | null>(null)

  const history = useQuery({
    queryKey: versionKeys.forNode(node.id),
    queryFn: ({ signal }) => listVersions(node.id, signal),
    enabled: node.kind === 'file',
  })

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: versionKeys.forNode(node.id) }),
      queryClient.invalidateQueries({ queryKey: nodeKeys.lists() }),
      queryClient.invalidateQueries({ queryKey: nodeKeys.detail(node.id) }),
    ])
  }

  const restore = useMutation({
    mutationFn: (version: FileVersion) => restoreVersion(node.id, version.id),
    onSuccess: async (_result, version) => {
      await refresh()
      toast.show({
        tone: 'success',
        title: `Restored version ${version.version}`,
        description:
          'What the file contained before is now the newest version, so this can be undone.',
      })
    },
    onError: (error: unknown) => {
      toast.show({
        tone: 'danger',
        title: 'Could not restore that version',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  const remove = useMutation({
    mutationFn: (version: FileVersion) => deleteVersion(node.id, version.id),
    onSuccess: async () => {
      setDeleting(null)
      await refresh()
    },
    onError: (error: unknown) => {
      setDeleting(null)
      toast.show({
        tone: 'danger',
        title: 'Could not delete that version',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  if (node.kind !== 'file') return null

  const canRestore = can(node, 'modify')
  const canDelete = can(node, 'delete')
  const canDownload = can(node, 'download')

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
        <History className="size-3.5" aria-hidden="true" />
        <span>Version history</span>
      </div>

      {history.isError ? (
        <ErrorAlert error={history.error} title="Could not load the version history" />
      ) : history.isPending ? (
        <div className="space-y-1.5" role="status" aria-label="Loading version history">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : (
        <ul className="divide-y divide-border rounded-card border border-border">
          <li className="flex items-center gap-3 px-3 py-2">
            <Badge variant="success" className="shrink-0">
              Now
            </Badge>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{history.data.current.name}</p>
              <p className="text-xs text-muted-foreground">
                {formatBytes(history.data.current.size_bytes)} ·{' '}
                {formatRelative(history.data.current.updated_at)}
              </p>
            </div>
          </li>

          {history.data.versions.length === 0 ? (
            <li className="px-3 py-4 text-center text-xs text-muted-foreground">
              No earlier versions yet. One is kept each time the file is replaced.
            </li>
          ) : (
            history.data.versions.map((version) => (
              <VersionRow
                key={version.id}
                nodeId={node.id}
                version={version}
                canRestore={canRestore}
                canDelete={canDelete}
                canDownload={canDownload}
                restoring={restore.isPending && restore.variables.id === version.id}
                onRestore={() => restore.mutate(version)}
                onDelete={() => setDeleting(version)}
              />
            ))
          )}
        </ul>
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={deleting ? `Delete version ${deleting.version}?` : 'Delete this version?'}
        description="This removes that copy permanently. Nothing else brings it back — unlike a restore, which can be undone."
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

function VersionRow({
  nodeId,
  version,
  canRestore,
  canDelete,
  canDownload,
  restoring,
  onRestore,
  onDelete,
}: {
  nodeId: string
  version: FileVersion
  canRestore: boolean
  canDelete: boolean
  canDownload: boolean
  restoring: boolean
  onRestore: () => void
  onDelete: () => void
}) {
  return (
    <li className="group flex items-center gap-3 px-3 py-2">
      <Badge variant="neutral" className="shrink-0 tabular-nums">
        v{version.version}
      </Badge>

      <div className="min-w-0 flex-1">
        <p className="truncate text-sm">{version.name}</p>
        <p className="text-xs text-muted-foreground">
          {formatBytes(version.size_bytes)} · {formatRelative(version.created_at)}
          {version.created_by ? ` · ${version.created_by.display_name}` : ''}
        </p>
        <p className="text-[11px] text-muted-foreground/70">{version.reason_label}</p>
      </div>

      <div
        className={cn(
          'flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity',
          'group-hover:opacity-100 group-focus-within:opacity-100',
          '[@media(hover:none)]:opacity-100',
        )}
      >
        <Button
          variant="ghost"
          size="icon-sm"
          asChild
          disabled={!canDownload}
          title="Download this version"
        >
          <a
            href={versionDownloadUrl(nodeId, version.id)}
            aria-label={`Download version ${version.version}`}
          >
            <Download aria-hidden="true" />
          </a>
        </Button>

        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onRestore}
          disabled={!canRestore || restoring}
          title={
            canRestore
              ? 'Make this the current version'
              : 'You do not have permission to change this file'
          }
          aria-label={`Restore version ${version.version}`}
        >
          <RotateCcw aria-hidden="true" />
        </Button>

        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onDelete}
          disabled={!canDelete}
          title={
            canDelete
              ? 'Delete this version permanently'
              : 'You do not have permission to delete from this file'
          }
          aria-label={`Delete version ${version.version}`}
        >
          <Trash2 aria-hidden="true" />
        </Button>
      </div>
    </li>
  )
}
