import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { Alert } from '@/components/ui/alert'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from './useAuth'

/**
 * Route guard.
 *
 * This is a usability affordance, not a security control: it decides what to
 * render, and nothing more. Every endpoint enforces its own authentication and
 * authorization server-side, so bypassing this component in a browser gains
 * access to nothing.
 */
export function RequireAuth({
  children,
  requireAdmin = false,
}: {
  children: ReactNode
  /** Also require administrator access. Same caveat: an affordance, not a gate. */
  requireAdmin?: boolean
}) {
  const { user, isAuthenticated, isLoading, isOffline } = useAuth()
  const location = useLocation()

  if (isLoading) {
    return (
      <div className="space-y-4" role="status" aria-label="Checking your session">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }

  // Being unable to reach the server is a different problem from being signed
  // out, and bouncing to the sign-in page would misdiagnose it.
  if (isOffline) {
    return (
      <Alert tone="danger" title="Cannot reach the PiFiles server">
        Check that the server is running and that you are connected to the network, then reload
        this page.
      </Alert>
    )
  }

  if (!isAuthenticated) {
    // Remember where they were headed so sign-in can return them there.
    return <Navigate to="/login" state={{ from: location.pathname + location.search }} replace />
  }

  // A signed-in non-administrator gets an explanation rather than a redirect:
  // they are in the right place and simply may not be here, and bouncing them
  // to the file browser would look like the link was broken.
  if (requireAdmin && !user?.is_admin) {
    return (
      <Alert tone="warning" title="This page is for administrators">
        Your account does not have administrator access. Ask an administrator if you need it.
      </Alert>
    )
  }

  return <>{children}</>
}
