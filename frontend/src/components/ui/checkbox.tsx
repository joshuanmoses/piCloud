import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * A native checkbox, styled. Radix's checkbox is not used here: a native input
 * already has correct keyboard and screen-reader behaviour, and avoiding the
 * dependency keeps the bundle smaller.
 */
export function Checkbox({ className, ...props }: ComponentProps<'input'>) {
  return (
    <input
      type="checkbox"
      className={cn(
        'size-4 shrink-0 rounded border-border accent-primary',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}
