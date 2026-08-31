import { AlertCircle, CheckCircle2, Info, TriangleAlert } from 'lucide-react'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from '@/lib/utils'

export type AlertTone = 'info' | 'success' | 'warning' | 'danger'

const toneStyles: Record<AlertTone, { container: string; Icon: typeof Info }> = {
  info: { container: 'border-border bg-muted text-foreground', Icon: Info },
  success: { container: 'border-success/30 bg-success-muted text-foreground', Icon: CheckCircle2 },
  warning: { container: 'border-warning/40 bg-warning-muted text-foreground', Icon: TriangleAlert },
  danger: { container: 'border-danger/30 bg-danger-muted text-foreground', Icon: AlertCircle },
}

export interface AlertProps extends Omit<ComponentProps<'div'>, 'title'> {
  tone?: AlertTone
  title?: ReactNode
  /** Rendered at the end of the alert, typically a retry button. */
  action?: ReactNode
}

export function Alert({
  tone = 'info',
  title,
  action,
  className,
  children,
  ...props
}: AlertProps) {
  const { container, Icon } = toneStyles[tone]
  return (
    <div
      // Errors are announced immediately; softer tones wait for a pause so they
      // do not interrupt what the user is reading.
      role={tone === 'danger' ? 'alert' : 'status'}
      aria-live={tone === 'danger' ? 'assertive' : 'polite'}
      className={cn('flex items-start gap-3 rounded-card border p-4 text-sm', container, className)}
      {...props}
    >
      <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        {title ? <p className="font-medium">{title}</p> : null}
        {children ? <div className={cn(title && 'mt-1', 'text-muted-foreground')}>{children}</div> : null}
      </div>
      {action ? <div className="shrink-0">{action}</div> : null}
    </div>
  )
}
