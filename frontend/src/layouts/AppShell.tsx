import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { HardDrive, Menu as MenuIcon, X } from 'lucide-react'
import { Link, useLocation } from 'react-router-dom'
import { ThemeToggle } from '@/components/ThemeToggle'
import { UserMenu } from '@/components/UserMenu'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/features/auth/useAuth'
import { HeaderSearch } from '@/features/productivity/HeaderSearch'
import { Sidebar } from './Sidebar'

interface AppShellProps {
  children: ReactNode
}

/**
 * The application frame.
 *
 * The sidebar is a permanent column on a wide screen and a slide-over drawer on
 * a narrow one. The drawer is rendered as a separate element rather than the
 * same one repositioned, so its focus and Escape behaviour do not have to be
 * conditional.
 */
export function AppShell({ children }: AppShellProps) {
  const { isAuthenticated } = useAuth()
  const location = useLocation()

  // The drawer records which page it was opened on. Deriving "is it open"
  // from that comparison closes it on any navigation — including the browser
  // back button — without an effect that sets state during render.
  const [openedOn, setOpenedOn] = useState<string | null>(null)
  const drawerOpen = openedOn === location.pathname

  const setDrawerOpen = useCallback(
    (open: boolean) => setOpenedOn(open ? location.pathname : null),
    [location.pathname],
  )

  // Escape closes it, matching every other overlay in the app.
  useEffect(() => {
    if (!drawerOpen) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setDrawerOpen(false)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [drawerOpen, setDrawerOpen])

  return (
    <div className="flex min-h-full flex-col bg-background">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-primary focus:px-4 focus:py-2 focus:text-primary-foreground"
      >
        Skip to content
      </a>

      <header className="sticky top-0 z-40 border-b border-border bg-surface/85 backdrop-blur-sm">
        <div className="flex h-14 w-full items-center gap-2 px-3 sm:px-4">
          {isAuthenticated ? (
            <Button
              variant="ghost"
              size="icon"
              className="lg:hidden"
              onClick={() => setDrawerOpen(true)}
              aria-label="Open navigation"
              aria-expanded={drawerOpen}
            >
              <MenuIcon aria-hidden="true" />
            </Button>
          ) : null}

          <Link to="/" className="flex shrink-0 items-center gap-2 rounded-md">
            <span className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
              <HardDrive className="size-4" aria-hidden="true" />
            </span>
            <span className="text-sm font-semibold tracking-tight">PiFiles</span>
          </Link>

          {isAuthenticated ? <HeaderSearch /> : null}

          <div className="ml-auto flex shrink-0 items-center gap-1">
            <ThemeToggle />
            <UserMenu />
          </div>
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        {isAuthenticated ? (
          <aside className="hidden w-60 shrink-0 border-r border-border lg:block">
            <div className="sticky top-14 flex h-[calc(100vh-3.5rem)] flex-col">
              <Sidebar />
            </div>
          </aside>
        ) : null}

        {drawerOpen && isAuthenticated ? (
          <>
            <div
              className="fixed inset-0 z-40 bg-black/50 lg:hidden"
              onClick={() => setDrawerOpen(false)}
              aria-hidden="true"
            />
            <aside
              className="fixed inset-y-0 left-0 z-50 w-64 border-r border-border bg-surface lg:hidden"
              aria-label="Navigation"
            >
              <div className="flex h-14 items-center justify-between border-b border-border px-3">
                <span className="text-sm font-semibold">Menu</span>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setDrawerOpen(false)}
                  aria-label="Close navigation"
                  autoFocus
                >
                  <X aria-hidden="true" />
                </Button>
              </div>
              <div className="h-[calc(100%-3.5rem)]">
                <Sidebar onNavigate={() => setDrawerOpen(false)} />
              </div>
            </aside>
          </>
        ) : null}

        <main id="main" className="min-w-0 flex-1 px-4 py-6 sm:px-6">
          <div className="mx-auto w-full max-w-6xl">{children}</div>
        </main>
      </div>

      <footer className="border-t border-border">
        <div className="px-4 py-3 text-xs text-muted-foreground sm:px-6">
          PiFiles — a private file server for your own hardware.
        </div>
      </footer>
    </div>
  )
}
