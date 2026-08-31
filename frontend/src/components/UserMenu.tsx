import { useEffect, useRef, useState } from 'react'
import { ChevronDown, LogOut, Settings, ShieldCheck } from 'lucide-react'
import { Link, useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { useAuth, useLogout } from '@/features/auth/useAuth'
import { initials } from '@/lib/format'
import { cn } from '@/lib/utils'

/**
 * Account menu in the header.
 *
 * Built on a native button plus a positioned list rather than a Radix popover:
 * the behaviour needed here is small, and the keyboard and focus handling below
 * is what the dependency would have provided.
 */
export function UserMenu() {
  const { user } = useAuth()
  const logout = useLogout()
  const navigate = useNavigate()

  const [open, setOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)

  // Close on an outside click or on Escape, and return focus to the trigger so
  // keyboard users are not stranded.
  useEffect(() => {
    if (!open) return

    function onPointerDown(event: PointerEvent) {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false)
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
        triggerRef.current?.focus()
      }
    }

    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  if (!user) return null

  function handleSignOut() {
    setOpen(false)
    logout.mutate(undefined, {
      // Navigate regardless: the cookies are cleared either way, so leaving the
      // user on a page they can no longer load would be worse than a redirect.
      onSettled: () => void navigate('/login', { replace: true }),
    })
  }

  return (
    <div className="relative" ref={containerRef}>
      <Button
        ref={triggerRef}
        variant="ghost"
        size="sm"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="menu"
      >
        <span className="flex size-6 items-center justify-center rounded-full bg-primary-muted text-xs font-semibold text-primary">
          {initials(user.display_name || user.username)}
        </span>
        <span className="hidden max-w-32 truncate sm:inline">{user.display_name}</span>
        <ChevronDown className={cn('transition-transform', open && 'rotate-180')} aria-hidden="true" />
      </Button>

      {open ? (
        <div
          role="menu"
          className="absolute right-0 z-50 mt-1 w-56 overflow-hidden rounded-card border border-border bg-surface shadow-lg"
        >
          <div className="border-b border-border px-3 py-2.5">
            <p className="truncate text-sm font-medium">{user.display_name}</p>
            <p className="truncate text-xs text-muted-foreground">
              {user.email || user.username}
            </p>
            {user.is_admin ? (
              <p className="mt-1.5 flex items-center gap-1 text-xs text-primary">
                <ShieldCheck className="size-3" aria-hidden="true" />
                Administrator
              </p>
            ) : null}
          </div>

          <nav className="p-1">
            <Link
              to="/account"
              role="menuitem"
              onClick={() => setOpen(false)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-muted"
            >
              <Settings className="size-4" aria-hidden="true" />
              Account settings
            </Link>
            <button
              type="button"
              role="menuitem"
              onClick={handleSignOut}
              disabled={logout.isPending}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            >
              <LogOut className="size-4" aria-hidden="true" />
              {logout.isPending ? 'Signing out…' : 'Sign out'}
            </button>
          </nav>
        </div>
      ) : null}
    </div>
  )
}
