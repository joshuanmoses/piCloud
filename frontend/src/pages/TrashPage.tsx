import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RotateCcw, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { deleteNode, listTrash, nodeKeys, restoreNode } from '@/api/nodes'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { ErrorAlert } from '@/features/files/dialogs'
import { iconFor } from '@/features/files/fileIcons'
import { formatRelative } from '@/lib/format'
import { formatBytes, cn } from '@/lib/utils'
import type { FileNode } from '@/types/api'

export function TrashPage() {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [purging, setPurging] = useState<FileNode | null>(null)

  const trash = useQuery({
    queryKey: nodeKeys.trash(),
    queryFn: ({ signal }) => listTrash({ limit: 200 }, signal),
  })

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: nodeKeys.trash() })
    void queryClient.invalidateQueries({ queryKey: nodeKeys.lists() })
    void queryClient.invalidateQueries({ queryKey: ['auth', 'session'] })
  }

  const restore = useMutation({
    mutationFn: (id: string) => restoreNode(id),
    onSuccess: (result) => {
      refresh()
      toast.show({ tone: 'success', title: `Restored “${result.node.name}”` })
    },
    onError: (error) => {
      toast.show({
        tone: 'danger',
        title: 'Could not restore',
        // The server explains the two real cases: a name collision, or a
        // parent folder that is itself still in the trash.
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  const purge = useMutation({
    mutationFn: (id: string) => deleteNode(id, true),
    onSuccess: () => {
      setPurging(null)
      refresh()
      toast.show({ tone: 'success', title: 'Deleted permanently' })
    },
    onError: (error) => {
      setPurging(null)
      toast.show({
        tone: 'danger',
        title: 'Could not delete',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  const nodes = trash.data?.nodes ?? []

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Trash</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Deleted items are kept here so you can restore them. They still count as deleted, and
          the server removes them permanently after the retention period.
        </p>
      </div>

      {trash.isError ? (
        <ErrorAlert error={trash.error} title="Could not load your trash" />
      ) : trash.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading trash">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : nodes.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <Trash2 className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">Trash is empty</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Items you delete will appear here.
          </p>
        </div>
      ) : (
        <ul className="divide-y divide-border rounded-card border border-border">
          {nodes.map((node) => {
            const { Icon, className } = iconFor(node)
            return (
              <li key={node.id} className="flex items-center gap-3 p-3">
                <Icon className={cn('size-4 shrink-0', className)} aria-hidden="true" />

                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{node.name}</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {node.kind === 'folder' ? 'Folder' : formatBytes(node.size_bytes)}
                    {node.deleted_at ? ` · deleted ${formatRelative(node.deleted_at)}` : ''}
                  </p>
                </div>

                <div className="flex shrink-0 gap-1.5">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => restore.mutate(node.id)}
                    disabled={restore.isPending}
                  >
                    <RotateCcw aria-hidden="true" />
                    <span className="hidden sm:inline">Restore</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => setPurging(node)}
                    aria-label={`Delete ${node.name} permanently`}
                  >
                    <Trash2 aria-hidden="true" />
                  </Button>
                </div>
              </li>
            )
          })}
        </ul>
      )}

      <ConfirmDialog
        open={purging !== null}
        onOpenChange={(open) => {
          if (!open) setPurging(null)
        }}
        title={`Permanently delete “${purging?.name}”?`}
        description={
          purging?.kind === 'folder'
            ? 'This folder and everything inside it will be deleted permanently. This cannot be undone.'
            : 'This file will be deleted permanently. This cannot be undone.'
        }
        confirmLabel="Delete permanently"
        destructive
        pending={purge.isPending}
        onConfirm={() => {
          if (purging) purge.mutate(purging.id)
        }}
      />
    </div>
  )
}
