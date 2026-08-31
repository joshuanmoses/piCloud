import { CheckCircle2, CircleAlert, CircleX } from 'lucide-react'
import type { HealthStatus } from '@/types/api'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

const presentation: Record<
  HealthStatus,
  { label: string; variant: 'success' | 'warning' | 'danger'; Icon: typeof CheckCircle2 }
> = {
  healthy: { label: 'Healthy', variant: 'success', Icon: CheckCircle2 },
  degraded: { label: 'Degraded', variant: 'warning', Icon: CircleAlert },
  unhealthy: { label: 'Unhealthy', variant: 'danger', Icon: CircleX },
}

export function StatusBadge({ status }: { status: HealthStatus }) {
  const { label, variant, Icon } = presentation[status] ?? presentation.unhealthy
  return (
    <Badge variant={variant}>
      <Icon className="size-3" aria-hidden="true" />
      {label}
    </Badge>
  )
}

interface SubsystemRowProps {
  name: string
  status: HealthStatus
  detail?: string | undefined
  latencyMs?: number | undefined
}

/** One subsystem's condition, as shown on the status page. */
export function SubsystemRow({ name, status, detail, latencyMs }: SubsystemRowProps) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
      <div className="min-w-0">
        <p className="text-sm font-medium capitalize">{name}</p>
        {detail ? <p className="mt-0.5 text-xs text-muted-foreground">{detail}</p> : null}
      </div>
      <div className="flex shrink-0 items-center gap-3">
        {typeof latencyMs === 'number' ? (
          <span
            className={cn(
              'font-mono text-xs',
              latencyMs > 500 ? 'text-warning-foreground' : 'text-muted-foreground',
            )}
          >
            {latencyMs} ms
          </span>
        ) : null}
        <StatusBadge status={status} />
      </div>
    </div>
  )
}
