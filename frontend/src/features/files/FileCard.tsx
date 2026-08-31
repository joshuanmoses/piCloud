import type { DragEvent, KeyboardEvent, MouseEvent, ReactNode } from 'react'
import { Checkbox } from '@/components/ui/checkbox'
import { formatBytes, cn } from '@/lib/utils'
import type { FileNode } from '@/types/api'
import { FavoriteButton } from '@/features/productivity/FavoriteButton'
import { Thumbnail } from '@/features/productivity/Thumbnail'

export interface FileCardProps {
  node: FileNode
  selected: boolean
  focused: boolean
  onClick: (event: MouseEvent) => void
  onDoubleClick: () => void
  onToggleSelect: () => void
  onKeyDown: (event: KeyboardEvent) => void
  onContextMenu: (event: MouseEvent) => void
  menu: ReactNode
  draggable: boolean
  onDragStart: (event: DragEvent) => void
  onDragOver: (event: DragEvent) => void
  onDragLeave: (event: DragEvent) => void
  onDrop: (event: DragEvent) => void
  dropTarget: boolean
}

/**
 * One tile in the grid view.
 *
 * The thumbnail is the point of this view: a grid of identical file icons tells
 * you nothing a list would not tell you better. Files without one fall back to
 * their type icon.
 */
export function FileCard({
  node,
  selected,
  focused,
  onClick,
  onDoubleClick,
  onToggleSelect,
  onKeyDown,
  onContextMenu,
  menu,
  draggable,
  onDragStart,
  onDragOver,
  onDragLeave,
  onDrop,
  dropTarget,
}: FileCardProps) {
  return (
    <div
      role="gridcell"
      tabIndex={focused ? 0 : -1}
      aria-selected={selected}
      data-node-id={node.id}
      draggable={draggable}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      onKeyDown={onKeyDown}
      onContextMenu={onContextMenu}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      className={cn(
        'group relative flex cursor-default flex-col items-center gap-2 rounded-card border p-3 outline-none',
        // As in FileRow: the ring marks real keyboard focus, not the card that
        // happens to hold the roving tabindex.
        'focus-visible:ring-2 focus-visible:ring-ring',
        selected ? 'border-primary bg-primary-muted' : 'border-border hover:bg-muted',
        dropTarget && 'border-primary bg-primary-muted ring-2 ring-primary',
      )}
    >
      <div className="absolute left-2 top-2 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 [&:has(:checked)]:opacity-100 [@media(hover:none)]:opacity-100">
        <Checkbox
          checked={selected}
          onChange={onToggleSelect}
          onClick={(event) => event.stopPropagation()}
          aria-label={`Select ${node.name}`}
        />
      </div>

      <div className="absolute right-1 top-1">{menu}</div>

      <div className="absolute bottom-1 left-1">
        <FavoriteButton node={node} subtle />
      </div>

      <Thumbnail
        node={node}
        className="h-16 w-full rounded"
        iconClassName="size-10"
      />

      <div className="w-full min-w-0 text-center">
        <p className="truncate text-xs font-medium" title={node.name}>
          {node.name}
        </p>
        <p className="mt-0.5 text-[11px] text-muted-foreground">
          {node.kind === 'folder' ? 'Folder' : formatBytes(node.size_bytes)}
        </p>
      </div>
    </div>
  )
}
