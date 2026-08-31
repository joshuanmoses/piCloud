import { cn } from '@/lib/utils'
import type { Tag } from '@/types/api'

/**
 * A row of tag chips.
 *
 * The colour comes from the server, which validates it as a plain six-digit hex
 * value and rejects anything else. It is applied through the `style` prop —
 * a value React escapes into a single CSS property — rather than interpolated
 * into a class name or a stylesheet, so even a value that somehow got past
 * validation cannot become a rule of its own.
 */
export function TagChips({
  tags,
  className,
  onRemove,
}: {
  tags: readonly Tag[]
  className?: string
  onRemove?: ((tag: Tag) => void) | undefined
}) {
  if (tags.length === 0) return null

  return (
    <ul className={cn('flex flex-wrap items-center gap-1', className)}>
      {tags.map((tag) => (
        <li key={tag.id}>
          <span
            className="inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-[11px] leading-none"
            style={
              tag.color
                ? { borderColor: tag.color, color: tag.color }
                : undefined
            }
          >
            <span
              aria-hidden="true"
              className="size-1.5 rounded-full bg-current"
            />
            {tag.name}
            {onRemove ? (
              <button
                type="button"
                onClick={(event) => {
                  event.stopPropagation()
                  onRemove(tag)
                }}
                aria-label={`Remove the tag ${tag.name}`}
                className="ml-0.5 rounded-full px-0.5 hover:bg-muted focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              >
                ×
              </button>
            ) : null}
          </span>
        </li>
      ))}
    </ul>
  )
}
