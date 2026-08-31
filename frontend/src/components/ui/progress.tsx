import { cn } from '@/lib/utils'

interface ProgressProps {
  /** 0–100. Values outside the range are clamped. */
  value: number
  className?: string
  /** Accessible description of what is progressing. */
  label: string
  tone?: 'primary' | 'success' | 'danger'
}

export function Progress({ value, className, label, tone = 'primary' }: ProgressProps) {
  const clamped = Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0))

  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(clamped)}
      aria-label={label}
      className={cn('h-1.5 w-full overflow-hidden rounded-full bg-muted', className)}
    >
      <div
        className={cn(
          'h-full rounded-full transition-[width] duration-200',
          tone === 'primary' && 'bg-primary',
          tone === 'success' && 'bg-success',
          tone === 'danger' && 'bg-danger',
        )}
        style={{ width: `${clamped}%` }}
      />
    </div>
  )
}
