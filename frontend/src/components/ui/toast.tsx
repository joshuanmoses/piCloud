import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { AlertCircle, CheckCircle2, Info, TriangleAlert, X } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * Toast notifications.
 *
 * Written here rather than pulled from a library: the behaviour needed is
 * small, and the part that actually matters — announcing to a screen reader
 * without stealing focus — is a handful of ARIA attributes that a dependency
 * would only wrap.
 */

export type ToastTone = 'info' | 'success' | 'warning' | 'danger'

export interface Toast {
  id: number
  tone: ToastTone
  title: string
  description?: string | undefined
  /** An optional action, typically Undo or Retry. */
  action?: { label: string; onClick: () => void } | undefined
  /** Milliseconds before auto-dismissal. Zero keeps it until dismissed. */
  duration: number
}

interface ToastContextValue {
  show: (toast: Omit<Toast, 'id' | 'duration'> & { duration?: number }) => number
  dismiss: (id: number) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

/** Default lifetimes. Errors stay longer because they usually need reading. */
const DEFAULT_DURATION = 5000
const ERROR_DURATION = 9000

/** Beyond this many, the oldest are dropped rather than stacking forever. */
const MAX_VISIBLE = 4

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const nextId = useRef(1)
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>())

  const dismiss = useCallback((id: number) => {
    const timer = timers.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timers.current.delete(id)
    }
    setToasts((current) => current.filter((t) => t.id !== id))
  }, [])

  const show = useCallback(
    (input: Omit<Toast, 'id' | 'duration'> & { duration?: number }) => {
      const id = nextId.current++
      const duration =
        input.duration ?? (input.tone === 'danger' ? ERROR_DURATION : DEFAULT_DURATION)

      setToasts((current) => [...current, { ...input, id, duration }].slice(-MAX_VISIBLE))

      if (duration > 0) {
        timers.current.set(
          id,
          setTimeout(() => dismiss(id), duration),
        )
      }
      return id
    },
    [dismiss],
  )

  // Clear pending timers on unmount so a dismissed provider cannot update
  // state afterwards.
  useEffect(() => {
    const pending = timers.current
    return () => {
      for (const timer of pending.values()) clearTimeout(timer)
      pending.clear()
    }
  }, [])

  const value = useMemo(() => ({ show, dismiss }), [show, dismiss])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <ToastViewport toasts={toasts} onDismiss={dismiss} />
    </ToastContext.Provider>
  )
}

/** Access the toast API. Throws outside a provider, which is a wiring bug. */
export function useToast(): ToastContextValue {
  const context = useContext(ToastContext)
  if (!context) {
    throw new Error('useToast must be used inside a <ToastProvider>')
  }
  return context
}

const toneStyles: Record<ToastTone, { icon: typeof Info; className: string }> = {
  info: { icon: Info, className: 'border-border' },
  success: { icon: CheckCircle2, className: 'border-success/40' },
  warning: { icon: TriangleAlert, className: 'border-warning/50' },
  danger: { icon: AlertCircle, className: 'border-danger/40' },
}

function ToastViewport({
  toasts,
  onDismiss,
}: {
  toasts: Toast[]
  onDismiss: (id: number) => void
}) {
  return (
    <div
      // Fixed and pointer-events-none so the region never blocks the page;
      // individual toasts re-enable pointer events for their own controls.
      className="pointer-events-none fixed inset-x-0 bottom-0 z-100 flex flex-col items-center gap-2 p-4 sm:items-end"
      // Polite rather than assertive: a toast reports what just happened and
      // should not interrupt what the user is reading.
      aria-live="polite"
      aria-atomic="false"
    >
      {toasts.map((toast) => {
        const { icon: Icon, className } = toneStyles[toast.tone]
        return (
          <div
            key={toast.id}
            role={toast.tone === 'danger' ? 'alert' : 'status'}
            className={cn(
              'pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-card border bg-surface p-3 shadow-lg',
              className,
            )}
          >
            <Icon
              className={cn(
                'mt-0.5 size-4 shrink-0',
                toast.tone === 'success' && 'text-success',
                toast.tone === 'warning' && 'text-warning',
                toast.tone === 'danger' && 'text-danger',
                toast.tone === 'info' && 'text-muted-foreground',
              )}
              aria-hidden="true"
            />

            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium">{toast.title}</p>
              {toast.description ? (
                <p className="mt-0.5 text-xs text-muted-foreground">{toast.description}</p>
              ) : null}
              {toast.action ? (
                <button
                  type="button"
                  onClick={() => {
                    toast.action?.onClick()
                    onDismiss(toast.id)
                  }}
                  className="mt-1.5 text-xs font-medium text-primary underline-offset-2 hover:underline"
                >
                  {toast.action.label}
                </button>
              ) : null}
            </div>

            <button
              type="button"
              onClick={() => onDismiss(toast.id)}
              className="-m-1 rounded p-1 text-muted-foreground hover:bg-muted"
              aria-label="Dismiss notification"
            >
              <X className="size-3.5" aria-hidden="true" />
            </button>
          </div>
        )
      })}
    </div>
  )
}
