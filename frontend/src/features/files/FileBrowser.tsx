import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Copy,
  Download,
  FileArchive,
  FolderOpen,
  Info,
  Link2,
  Pencil,
  Share2,
  Star,
  Trash2,
  Upload,
} from 'lucide-react'
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type KeyboardEvent,
  type MouseEvent,
} from 'react'
import {
  copyNode,
  createFolder,
  deleteNode,
  downloadUrl,
  listNodes,
  nodeKeys,
  updateNode,
} from '@/api/nodes'
import {
  addFavorite,
  folderArchiveUrl,
  removeFavorite,
  selectionArchiveUrl,
} from '@/api/productivity'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/dialog'
import {
  Menu,
  MenuContent,
  MenuItem,
  MenuSeparator,
  MenuTrigger,
} from '@/components/ui/menu'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { cn } from '@/lib/utils'
import type { FileNode, SortField, SortOrder } from '@/types/api'
import { Breadcrumbs } from './Breadcrumbs'
import { DetailsDialog, ErrorAlert, NameDialog } from './dialogs'
import { FileCard } from './FileCard'
import { FileRow, RowMenuButton } from './FileRow'
import { FileToolbar } from './FileToolbar'
import { usePreferences, type ViewMode } from '@/stores/preferences'
import { UploadPanel } from './UploadPanel'
import { PublicLinkDialog } from '@/features/links/PublicLinkDialog'
import { ShareDialog } from '@/features/sharing/ShareDialog'
import { previewKindFor } from '@/lib/preview'
import { PreviewDialog } from '@/features/productivity/PreviewDialog'
import { can } from './can'
import { useSelection } from './useSelection'
import { useUploads } from './useUploads'

const PAGE_SIZE = 100

interface FileBrowserProps {
  folderId: string
  onNavigate: (folderId: string) => void
}

export function FileBrowser({ folderId, onNavigate }: FileBrowserProps) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const uploads = useUploads()

  const view = usePreferences((state) => state.view)
  const setView = usePreferences((state) => state.setView)
  const [sort, setSort] = useState<SortField>('name')
  const [order, setOrder] = useState<SortOrder>('asc')
  const [offset, setOffset] = useState(0)

  const [newFolderOpen, setNewFolderOpen] = useState(false)
  const [renaming, setRenaming] = useState<FileNode | null>(null)
  // The details dialog holds an *identifier*, not a node.
  //
  // Restoring a version changes the file's size and modification time while the
  // dialog is open. Holding the node object would leave those fields showing
  // what they were when the dialog opened, contradicting the version list
  // immediately below them. Resolving from the listing on every render means
  // the dialog follows the refetch.
  const [detailsOfId, setDetailsOfId] = useState<string | null>(null)
  const [detailsFallback, setDetailsFallback] = useState<FileNode | null>(null)
  const [previewing, setPreviewing] = useState<FileNode | null>(null)
  const [replacing, setReplacing] = useState<FileNode | null>(null)
  const [sharingNode, setSharingNode] = useState<FileNode | null>(null)
  const [linkingNode, setLinkingNode] = useState<FileNode | null>(null)
  const [deleting, setDeleting] = useState<FileNode[] | null>(null)

  const [focusedId, setFocusedId] = useState<string | null>(null)
  const [dropTargetId, setDropTargetId] = useState<string | null>(null)
  const [draggingOverRoot, setDraggingOverRoot] = useState(false)

  const fileInputRef = useRef<HTMLInputElement>(null)
  const replaceInputRef = useRef<HTMLInputElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)

  const listParams = useMemo(
    () => ({ parentId: folderId, limit: PAGE_SIZE, offset, sort, order }),
    [folderId, offset, sort, order],
  )

  const listing = useQuery({
    queryKey: nodeKeys.list(listParams),
    queryFn: ({ signal }) => listNodes(listParams, signal),
    // Keeping the previous folder's data during navigation makes the
    // transition feel instant instead of flashing a skeleton.
    placeholderData: (previous) => previous,
  })

  const nodes = useMemo(() => listing.data?.nodes ?? [], [listing.data])
  const orderedIds = useMemo(() => nodes.map((n) => n.id), [nodes])
  const selection = useSelection(orderedIds)

  // Drop selections and the keyboard cursor when the underlying items change.
  useEffect(() => {
    selection.reconcile(orderedIds)
    setFocusedId((current) => (current && orderedIds.includes(current) ? current : null))
  }, [orderedIds, selection])

  // Navigating to a different folder resets pagination and selection.
  useEffect(() => {
    setOffset(0)
    selection.clear()
    setFocusedId(null)
    // selection.clear is stable; folderId is the real dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [folderId])

  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: nodeKeys.lists() })
    void queryClient.invalidateQueries({ queryKey: ['auth', 'session'] })
  }, [queryClient])

  // --- Mutations ------------------------------------------------------------

  const createFolderMutation = useMutation({
    mutationFn: (name: string) => createFolder(folderId, name),
    onSuccess: (result) => {
      setNewFolderOpen(false)
      refresh()
      toast.show({ tone: 'success', title: `Created “${result.node.name}”` })
    },
  })

  const renameMutation = useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) => updateNode(id, { name }),
    onSuccess: (result) => {
      setRenaming(null)
      refresh()
      toast.show({ tone: 'success', title: `Renamed to “${result.node.name}”` })
    },
  })

  const moveMutation = useMutation({
    mutationFn: ({ id, parentId }: { id: string; parentId: string }) =>
      updateNode(id, { parent_id: parentId }),
    onSuccess: () => refresh(),
    onError: (error) => {
      toast.show({
        tone: 'danger',
        title: 'Could not move the item',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  const copyMutation = useMutation({
    mutationFn: (id: string) => copyNode(id, folderId),
    onSuccess: (result) => {
      refresh()
      toast.show({ tone: 'success', title: `Copied as “${result.node.name}”` })
    },
    onError: (error) => {
      toast.show({
        tone: 'danger',
        title: 'Could not copy',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: async (targets: FileNode[]) => {
      // Sequential rather than parallel: a Pi handles a burst of deletes
      // poorly, and the ordering makes a partial failure comprehensible.
      for (const node of targets) {
        await deleteNode(node.id)
      }
      return targets
    },
    onSuccess: (targets) => {
      setDeleting(null)
      selection.clear()
      refresh()
      toast.show({
        tone: 'success',
        title:
          targets.length === 1
            ? `“${targets[0]?.name}” moved to trash`
            : `${targets.length} items moved to trash`,
        description: 'You can restore it from Trash.',
      })
    },
    onError: (error) => {
      setDeleting(null)
      refresh()
      toast.show({
        tone: 'danger',
        title: 'Could not delete everything',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  // --- Actions --------------------------------------------------------------

  const openNode = useCallback(
    (node: FileNode) => {
      if (node.kind === 'folder') {
        onNavigate(node.id)
        return
      }
      // Anything the browser can render opens in the preview. Opening a
      // photograph should show it, not put it in the downloads folder.
      if (previewKindFor(node.mime_type) !== 'none') {
        setPreviewing(node)
        return
      }
      // Everything else downloads through a normal navigation, so the
      // browser's own download manager handles it.
      window.location.assign(downloadUrl(node.id))
    },
    [onNavigate],
  )

  /**
   * Downloads what is selected.
   *
   * One file goes straight to the browser's download manager. Anything else —
   * several files, or any folder — becomes one streamed ZIP, which is both what
   * the user meant and far kinder than opening eight download tabs.
   */
  const downloadSelected = useCallback(() => {
    const targets = nodes.filter((n) => selection.isSelected(n.id))
    if (targets.length === 0) return

    const single = targets[0]
    if (targets.length === 1 && single && single.kind === 'file') {
      window.location.assign(downloadUrl(single.id, { forceDownload: true }))
      return
    }

    // A ZIP has no Content-Length, so the browser shows an indeterminate
    // progress bar. Worth saying once, because a large folder can take a while
    // before anything appears to be happening.
    toast.show({
      tone: 'info',
      title: 'Preparing your download',
      description:
        'The archive is built as it downloads, so the total size is not known in advance.',
    })
    window.location.assign(selectionArchiveUrl(targets.map((n) => n.id)))
  }, [nodes, selection, toast])

  /**
   * Stars or unstars a node from the context menu.
   *
   * Favouriting is private to each user and changes nothing about the file, so
   * the only feedback is the star itself; a toast on every click would be
   * noise. A failure does get one, because doing nothing silently is the one
   * outcome the user cannot interpret.
   */
  const favoriteMutation = useMutation({
    mutationFn: (node: FileNode) =>
      node.favorite === true ? removeFavorite(node.id) : addFavorite(node.id),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: nodeKeys.lists() }),
        queryClient.invalidateQueries({ queryKey: ['favorites'] }),
      ])
    },
    onError: (error: unknown) => {
      toast.show({
        tone: 'danger',
        title: 'Could not change the favourite',
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  const toggleFavorite = useCallback(
    (node: FileNode) => favoriteMutation.mutate(node),
    [favoriteMutation],
  )

  // Prefer the listing's copy; fall back to the node the dialog was opened
  // with, so an item that has scrolled out of the current page still renders.
  const detailsOf =
    detailsOfId === null
      ? null
      : (nodes.find((n) => n.id === detailsOfId) ?? detailsFallback)

  const showDetails = useCallback((node: FileNode) => {
    setDetailsOfId(node.id)
    setDetailsFallback(node)
  }, [])

  const chooseFiles = useCallback(() => {
    setReplacing(null)
    fileInputRef.current?.click()
  }, [])

  /**
   * Opens the picker to overwrite one file rather than add new ones.
   *
   * Replacing is how a version gets made, so this is the only route in the
   * interface to file history. Dragging a same-named file in would be a name
   * conflict instead, which is the right default: an accidental drop must not
   * silently overwrite somebody's work.
   */
  const chooseReplacement = useCallback((node: FileNode) => {
    setReplacing(node)
    replaceInputRef.current?.click()
  }, [])

  const handleFilesChosen = useCallback(
    (fileList: FileList | null) => {
      if (!fileList || fileList.length === 0) return
      uploads.add(Array.from(fileList), folderId)
      if (fileInputRef.current) fileInputRef.current.value = ''
    },
    [uploads, folderId],
  )

  const handleReplacementChosen = useCallback(
    (fileList: FileList | null) => {
      const file = fileList?.[0]
      const target = replacing
      if (replaceInputRef.current) replaceInputRef.current.value = ''
      setReplacing(null)
      if (!file || !target) return

      uploads.add([file], target.parent_id ?? folderId, target.id)
    },
    [uploads, replacing, folderId],
  )

  // --- Drag and drop --------------------------------------------------------

  /** True when the drag carries files from the desktop rather than a node. */
  const isFileDrag = (event: DragEvent) =>
    Array.from(event.dataTransfer.types).includes('Files')

  const handleRootDragOver = useCallback((event: DragEvent) => {
    if (!isFileDrag(event)) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
    setDraggingOverRoot(true)
  }, [])

  const handleRootDrop = useCallback(
    (event: DragEvent) => {
      if (!isFileDrag(event)) return
      event.preventDefault()
      setDraggingOverRoot(false)
      handleFilesChosen(event.dataTransfer.files)
    },
    [handleFilesChosen],
  )

  const handleNodeDragStart = useCallback(
    (node: FileNode) => (event: DragEvent) => {
      // Dragging an unselected item selects it first, so what is dragged is
      // always what is highlighted.
      const ids = selection.isSelected(node.id) ? [...selection.selected] : [node.id]
      if (!selection.isSelected(node.id)) selection.select([node.id])

      event.dataTransfer.setData('application/x-pifiles-nodes', JSON.stringify(ids))
      event.dataTransfer.effectAllowed = 'move'
    },
    [selection],
  )

  const handleNodeDragOver = useCallback(
    (node: FileNode) => (event: DragEvent) => {
      // Only a folder can receive a dropped node, and never one being dragged.
      const carriesNodes = Array.from(event.dataTransfer.types).includes(
        'application/x-pifiles-nodes',
      )
      if (node.kind !== 'folder') return
      if (!carriesNodes && !isFileDrag(event)) return
      if (selection.isSelected(node.id)) return

      event.preventDefault()
      event.stopPropagation()
      event.dataTransfer.dropEffect = carriesNodes ? 'move' : 'copy'
      setDropTargetId(node.id)
    },
    [selection],
  )

  const handleNodeDrop = useCallback(
    (node: FileNode) => (event: DragEvent) => {
      if (node.kind !== 'folder') return
      event.preventDefault()
      event.stopPropagation()
      setDropTargetId(null)

      if (isFileDrag(event)) {
        uploads.add(Array.from(event.dataTransfer.files), node.id)
        return
      }

      const raw = event.dataTransfer.getData('application/x-pifiles-nodes')
      if (!raw) return
      let ids: string[]
      try {
        ids = JSON.parse(raw) as string[]
      } catch {
        return
      }

      for (const id of ids) {
        if (id === node.id) continue
        moveMutation.mutate({ id, parentId: node.id })
      }
      selection.clear()
    },
    [uploads, moveMutation, selection],
  )

  /** Dropping onto a breadcrumb moves items up the tree. */
  const handleBreadcrumbDrop = useCallback(
    (targetId: string, event: DragEvent) => {
      event.preventDefault()
      setDropTargetId(null)
      const raw = event.dataTransfer.getData('application/x-pifiles-nodes')
      if (!raw) return
      try {
        for (const id of JSON.parse(raw) as string[]) {
          moveMutation.mutate({ id, parentId: targetId })
        }
      } catch {
        return
      }
      selection.clear()
    },
    [moveMutation, selection],
  )

  // --- Keyboard navigation --------------------------------------------------

  const handleItemKeyDown = useCallback(
    (node: FileNode, index: number) => (event: KeyboardEvent) => {
      const move = (nextIndex: number) => {
        const clamped = Math.max(0, Math.min(nodes.length - 1, nextIndex))
        const target = nodes[clamped]
        if (!target) return
        event.preventDefault()
        setFocusedId(target.id)
        containerRef.current
          ?.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(target.id)}"]`)
          ?.focus()
        if (!event.shiftKey) selection.select([target.id])
      }

      switch (event.key) {
        case 'ArrowDown':
          move(index + 1)
          break
        case 'ArrowUp':
          move(index - 1)
          break
        case 'Home':
          move(0)
          break
        case 'End':
          move(nodes.length - 1)
          break
        case 'Enter':
          event.preventDefault()
          openNode(node)
          break
        case ' ':
          event.preventDefault()
          selection.toggle(node.id)
          break
        case 'Delete':
        case 'Backspace': {
          event.preventDefault()
          const targets = selection.count > 0
            ? nodes.filter((n) => selection.isSelected(n.id))
            : [node]
          setDeleting(targets)
          break
        }
        case 'F2':
          event.preventDefault()
          setRenaming(node)
          break
        case 'a':
          if (event.ctrlKey || event.metaKey) {
            event.preventDefault()
            selection.selectAll()
          }
          break
        case 'Escape':
          event.preventDefault()
          selection.clear()
          break
      }
    },
    [nodes, selection, openNode],
  )

  // --- Rendering ------------------------------------------------------------

  const buildMenu = useCallback(
    (node: FileNode) => (
      <Menu>
        <MenuTrigger asChild>
          <button
            type="button"
            aria-label={`Actions for ${node.name}`}
            onClick={(event: MouseEvent) => event.stopPropagation()}
          >
            <RowMenuButton />
          </button>
        </MenuTrigger>
        <MenuContent align="end">
          <MenuItem onSelect={() => openNode(node)}>
            {node.kind === 'folder' ? <FolderOpen /> : <Download />}
            {node.kind === 'folder' ? 'Open' : 'Download'}
          </MenuItem>
          {node.kind === 'folder' ? (
            <MenuItem
              onSelect={() => window.location.assign(folderArchiveUrl(node.id))}
              disabled={!can(node, 'download')}
            >
              <FileArchive />
              Download as ZIP
            </MenuItem>
          ) : null}
          {node.kind === 'file' ? (
            <MenuItem
              onSelect={() => chooseReplacement(node)}
              disabled={!can(node, 'modify')}
            >
              <Upload />
              Replace file…
            </MenuItem>
          ) : null}
          <MenuItem onSelect={() => toggleFavorite(node)}>
            <Star className={node.favorite ? 'fill-current' : undefined} />
            {node.favorite ? 'Remove from favourites' : 'Add to favourites'}
          </MenuItem>
          <MenuItem
            onSelect={() => setRenaming(node)}
            disabled={!can(node, 'rename')}
          >
            <Pencil />
            Rename
          </MenuItem>
          <MenuItem onSelect={() => copyMutation.mutate(node.id)}>
            <Copy />
            Make a copy
          </MenuItem>
          <MenuItem
            onSelect={() => setSharingNode(node)}
            // The server checks again on every request; hiding the item here
            // only avoids offering an action that would be refused.
            disabled={!can(node, 'share')}
          >
            <Share2 />
            Share with people
          </MenuItem>
          <MenuItem onSelect={() => setLinkingNode(node)} disabled={!can(node, 'share')}>
            <Link2 />
            Get a link
          </MenuItem>
          <MenuItem onSelect={() => showDetails(node)}>
            <Info />
            Details
          </MenuItem>
          <MenuSeparator />
          <MenuItem
            destructive
            onSelect={() => setDeleting([node])}
            disabled={!can(node, 'delete')}
          >
            <Trash2 />
            Move to trash
          </MenuItem>
        </MenuContent>
      </Menu>
    ),
    [openNode, copyMutation, toggleFavorite, chooseReplacement, showDetails],
  )

  const total = listing.data?.total ?? 0
  const hasMore = listing.data?.has_more ?? false

  return (
    <div
      ref={containerRef}
      onDragOver={handleRootDragOver}
      onDragLeave={(event) => {
        // Only clear when the pointer actually leaves the container, not when
        // it crosses between children.
        if (!event.currentTarget.contains(event.relatedTarget as Node)) {
          setDraggingOverRoot(false)
        }
      }}
      onDrop={handleRootDrop}
      onClick={(event) => {
        // Clicking empty space clears the selection, as a file manager does.
        if (event.target === event.currentTarget) selection.clear()
      }}
      className={cn(
        'relative space-y-4 rounded-card',
        draggingOverRoot && 'outline-2 outline-dashed outline-offset-4 outline-primary',
      )}
    >
      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="sr-only"
        onChange={(event) => handleFilesChosen(event.target.files)}
        aria-hidden="true"
        tabIndex={-1}
      />

      {/* A separate input, single-file: replacing is a one-for-one operation
          and letting the picker offer several would leave the question of which
          one wins. */}
      <input
        ref={replaceInputRef}
        type="file"
        className="sr-only"
        onChange={(event) => handleReplacementChosen(event.target.files)}
        aria-hidden="true"
        tabIndex={-1}
      />

      {listing.data ? (
        <Breadcrumbs
          items={listing.data.breadcrumbs}
          onNavigate={onNavigate}
          onDropOn={handleBreadcrumbDrop}
          dropTargetId={dropTargetId}
          onDragOver={(id, event) => {
            if (Array.from(event.dataTransfer.types).includes('application/x-pifiles-nodes')) {
              event.preventDefault()
              setDropTargetId(id)
            }
          }}
          onDragLeave={() => setDropTargetId(null)}
        />
      ) : null}

      <FileToolbar
        onNewFolder={() => setNewFolderOpen(true)}
        onUpload={chooseFiles}
        view={view}
        onViewChange={setView}
        sort={sort}
        order={order}
        onSortChange={(nextSort, nextOrder) => {
          setSort(nextSort)
          setOrder(nextOrder)
          setOffset(0)
        }}
        selectedCount={selection.count}
        onClearSelection={selection.clear}
        onDeleteSelected={() => setDeleting(nodes.filter((n) => selection.isSelected(n.id)))}
        onDownloadSelected={downloadSelected}
        disabled={listing.isError}
      />

      {listing.isError ? (
        <ErrorAlert error={listing.error} title="Could not load this folder" />
      ) : listing.isPending ? (
        <LoadingState view={view} />
      ) : nodes.length === 0 ? (
        <EmptyState onUpload={chooseFiles} onNewFolder={() => setNewFolderOpen(true)} />
      ) : view === 'list' ? (
        <ListView
          nodes={nodes}
          selection={selection}
          focusedId={focusedId}
          dropTargetId={dropTargetId}
          onOpen={openNode}
          onKeyDown={handleItemKeyDown}
          buildMenu={buildMenu}
          onDragStart={handleNodeDragStart}
          onDragOver={handleNodeDragOver}
          onDragLeave={() => setDropTargetId(null)}
          onDrop={handleNodeDrop}
          setFocusedId={setFocusedId}
        />
      ) : (
        <GridView
          nodes={nodes}
          selection={selection}
          focusedId={focusedId}
          dropTargetId={dropTargetId}
          onOpen={openNode}
          onKeyDown={handleItemKeyDown}
          buildMenu={buildMenu}
          onDragStart={handleNodeDragStart}
          onDragOver={handleNodeDragOver}
          onDragLeave={() => setDropTargetId(null)}
          onDrop={handleNodeDrop}
          setFocusedId={setFocusedId}
        />
      )}

      {total > PAGE_SIZE ? (
        <div className="flex items-center justify-between border-t border-border pt-3">
          <p className="text-xs text-muted-foreground">
            Showing {offset + 1}–{Math.min(offset + nodes.length, total)} of {total}
          </p>
          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
              disabled={offset === 0}
            >
              Previous
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setOffset(offset + PAGE_SIZE)}
              disabled={!hasMore}
            >
              Next
            </Button>
          </div>
        </div>
      ) : null}

      <NameDialog
        open={newFolderOpen}
        onOpenChange={(open) => {
          setNewFolderOpen(open)
          if (!open) createFolderMutation.reset()
        }}
        title="New folder"
        label="Folder name"
        submitLabel="Create"
        pending={createFolderMutation.isPending}
        error={createFolderMutation.error}
        onSubmit={(name) => createFolderMutation.mutate(name)}
      />

      <NameDialog
        open={renaming !== null}
        onOpenChange={(open) => {
          if (!open) {
            setRenaming(null)
            renameMutation.reset()
          }
        }}
        title="Rename"
        label="Name"
        initialValue={renaming?.name ?? ''}
        submitLabel="Rename"
        selectBaseName={renaming?.kind === 'file'}
        pending={renameMutation.isPending}
        error={renameMutation.error}
        onSubmit={(name) => {
          if (renaming) renameMutation.mutate({ id: renaming.id, name })
        }}
      />

      <DetailsDialog
        open={detailsOfId !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDetailsOfId(null)
            setDetailsFallback(null)
          }
        }}
        node={detailsOf}
      />

      <PreviewDialog
        open={previewing !== null}
        onOpenChange={(open) => {
          if (!open) setPreviewing(null)
        }}
        node={previewing}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={
          deleting?.length === 1 ? `Move “${deleting[0]?.name}” to trash?` : 'Move items to trash?'
        }
        description={
          deleting?.length === 1 && deleting[0]?.kind === 'folder'
            ? 'The folder and everything inside it will be moved to trash. You can restore it later.'
            : `${deleting?.length ?? 0} item${deleting?.length === 1 ? '' : 's'} will be moved to trash. You can restore ${deleting?.length === 1 ? 'it' : 'them'} later.`
        }
        confirmLabel="Move to trash"
        destructive
        pending={deleteMutation.isPending}
        onConfirm={() => {
          if (deleting) deleteMutation.mutate(deleting)
        }}
      />

      <ShareDialog
        node={sharingNode}
        open={sharingNode !== null}
        onOpenChange={(open) => {
          if (!open) setSharingNode(null)
        }}
      />

      <PublicLinkDialog
        node={linkingNode}
        open={linkingNode !== null}
        onOpenChange={(open) => {
          if (!open) setLinkingNode(null)
        }}
      />

      <UploadPanel
        items={uploads.items}
        onCancel={uploads.cancel}
        onRetry={uploads.retry}
        onClearFinished={uploads.clearFinished}
        onDismiss={uploads.dismiss}
      />
    </div>
  )
}

// --- Views ------------------------------------------------------------------

interface ViewProps {
  nodes: FileNode[]
  selection: ReturnType<typeof useSelection>
  focusedId: string | null
  dropTargetId: string | null
  onOpen: (node: FileNode) => void
  onKeyDown: (node: FileNode, index: number) => (event: KeyboardEvent) => void
  buildMenu: (node: FileNode) => React.ReactNode
  onDragStart: (node: FileNode) => (event: DragEvent) => void
  onDragOver: (node: FileNode) => (event: DragEvent) => void
  onDragLeave: () => void
  onDrop: (node: FileNode) => (event: DragEvent) => void
  setFocusedId: (id: string) => void
}

function ListView({
  nodes,
  selection,
  focusedId,
  dropTargetId,
  onOpen,
  onKeyDown,
  buildMenu,
  onDragStart,
  onDragOver,
  onDragLeave,
  onDrop,
  setFocusedId,
}: ViewProps) {
  // Exactly one row carries tabIndex 0 (the roving tabindex pattern), so Tab
  // enters the grid once and arrows move within it.
  const cursorId = focusedId ?? nodes[0]?.id ?? null

  return (
    <div className="overflow-x-auto rounded-card border border-border">
      <table className="w-full min-w-[36rem] border-collapse">
        <caption className="sr-only">
          Files and folders. Use the arrow keys to move, Enter to open, Space to select, F2 to
          rename, and Delete to move to trash.
        </caption>
        <thead>
          <tr className="border-b border-border bg-surface-muted text-left">
            <th scope="col" className="w-10 px-3 py-2">
              <span className="sr-only">Select</span>
            </th>
            <th scope="col" className="px-2 py-2 text-xs font-medium text-muted-foreground">
              Name
            </th>
            <th scope="col" className="w-10 px-1 py-2">
              <span className="sr-only">Favourite</span>
            </th>
            <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground md:table-cell">
              Owner
            </th>
            <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground sm:table-cell">
              Modified
            </th>
            <th scope="col" className="px-3 py-2 text-right text-xs font-medium text-muted-foreground">
              Size
            </th>
            <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground lg:table-cell">
              Sharing
            </th>
            <th scope="col" className="w-10 px-2 py-2">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((node, index) => (
            <FileRow
              key={node.id}
              node={node}
              selected={selection.isSelected(node.id)}
              focused={cursorId === node.id}
              onClick={(event) => {
                setFocusedId(node.id)
                selection.handleClick(node.id, event)
              }}
              onDoubleClick={() => onOpen(node)}
              onToggleSelect={() => selection.toggle(node.id)}
              onKeyDown={onKeyDown(node, index)}
              onContextMenu={() => {
                if (!selection.isSelected(node.id)) selection.select([node.id])
              }}
              menu={buildMenu(node)}
              draggable
              onDragStart={onDragStart(node)}
              onDragOver={onDragOver(node)}
              onDragLeave={onDragLeave}
              onDrop={onDrop(node)}
              dropTarget={dropTargetId === node.id}
            />
          ))}
        </tbody>
      </table>
    </div>
  )
}

function GridView({
  nodes,
  selection,
  focusedId,
  dropTargetId,
  onOpen,
  onKeyDown,
  buildMenu,
  onDragStart,
  onDragOver,
  onDragLeave,
  onDrop,
  setFocusedId,
}: ViewProps) {
  const cursorId = focusedId ?? nodes[0]?.id ?? null

  return (
    <div
      role="grid"
      aria-label="Files and folders"
      className="grid grid-cols-2 gap-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6"
    >
      {nodes.map((node, index) => (
        <FileCard
          key={node.id}
          node={node}
          selected={selection.isSelected(node.id)}
          focused={cursorId === node.id}
          onClick={(event) => {
            setFocusedId(node.id)
            selection.handleClick(node.id, event)
          }}
          onDoubleClick={() => onOpen(node)}
          onToggleSelect={() => selection.toggle(node.id)}
          onKeyDown={onKeyDown(node, index)}
          onContextMenu={() => {
            if (!selection.isSelected(node.id)) selection.select([node.id])
          }}
          menu={buildMenu(node)}
          draggable
          onDragStart={onDragStart(node)}
          onDragOver={onDragOver(node)}
          onDragLeave={onDragLeave}
          onDrop={onDrop(node)}
          dropTarget={dropTargetId === node.id}
        />
      ))}
    </div>
  )
}

function LoadingState({ view }: { view: ViewMode }) {
  return (
    <div role="status" aria-label="Loading folder contents">
      {view === 'list' ? (
        <div className="space-y-px rounded-card border border-border p-1">
          {Array.from({ length: 8 }, (_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6">
          {Array.from({ length: 12 }, (_, i) => (
            <Skeleton key={i} className="h-28 w-full" />
          ))}
        </div>
      )}
    </div>
  )
}

function EmptyState({
  onUpload,
  onNewFolder,
}: {
  onUpload: () => void
  onNewFolder: () => void
}) {
  return (
    <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
      <FolderOpen className="size-8 text-muted-foreground" aria-hidden="true" />
      <h2 className="mt-3 text-sm font-medium">This folder is empty</h2>
      <p className="mt-1 max-w-xs text-sm text-muted-foreground">
        Drag files here to upload them, or use the buttons below.
      </p>
      <div className="mt-4 flex gap-2">
        <Button variant="outline" size="sm" onClick={onNewFolder}>
          New folder
        </Button>
        <Button size="sm" onClick={onUpload}>
          Upload files
        </Button>
      </div>
    </div>
  )
}
