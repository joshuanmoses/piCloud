import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Placeholder shown while content loads. It is marked aria-hidden because a
 * screen reader should hear the surrounding live region announce "loading",
 * not a series of meaningless boxes.
 */
export function Skeleton({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      aria-hidden="true"
      className={cn('animate-pulse rounded-md bg-muted', className)}
      {...props}
    />
  )
}
