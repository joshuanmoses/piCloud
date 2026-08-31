import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'

export function NotFoundPage() {
  return (
    <div className="flex flex-col items-center justify-center py-24 text-center">
      <p className="font-mono text-sm text-muted-foreground">404</p>
      <h1 className="mt-2 text-xl font-semibold tracking-tight">Page not found</h1>
      <p className="mt-2 max-w-sm text-sm text-muted-foreground">
        That page does not exist. It may have been moved, or the link may be incorrect.
      </p>
      <Button asChild className="mt-6">
        <Link to="/">Go to system status</Link>
      </Button>
    </div>
  )
}
