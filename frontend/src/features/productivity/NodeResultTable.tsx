import { Download, FolderOpen } from 'lucide-react'
import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { downloadUrl } from '@/api/nodes'
import { folderArchiveUrl } from '@/api/productivity'
import { Button } from '@/components/ui/button'
import { can } from '@/features/files/can'
import { formatRelative } from '@/lib/format'
import { formatBytes, cn } from '@/lib/utils'
import type { FileNode } from '@/types/api'
import { FavoriteButton } from './FavoriteButton'
import { TagChips } from './TagChips'
import { Thumbnail } from './Thumbnail'

interface NodeResultTableProps {
  nodes: readonly FileNode[]
  caption: string
  /** An extra column rendered after the name, e.g. Recent's "what you did". */
  extraColumn?: { header: string; render: (node: FileNode) => ReactNode } | undefined
  /** Called instead of the default open behaviour. */
  onOpen?: ((node: FileNode) => void) | undefined
}

/**
 * The listing shared by Favourites, Recent, and Search.
 *
 * These three pages show the same thing — a flat list of nodes reached from
 * somewhere other than a folder — so they share one table rather than three
 * that drift. The folder browser keeps its own, because it additionally carries
 * selection, drag-and-drop, and keyboard navigation that none of these need.
 */
export function NodeResultTable({ nodes, caption, extraColumn, onOpen }: NodeResultTableProps) {
  const navigate = useNavigate()

  const open = (node: FileNode) => {
    if (onOpen) {
      onOpen(node)
      return
    }
    if (node.kind === 'folder') {
      void navigate(`/files/${node.id}`)
      return
    }
    // A file opens where it lives, with the file highlighted, rather than
    // downloading: the person searched for it, they did not ask for a copy.
    if (node.parent_id) {
      void navigate(`/files/${node.parent_id}?highlight=${node.id}`)
    }
  }

  return (
    <div className="overflow-x-auto rounded-card border border-border">
      <table className="w-full min-w-[36rem] border-collapse">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr className="border-b border-border bg-surface-muted text-left">
            <th scope="col" className="w-10 px-2 py-2">
              <span className="sr-only">Favourite</span>
            </th>
            <th scope="col" className="px-3 py-2 text-xs font-medium text-muted-foreground">
              Name
            </th>
            {extraColumn ? (
              <th
                scope="col"
                className="hidden px-3 py-2 text-xs font-medium text-muted-foreground sm:table-cell"
              >
                {extraColumn.header}
              </th>
            ) : null}
            <th
              scope="col"
              className="hidden px-3 py-2 text-xs font-medium text-muted-foreground md:table-cell"
            >
              Owner
            </th>
            <th
              scope="col"
              className="hidden px-3 py-2 text-xs font-medium text-muted-foreground lg:table-cell"
            >
              Modified
            </th>
            <th scope="col" className="px-3 py-2 text-right text-xs font-medium text-muted-foreground">
              Size
            </th>
            <th scope="col" className="w-20 px-3 py-2">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((node) => (
            <ResultRow
              key={node.id}
              node={node}
              extraColumn={extraColumn}
              onOpen={() => open(node)}
            />
          ))}
        </tbody>
      </table>
    </div>
  )
}

function ResultRow({
  node,
  extraColumn,
  onOpen,
}: {
  node: FileNode
  extraColumn: NodeResultTableProps['extraColumn']
  onOpen: () => void
}) {
  const isFolder = node.kind === 'folder'
  const canDownload = can(node, 'download')

  return (
    <tr
      className="group cursor-default border-b border-border last:border-b-0 hover:bg-muted"
      onDoubleClick={onOpen}
    >
      <td className="px-2 py-2">
        <FavoriteButton node={node} />
      </td>

      <td className="min-w-0 px-3 py-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <Thumbnail node={node} className="size-9" />
          <div className="min-w-0">
            <button
              type="button"
              onClick={onOpen}
              className="block max-w-full truncate rounded text-left text-sm hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {node.name}
            </button>
            {node.tags && node.tags.length > 0 ? (
              <TagChips tags={node.tags} className="mt-1" />
            ) : null}
          </div>
        </div>
      </td>

      {extraColumn ? (
        <td className="hidden whitespace-nowrap px-3 py-2 text-xs text-muted-foreground sm:table-cell">
          {extraColumn.render(node)}
        </td>
      ) : null}

      <td className="hidden whitespace-nowrap px-3 py-2 text-xs text-muted-foreground md:table-cell">
        {node.shared ? (node.owner?.display_name ?? 'Shared with you') : 'You'}
      </td>

      <td className="hidden whitespace-nowrap px-3 py-2 text-xs text-muted-foreground lg:table-cell">
        {formatRelative(node.updated_at)}
      </td>

      <td
        className={cn(
          'whitespace-nowrap px-3 py-2 text-right text-xs tabular-nums text-muted-foreground',
        )}
      >
        {isFolder ? '—' : formatBytes(node.size_bytes)}
      </td>

      <td className="whitespace-nowrap px-3 py-2 text-right">
        <Button
          variant="ghost"
          size="icon-sm"
          asChild
          disabled={!canDownload}
          title={isFolder ? 'Download as ZIP' : 'Download'}
        >
          <a
            href={isFolder ? folderArchiveUrl(node.id) : downloadUrl(node.id, { forceDownload: true })}
            aria-label={isFolder ? `Download ${node.name} as a ZIP archive` : `Download ${node.name}`}
          >
            {isFolder ? <FolderOpen aria-hidden="true" /> : <Download aria-hidden="true" />}
          </a>
        </Button>
      </td>
    </tr>
  )
}
