import { useQuery } from '@tanstack/react-query'
import { Star } from 'lucide-react'
import { listFavorites, productivityKeys } from '@/api/productivity'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorAlert } from '@/features/files/dialogs'
import { NodeResultTable } from '@/features/productivity/NodeResultTable'

/**
 * The signed-in user's bookmarks.
 *
 * Favourites are private: two people may each favourite the same shared folder,
 * and one removing it does not affect the other. Anything the user can no
 * longer see is filtered out server-side, so a favourite on a folder that was
 * later unshared simply stops appearing rather than sitting here as a link that
 * fails when clicked.
 */
export function FavoritesPage() {
  const favorites = useQuery({
    queryKey: productivityKeys.favorites(),
    queryFn: ({ signal }) => listFavorites({}, signal),
  })

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Favourites</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The files and folders you have starred. Only you can see this list.
        </p>
      </div>

      {favorites.isError ? (
        <ErrorAlert error={favorites.error} title="Could not load your favourites" />
      ) : favorites.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading favourites">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : favorites.data.nodes.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <Star className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">No favourites yet</h2>
          <p className="mt-1 max-w-sm text-sm text-muted-foreground">
            Star a file or folder anywhere in PiFiles and it will appear here, ready to reach in
            one click.
          </p>
        </div>
      ) : (
        <>
          <NodeResultTable nodes={favorites.data.nodes} caption="Your favourite files and folders" />
          <p className="text-xs text-muted-foreground">
            {favorites.data.total} {favorites.data.total === 1 ? 'item' : 'items'}
          </p>
        </>
      )}
    </div>
  )
}
