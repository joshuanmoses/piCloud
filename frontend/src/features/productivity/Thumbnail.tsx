import { useState } from 'react'
import { previewUrl } from '@/api/productivity'
import { iconFor } from '@/features/files/fileIcons'
import { cn } from '@/lib/utils'
import type { FileNode } from '@/types/api'

interface ThumbnailProps {
  node: FileNode
  /** Tailwind size classes for the box. Defaults to a list-row square. */
  className?: string
  /** Icon size classes used when there is no thumbnail. */
  iconClassName?: string
}

/**
 * A node's thumbnail, falling back to its type icon.
 *
 * The image is a plain `<img>` rather than a fetch: the browser caches it,
 * revalidates it with the ETag, and never holds it in JavaScript memory — which
 * matters when a folder of two hundred photographs scrolls past.
 *
 * `has_preview` comes from the listing, so a file known to have no thumbnail
 * never issues a request at all. When it is true and the request still fails —
 * a preview lost to a disk problem, a file replaced a moment ago — onError
 * falls back to the icon rather than showing a broken image.
 */
export function Thumbnail({ node, className, iconClassName }: ThumbnailProps) {
  const [failed, setFailed] = useState(false)
  const { Icon, className: iconTone } = iconFor(node)

  const showImage = node.kind === 'file' && node.has_preview === true && !failed

  if (!showImage) {
    return (
      <span
        className={cn('flex shrink-0 items-center justify-center', className ?? 'size-9')}
        aria-hidden="true"
      >
        <Icon className={cn(iconClassName ?? 'size-4', iconTone)} />
      </span>
    )
  }

  return (
    <span
      className={cn(
        'flex shrink-0 items-center justify-center overflow-hidden rounded border border-border bg-surface-muted',
        className ?? 'size-9',
      )}
    >
      <img
        src={previewUrl(node.id)}
        // Decorative: the filename is already in the row beside it, and
        // repeating it here would have a screen reader say it twice.
        alt=""
        loading="lazy"
        decoding="async"
        onError={() => setFailed(true)}
        className="size-full object-cover"
      />
    </span>
  )
}
