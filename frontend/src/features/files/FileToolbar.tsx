import {
  ArrowDownAZ,
  ArrowUpAZ,
  FolderPlus,
  LayoutGrid,
  List,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Menu,
  MenuContent,
  MenuLabel,
  MenuRadioGroup,
  MenuRadioItem,
  MenuSeparator,
  MenuTrigger,
} from '@/components/ui/menu'
import type { SortField, SortOrder } from '@/types/api'

export type ViewMode = 'list' | 'grid'

interface FileToolbarProps {
  onNewFolder: () => void
  onUpload: () => void
  view: ViewMode
  onViewChange: (view: ViewMode) => void
  sort: SortField
  order: SortOrder
  onSortChange: (sort: SortField, order: SortOrder) => void
  selectedCount: number
  onClearSelection: () => void
  onDeleteSelected: () => void
  onDownloadSelected: () => void
  disabled?: boolean
}

const sortLabels: Record<SortField, string> = {
  name: 'Name',
  size: 'Size',
  modified: 'Last modified',
  created: 'Date added',
}

export function FileToolbar({
  onNewFolder,
  onUpload,
  view,
  onViewChange,
  sort,
  order,
  onSortChange,
  selectedCount,
  onClearSelection,
  onDeleteSelected,
  onDownloadSelected,
  disabled,
}: FileToolbarProps) {
  // With a selection active the toolbar switches to acting on it, which is
  // what a file manager does and keeps the two sets of actions from competing
  // for space on a phone.
  if (selectedCount > 0) {
    return (
      <div className="flex flex-wrap items-center gap-2 rounded-card border border-primary/30 bg-primary-muted px-3 py-2">
        <Button variant="ghost" size="icon-sm" onClick={onClearSelection} aria-label="Clear selection">
          <X aria-hidden="true" />
        </Button>
        <span className="text-sm font-medium" aria-live="polite">
          {selectedCount} selected
        </span>

        <div className="ml-auto flex items-center gap-1.5">
          <Button variant="outline" size="sm" onClick={onDownloadSelected}>
            Download
          </Button>
          <Button variant="outline" size="sm" onClick={onDeleteSelected}>
            <Trash2 aria-hidden="true" />
            Delete
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button onClick={onNewFolder} size="sm" variant="outline" disabled={disabled}>
        <FolderPlus aria-hidden="true" />
        New folder
      </Button>

      <Button onClick={onUpload} size="sm" disabled={disabled}>
        <Upload aria-hidden="true" />
        Upload
      </Button>

      <div className="ml-auto flex items-center gap-1.5">
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="sm">
              {order === 'asc' ? (
                <ArrowDownAZ aria-hidden="true" />
              ) : (
                <ArrowUpAZ aria-hidden="true" />
              )}
              <span className="hidden sm:inline">{sortLabels[sort]}</span>
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            <MenuLabel>Sort by</MenuLabel>
            <MenuRadioGroup
              value={sort}
              onValueChange={(value) => onSortChange(value as SortField, order)}
            >
              {(Object.keys(sortLabels) as SortField[]).map((field) => (
                <MenuRadioItem key={field} value={field}>
                  {sortLabels[field]}
                </MenuRadioItem>
              ))}
            </MenuRadioGroup>
            <MenuSeparator />
            <MenuRadioGroup
              value={order}
              onValueChange={(value) => onSortChange(sort, value as SortOrder)}
            >
              <MenuRadioItem value="asc">Ascending</MenuRadioItem>
              <MenuRadioItem value="desc">Descending</MenuRadioItem>
            </MenuRadioGroup>
          </MenuContent>
        </Menu>

        <div className="flex items-center rounded-md border border-border p-0.5" role="group" aria-label="View mode">
          <Button
            variant={view === 'list' ? 'secondary' : 'ghost'}
            size="icon-sm"
            onClick={() => onViewChange('list')}
            aria-label="List view"
            aria-pressed={view === 'list'}
          >
            <List aria-hidden="true" />
          </Button>
          <Button
            variant={view === 'grid' ? 'secondary' : 'ghost'}
            size="icon-sm"
            onClick={() => onViewChange('grid')}
            aria-label="Grid view"
            aria-pressed={view === 'grid'}
          >
            <LayoutGrid aria-hidden="true" />
          </Button>
        </div>
      </div>
    </div>
  )
}
