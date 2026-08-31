import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Search as SearchIcon, SlidersHorizontal, X } from 'lucide-react'
import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { listTags, MIN_QUERY_LENGTH, productivityKeys, search } from '@/api/productivity'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorAlert } from '@/features/files/dialogs'
import { NodeResultTable } from '@/features/productivity/NodeResultTable'
import { cn } from '@/lib/utils'
import type { NodeKind, SearchCategory, SearchParams } from '@/types/api'

/**
 * Search results.
 *
 * The query lives in the URL, so a search is a link: it can be bookmarked,
 * shared with yourself on another device, and reached with the back button.
 * Filters live there too, for the same reason.
 *
 * Everything shown here has already passed the server's permission check. The
 * result count comes from the same query as the rows, so it counts only what
 * the caller may see — a total that included other people's files would leak
 * their existence just as surely as showing their names.
 */
export function SearchPage() {
  const [params, setParams] = useSearchParams()
  const [showFilters, setShowFilters] = useState(false)

  const query = params.get('q') ?? ''
  const kind = (params.get('kind') as NodeKind | null) ?? undefined
  const category = (params.get('type') as SearchCategory | null) ?? undefined
  const ownedByMe = params.get('owned_by_me') === '1'
  const tags = params.getAll('tag')

  const searchParams: SearchParams = {
    q: query,
    ...(kind ? { kind } : {}),
    ...(category ? { type: category } : {}),
    ...(ownedByMe ? { ownedByMe: true } : {}),
    ...(tags.length > 0 ? { tags } : {}),
  }

  const enabled = query.trim().length >= MIN_QUERY_LENGTH

  const results = useQuery({
    queryKey: productivityKeys.search(searchParams),
    queryFn: ({ signal }) => search(searchParams, signal),
    enabled,
    // Keeping the previous page visible while a refined query runs stops the
    // list flashing empty on every keystroke of a filter change.
    placeholderData: keepPreviousData,
  })

  const availableTags = useQuery({
    queryKey: productivityKeys.tags(),
    queryFn: ({ signal }) => listTags(signal),
  })

  /** Replaces one parameter, preserving the rest and resetting nothing else. */
  const setParam = (key: string, value: string | undefined) => {
    const next = new URLSearchParams(params)
    if (value === undefined || value === '') next.delete(key)
    else next.set(key, value)
    setParams(next, { replace: true })
  }

  const toggleTag = (name: string) => {
    const next = new URLSearchParams(params)
    const current = next.getAll('tag')
    next.delete('tag')
    for (const t of current) {
      if (t !== name) next.append('tag', t)
    }
    if (!current.includes(name)) next.append('tag', name)
    setParams(next, { replace: true })
  }

  const activeFilters =
    (kind ? 1 : 0) + (category ? 1 : 0) + (ownedByMe ? 1 : 0) + tags.length

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Search</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Everything you own and everything shared with you.
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-0 flex-1">
          <SearchIcon
            className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={query}
            onChange={(event) => setParam('q', event.target.value)}
            placeholder="Search by name…"
            aria-label="Search files and folders"
            autoFocus
            className="pl-8"
          />
        </div>

        <Button
          variant={activeFilters > 0 ? 'secondary' : 'outline'}
          onClick={() => setShowFilters((open) => !open)}
          aria-expanded={showFilters}
          aria-controls="search-filters"
        >
          <SlidersHorizontal aria-hidden="true" />
          Filters
          {activeFilters > 0 ? <Badge variant="neutral">{activeFilters}</Badge> : null}
        </Button>
      </div>

      {showFilters ? (
        <div
          id="search-filters"
          className="space-y-3 rounded-card border border-border bg-surface-muted p-3"
        >
          <FilterGroup label="Type">
            <FilterChip active={kind === 'folder'} onClick={() => setParam('kind', kind === 'folder' ? undefined : 'folder')}>
              Folders
            </FilterChip>
            <FilterChip active={kind === 'file'} onClick={() => setParam('kind', kind === 'file' ? undefined : 'file')}>
              Files
            </FilterChip>
          </FilterGroup>

          <FilterGroup label="Kind of file">
            {(['image', 'video', 'audio', 'pdf', 'text'] as const).map((value) => (
              <FilterChip
                key={value}
                active={category === value}
                onClick={() => setParam('type', category === value ? undefined : value)}
              >
                {CATEGORY_LABELS[value]}
              </FilterChip>
            ))}
          </FilterGroup>

          <FilterGroup label="Ownership">
            <FilterChip
              active={ownedByMe}
              onClick={() => setParam('owned_by_me', ownedByMe ? undefined : '1')}
            >
              Only mine
            </FilterChip>
          </FilterGroup>

          {availableTags.data && availableTags.data.tags.length > 0 ? (
            <FilterGroup label="Tags">
              {availableTags.data.tags.map((tag) => (
                <FilterChip
                  key={tag.id}
                  active={tags.includes(tag.name)}
                  onClick={() => toggleTag(tag.name)}
                >
                  {tag.name}
                </FilterChip>
              ))}
            </FilterGroup>
          ) : null}

          {activeFilters > 0 ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setParams(query ? { q: query } : {}, { replace: true })}
            >
              <X aria-hidden="true" />
              Clear filters
            </Button>
          ) : null}
        </div>
      ) : null}

      {!enabled ? (
        <p className="rounded-card border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground">
          Type at least {MIN_QUERY_LENGTH} characters to search.
        </p>
      ) : results.isError ? (
        <ErrorAlert error={results.error} title="The search could not be completed" />
      ) : results.isPending ? (
        <div className="space-y-2" role="status" aria-label="Searching">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : results.data.results.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <SearchIcon className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">No matches</h2>
          <p className="mt-1 max-w-sm text-sm text-muted-foreground">
            Nothing you can see matches “{query}”
            {activeFilters > 0 ? ' with these filters' : ''}.
          </p>
        </div>
      ) : (
        <>
          <p className="text-sm text-muted-foreground" role="status">
            {results.data.total} {results.data.total === 1 ? 'match' : 'matches'} for “{query}”
          </p>
          <NodeResultTable nodes={results.data.results} caption={`Search results for ${query}`} />
        </>
      )}
    </div>
  )
}

const CATEGORY_LABELS: Record<SearchCategory, string> = {
  image: 'Images',
  video: 'Video',
  audio: 'Audio',
  pdf: 'PDFs',
  text: 'Text',
}

function FilterGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <fieldset>
      <legend className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </legend>
      <div className="flex flex-wrap gap-1.5">{children}</div>
    </fieldset>
  )
}

function FilterChip({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'rounded-full border px-2.5 py-1 text-xs transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        active
          ? 'border-primary bg-primary-muted font-medium text-primary'
          : 'border-border bg-surface text-muted-foreground hover:bg-muted',
      )}
    >
      {children}
    </button>
  )
}
