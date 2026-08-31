import { ChevronRight, Home } from 'lucide-react'
import type { DragEvent } from 'react'
import { cn } from '@/lib/utils'
import type { Breadcrumb } from '@/types/api'

interface BreadcrumbsProps {
  items: Breadcrumb[]
  onNavigate: (id: string) => void
  /** Dropping onto an ancestor moves items up the tree. */
  onDropOn?: (id: string, event: DragEvent) => void
  dropTargetId?: string | null
  onDragOver?: (id: string, event: DragEvent) => void
  onDragLeave?: (event: DragEvent) => void
}

/** How many trailing crumbs stay visible before the middle is collapsed. */
const VISIBLE_TAIL = 3

export function Breadcrumbs({
  items,
  onNavigate,
  onDropOn,
  dropTargetId,
  onDragOver,
  onDragLeave,
}: BreadcrumbsProps) {
  if (items.length === 0) return null

  // A deep path would otherwise wrap onto several lines on a phone. The first
  // and last few are kept, since those are what people navigate to.
  const collapsed = items.length > VISIBLE_TAIL + 2
  const shown = collapsed
    ? [items[0]!, null, ...items.slice(-VISIBLE_TAIL)]
    : items

  return (
    <nav aria-label="Folder path" className="min-w-0">
      <ol className="flex min-w-0 items-center gap-0.5 text-sm">
        {shown.map((item, index) => {
          if (item === null) {
            return (
              <li key="ellipsis" className="flex items-center gap-0.5">
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="px-1 text-muted-foreground" aria-hidden="true">
                  …
                </span>
              </li>
            )
          }

          const isLast = index === shown.length - 1
          const label = item.is_root ? 'My Files' : item.name

          return (
            <li key={item.id} className="flex min-w-0 items-center gap-0.5">
              {index > 0 ? (
                <ChevronRight
                  className="size-3.5 shrink-0 text-muted-foreground"
                  aria-hidden="true"
                />
              ) : null}

              {isLast ? (
                <span
                  aria-current="page"
                  className="truncate rounded px-1.5 py-1 font-medium"
                  title={label}
                >
                  {item.is_root ? (
                    <span className="flex items-center gap-1.5">
                      <Home className="size-3.5" aria-hidden="true" />
                      {label}
                    </span>
                  ) : (
                    label
                  )}
                </span>
              ) : (
                <button
                  type="button"
                  onClick={() => onNavigate(item.id)}
                  onDragOver={(event) => onDragOver?.(item.id, event)}
                  onDragLeave={onDragLeave}
                  onDrop={(event) => onDropOn?.(item.id, event)}
                  className={cn(
                    'truncate rounded px-1.5 py-1 text-muted-foreground hover:bg-muted hover:text-foreground',
                    dropTargetId === item.id && 'bg-primary-muted text-primary ring-1 ring-primary',
                  )}
                  title={label}
                >
                  {item.is_root ? (
                    <span className="flex items-center gap-1.5">
                      <Home className="size-3.5" aria-hidden="true" />
                      <span className="hidden sm:inline">{label}</span>
                    </span>
                  ) : (
                    label
                  )}
                </button>
              )}
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
