import { Search } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { cn } from '@/lib/utils'

/**
 * The search box in the application header.
 *
 * Submitting navigates to /search rather than filtering in place, so a search
 * is a URL: bookmarkable, shareable with yourself, and reachable with the back
 * button. On the search page itself this renders nothing, because that page
 * carries its own box with the filters attached to it.
 *
 * "/" focuses it, the convention everywhere else that has a search box. The
 * shortcut is ignored while the user is typing in another field, which is the
 * bug every implementation of this makes once.
 */
export function HeaderSearch() {
  const navigate = useNavigate()
  const location = useLocation()
  const inputRef = useRef<HTMLInputElement>(null)

  // The search page has its own box, with the filters attached to it. Showing
  // a second one in the header would put two identical inputs on screen at
  // once, and leave the user to guess which one the filters belong to.
  const onSearchPage = location.pathname === '/search'
  const [value, setValue] = useState('')

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== '/' || event.metaKey || event.ctrlKey || event.altKey) return

      const target = event.target
      const typing =
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        (target instanceof HTMLElement && target.isContentEditable)
      if (typing) return

      event.preventDefault()
      inputRef.current?.focus()
      inputRef.current?.select()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  if (onSearchPage) return null

  return (
    <form
      role="search"
      onSubmit={(event) => {
        event.preventDefault()
        const trimmed = value.trim()
        if (trimmed === '') return
        void navigate(`/search?q=${encodeURIComponent(trimmed)}`)
      }}
      className="hidden min-w-0 flex-1 justify-center px-2 sm:flex"
    >
      <div className="relative w-full max-w-md">
        <Search
          className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <input
          ref={inputRef}
          type="search"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder="Search files…"
          aria-label="Search files and folders"
          className={cn(
            'h-9 w-full rounded-md border border-border bg-surface pl-8 pr-3 text-sm',
            'placeholder:text-muted-foreground',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
          )}
        />
      </div>
    </form>
  )
}
