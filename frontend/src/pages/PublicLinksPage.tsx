import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link2, Lock, Trash2, Upload } from 'lucide-react'
import { linkKeys, listMyPublicLinks, revokePublicLink } from '@/api/publiclinks'
import { ApiError } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { ErrorAlert } from '@/features/files/dialogs'
import { describeLinkKind, formatRelative } from '@/lib/format'
import type { PublicLink } from '@/types/api'

/** Everything the signed-in user has published, so it can be withdrawn. */
export function PublicLinksPage() {
  const queryClient = useQueryClient()
  const toast = useToast()

  const links = useQuery({
    queryKey: linkKeys.mine(),
    queryFn: ({ signal }) => listMyPublicLinks(signal),
  })

  const revoke = useMutation({
    mutationFn: (id: string) => revokePublicLink(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: linkKeys.all })
      toast.show({ tone: 'success', title: 'Link revoked', description: 'It no longer works.' })
    },
    onError: (error) => {
      toast.show({
        tone: 'danger',
        title: 'Could not revoke the link',
        description: error instanceof ApiError ? error.message : undefined,
      })
    },
  })

  const all = links.data?.links ?? []
  const live = all.filter((link) => link.live)
  const inactive = all.filter((link) => !link.live)

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Public links</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Links you have created that work without a PiFiles account. Revoking one stops it
          immediately.
        </p>
      </div>

      {links.isError ? (
        <ErrorAlert error={links.error} title="Could not load your links" />
      ) : links.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading links">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : all.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <Link2 className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">No public links yet</h2>
          <p className="mt-1 max-w-sm text-sm text-muted-foreground">
            Use “Get a link” on any file or folder to create one. A link works for anybody who
            has it, so only share it with people you trust.
          </p>
        </div>
      ) : (
        <>
          <LinkTable
            links={live}
            onRevoke={(id) => revoke.mutate(id)}
            revoking={revoke.isPending}
          />

          {inactive.length > 0 ? (
            <details className="rounded-card border border-border">
              <summary className="cursor-pointer px-4 py-3 text-sm font-medium">
                {inactive.length} inactive link{inactive.length === 1 ? '' : 's'}
              </summary>
              <div className="border-t border-border">
                <LinkTable links={inactive} onRevoke={() => {}} revoking={false} />
              </div>
            </details>
          ) : null}
        </>
      )}
    </div>
  )
}

function LinkTable({
  links,
  onRevoke,
  revoking,
}: {
  links: PublicLink[]
  onRevoke: (id: string) => void
  revoking: boolean
}) {
  if (links.length === 0) return null

  return (
    <div className="overflow-x-auto rounded-card border border-border">
      <table className="w-full min-w-[36rem] border-collapse">
        <caption className="sr-only">Your public links</caption>
        <thead>
          <tr className="border-b border-border bg-surface-muted text-left">
            <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
              Item
            </th>
            <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
              Allows
            </th>
            <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground sm:table-cell">
              Used
            </th>
            <th scope="col" className="hidden px-3 py-2 text-xs font-medium text-muted-foreground md:table-cell">
              Created
            </th>
            <th scope="col" className="w-20 px-3 py-2">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {links.map((link) => {
            const isUpload = link.kind === 'upload' || link.kind === 'upload_view'
            return (
              <tr key={link.id} className="border-b border-border last:border-b-0 hover:bg-muted">
                <td className="min-w-0 px-3 py-2.5">
                  <div className="flex min-w-0 items-center gap-2">
                    {isUpload ? (
                      <Upload className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                    ) : (
                      <Link2 className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                    )}
                    <span className="truncate text-sm font-medium">
                      {link.node?.name ?? 'Deleted item'}
                    </span>
                    {link.has_password ? (
                      <Lock className="size-3 shrink-0 text-muted-foreground" aria-label="Password protected" />
                    ) : null}
                  </div>
                  {link.title ? (
                    <p className="mt-0.5 truncate text-xs text-muted-foreground">{link.title}</p>
                  ) : null}
                </td>

                <td className="whitespace-nowrap px-3 py-2.5">
                  <Badge variant="neutral">{describeLinkKind(link.kind)}</Badge>
                </td>

                <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground sm:table-cell">
                  {link.max_uses ? `${link.use_count} of ${link.max_uses}` : `${link.use_count}`}
                  {link.last_used_at ? ` · last ${formatRelative(link.last_used_at)}` : ''}
                </td>

                <td className="hidden whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground md:table-cell">
                  {formatRelative(link.created_at)}
                  {link.expires_at ? ` · expires ${formatRelative(link.expires_at)}` : ''}
                </td>

                <td className="whitespace-nowrap px-3 py-2.5 text-right">
                  {link.live ? (
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => onRevoke(link.id)}
                      disabled={revoking}
                      aria-label={`Revoke the link to ${link.node?.name ?? 'this item'}`}
                    >
                      <Trash2 aria-hidden="true" />
                    </Button>
                  ) : (
                    <Badge variant="neutral">{link.revoked_at ? 'Revoked' : 'Expired'}</Badge>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
