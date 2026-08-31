import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Star } from 'lucide-react'
import { addFavorite, removeFavorite } from '@/api/productivity'
import { nodeKeys } from '@/api/nodes'
import { useToast } from '@/components/ui/toast'
import { cn } from '@/lib/utils'
import type { FileNode } from '@/types/api'

interface FavoriteButtonProps {
  node: FileNode
  /** Rendered without a background, for use inside a row. */
  subtle?: boolean
  className?: string
}

/**
 * The star toggle.
 *
 * Favouriting is private to each user, so the only feedback needed is the star
 * itself — no toast on success, which would be noise on a control whose whole
 * point is that it is a single quiet click. A failure does get a toast, because
 * silently doing nothing is the one outcome the user cannot interpret.
 */
export function FavoriteButton({ node, subtle = false, className }: FavoriteButtonProps) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const isFavorite = node.favorite === true

  const toggle = useMutation({
    mutationFn: () => (isFavorite ? removeFavorite(node.id) : addFavorite(node.id)),
    onSuccess: async () => {
      // The listings that show a star, and the Favourites page itself.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: nodeKeys.lists() }),
        queryClient.invalidateQueries({ queryKey: nodeKeys.detail(node.id) }),
        queryClient.invalidateQueries({ queryKey: ['favorites'] }),
      ])
    },
    onError: (error: unknown) => {
      toast.show({
        title: isFavorite ? 'Could not remove the favourite' : 'Could not add the favourite',
        description: error instanceof Error ? error.message : undefined,
        tone: 'danger',
      })
    },
  })

  return (
    <button
      type="button"
      // The row underneath is a click target; a star click must not also open
      // the file.
      onClick={(event) => {
        event.stopPropagation()
        toggle.mutate()
      }}
      onDoubleClick={(event) => event.stopPropagation()}
      disabled={toggle.isPending}
      aria-pressed={isFavorite}
      aria-label={isFavorite ? `Remove ${node.name} from favourites` : `Add ${node.name} to favourites`}
      title={isFavorite ? 'Remove from favourites' : 'Add to favourites'}
      className={cn(
        'inline-flex size-7 shrink-0 items-center justify-center rounded-md',
        'text-muted-foreground transition-colors hover:text-warning focus-visible:outline-none',
        'focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50',
        !subtle && 'hover:bg-surface-muted',
        // On a pointer device an unfavourited star only appears on hover, so a
        // long list is not a wall of grey stars. It is always visible once set,
        // always visible on touch, and always visible when focused.
        !isFavorite && !subtle && 'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100',
        !isFavorite && !subtle && 'focus-visible:opacity-100 [@media(hover:none)]:opacity-100',
        className,
      )}
    >
      <Star
        className={cn('size-4', isFavorite && 'fill-warning text-warning')}
        aria-hidden="true"
      />
    </button>
  )
}
