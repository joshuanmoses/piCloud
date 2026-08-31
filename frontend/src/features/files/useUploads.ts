import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { nodeKeys } from '@/api/nodes'
import { useToast } from '@/components/ui/toast'
import { UploadQueue, type UploadItem } from './uploadQueue'

/**
 * Binds the upload queue to React.
 *
 * The queue itself is constructed once and lives for the session, so transfers
 * survive re-renders and navigation between folders. It reports only state
 * changes; every side effect — refreshing the listing, reporting a failure,
 * warning before a reload — is driven by effects here, watching that state.
 *
 * That split matters: the queue never captures React values, so it cannot hold
 * a stale query client or a toast handle from a render that has since been
 * replaced.
 */
export function useUploads() {
  const [items, setItems] = useState<UploadItem[]>([])
  const [dismissed, setDismissed] = useState(false)
  const queryClient = useQueryClient()
  const toast = useToast()

  // useState's lazy initialiser constructs the queue exactly once.
  const [queue] = useState(() => new UploadQueue({ onChange: setItems }))

  // Each item is acted on once, tracked by id so a re-render cannot repeat a
  // toast or a refetch.
  const handled = useRef({ completed: new Set<string>(), failed: new Set<string>() })

  useEffect(() => {
    let anyCompleted = false

    for (const item of items) {
      if (item.status === 'completed' && !handled.current.completed.has(item.id)) {
        handled.current.completed.add(item.id)
        anyCompleted = true
      }
      if (item.status === 'failed' && !handled.current.failed.has(item.id)) {
        handled.current.failed.add(item.id)
        toast.show({
          tone: 'danger',
          title: `Could not upload ${item.file.name}`,
          description: item.error,
        })
      }
    }

    if (anyCompleted) {
      // Refresh every cached folder listing: the upload may have landed in one
      // the user is not currently looking at. The session query carries the
      // storage total shown in the sidebar, and a replacement adds a version,
      // so the history is refreshed too.
      void queryClient.invalidateQueries({ queryKey: nodeKeys.lists() })
      void queryClient.invalidateQueries({ queryKey: ['auth', 'session'] })
      void queryClient.invalidateQueries({ queryKey: ['versions'] })
    }
  }, [items, queryClient, toast])

  // Closing the tab mid-transfer loses the upload. The browser only shows a
  // generic warning, but it is enough to stop an accidental reload.
  const hasActive = items.some((i) => i.status === 'uploading' || i.status === 'queued')
  useEffect(() => {
    if (!hasActive) return
    const onBeforeUnload = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [hasActive])

  const add = useCallback(
    (files: File[], parentId: string, replaceId?: string) => {
      if (files.length === 0) return
      setDismissed(false)
      queue.add(files, parentId, replaceId)
    },
    [queue],
  )

  return useMemo(
    () => ({
      items: dismissed ? [] : items,
      hasActive,
      add,
      cancel: (id: string) => queue.cancel(id),
      retry: (id: string) => queue.retry(id),
      clearFinished: () => queue.clearFinished(),
      dismiss: () => setDismissed(true),
      cancelAll: () => queue.cancelAll(),
    }),
    [items, dismissed, hasActive, add, queue],
  )
}
