import { MoreHorizontal, Share2 } from 'lucide-react'
import type { DragEvent, KeyboardEvent, MouseEvent, ReactNode } from 'react'
import { Checkbox } from '@/components/ui/checkbox'
import { formatRelative } from '@/lib/format'
import { formatBytes, cn } from '@/lib/utils'
import type { FileNode } from '@/types/api'
import { FavoriteButton } from '@/features/productivity/FavoriteButton'
import { TagChips } from '@/features/productivity/TagChips'
import { Thumbnail } from '@/features/productivity/Thumbnail'

export interface FileRowProps {
  node: FileNode
  selected: boolean
  /** True when this row is the keyboard cursor. */
  focused: boolean
  onClick: (event: MouseEvent) => void
  onDoubleClick: () => void
  onToggleSelect: () => void
  onKeyDown: (event: KeyboardEvent) => void
  onContextMenu: (event: MouseEvent) => void
  /** Rendered as the row's action menu. */
  menu: ReactNode
  /** Drag-and-drop movement handlers. */
  draggable: boolean
  onDragStart: (event: DragEvent) => void
  onDragOver: (event: DragEvent) => void
  onDragLeave: (event: DragEvent) => void
  onDrop: (event: DragEvent) => void
  /** True while a drag hovers this row as a drop target. */
  dropTarget: boolean
}

/**
 * One row in the list view.
 *
 * The row is a `tr` with `role="row"` and is focusable, so the whole grid can
 * be driven from the keyboard. `aria-selected` carries the selection state to a
 * screen reader rather than relying on the background colour alone.
 */
export function FileRow({
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
}: FileRowProps) {
  return (
    <tr
      // tabIndex follows the roving-tabindex pattern: exactly one row is in the
      // tab order, and arrow keys move which one.
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
        'group cursor-default border-b border-border outline-none last:border-b-0',
        // The ring follows real keyboard focus, not the roving tabindex. One
        // row always holds tabIndex 0 so Tab can enter the grid; drawing a
        // focus ring around it on load would mark a row nobody has moved to.
        // Arrow navigation calls focus() on the row it moves to, so the ring
        // appears the moment the keyboard is actually in use.
        'focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring',
        selected ? 'bg-primary-muted' : 'hover:bg-muted',
        dropTarget && 'bg-primary-muted ring-2 ring-inset ring-primary',
      )}
    >
      <td className="w-10 py-2 pl-3 pr-0">
        <Checkbox
          checked={selected}
          onChange={onToggleSelect}
          // The row already carries the selection state; the checkbox would
          // otherwise be announced as a second, redundant control.
          onClick={(event) => event.stopPropagation()}
          aria-label={`Select ${node.name}`}
        />
      </td>

      <td className="min-w-0 py-2 pl-2 pr-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <Thumbnail node={node} className="size-7" iconClassName="size-4" />
          <div className="min-w-0">
            <span className="flex min-w-0 items-center gap-1.5">
              <span className="truncate text-sm">{node.name}</span>
              {node.version_count ? (
                <span
                  className="shrink-0 rounded bg-surface-muted px-1 text-[10px] leading-4 text-muted-foreground"
                  title={`${node.version_count} earlier ${node.version_count === 1 ? 'version' : 'versions'}`}
                >
                  v{node.version_count + 1}
                </span>
              ) : null}
            </span>
            {node.tags && node.tags.length > 0 ? (
              <TagChips tags={node.tags} className="mt-0.5" />
            ) : null}
          </div>
        </div>
      </td>

      <td className="w-10 px-1 py-2">
        <FavoriteButton node={node} />
      </td>

      <td className="hidden whitespace-nowrap px-3 py-2 text-xs text-muted-foreground md:table-cell">
        {node.owner?.display_name ?? '—'}
      </td>

      <td className="hidden whitespace-nowrap px-3 py-2 text-xs text-muted-foreground sm:table-cell">
        {formatRelative(node.updated_at)}
      </td>

      <td className="whitespace-nowrap px-3 py-2 text-right text-xs tabular-nums text-muted-foreground">
        {node.kind === 'folder' ? '—' : formatBytes(node.size_bytes)}
      </td>

      <td className="hidden whitespace-nowrap px-3 py-2 text-xs lg:table-cell">
        {node.shared ? (
          <span className="inline-flex items-center gap-1 text-primary">
            <Share2 className="size-3" aria-hidden="true" />
            Shared with you
          </span>
        ) : node.shared_out ? (
          <span className="inline-flex items-center gap-1 text-primary">
            <Share2 className="size-3" aria-hidden="true" />
            Shared
          </span>
        ) : (
          <span className="text-muted-foreground/60">Private</span>
        )}
      </td>

      <td className="w-10 py-2 pl-0 pr-2 text-right">{menu}</td>
    </tr>
  )
}

/** The action-menu trigger, styled to appear on hover or focus. */
export function RowMenuButton() {
  return (
    <span
      className={cn(
        'inline-flex size-7 items-center justify-center rounded-md text-muted-foreground',
        'opacity-0 transition-opacity hover:bg-surface-muted focus-visible:opacity-100',
        'group-hover:opacity-100 group-focus-within:opacity-100',
        // Always visible on touch, where there is no hover.
        '[@media(hover:none)]:opacity-100',
      )}
    >
      <MoreHorizontal className="size-4" aria-hidden="true" />
    </span>
  )
}
