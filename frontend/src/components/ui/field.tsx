import { useId, type ReactNode } from 'react'
import { Label } from './label'
import { cn } from '@/lib/utils'

interface FieldProps {
  label: string
  /** Rendered beneath the control; announced with the input. */
  hint?: ReactNode
  error?: string | undefined
  required?: boolean
  className?: string
  /** Receives the ids and validity state to spread onto the control. */
  children: (props: {
    id: string
    'aria-describedby': string | undefined
    'aria-invalid': boolean | undefined
    required: boolean | undefined
  }) => ReactNode
}

/**
 * A labelled form field with accessible error and hint wiring.
 *
 * The render-prop shape exists so the ids and ARIA attributes are generated
 * once and applied to the control automatically — a label that is not actually
 * associated with its input is the single most common accessibility defect in
 * hand-written forms.
 */
export function Field({ label, hint, error, required, className, children }: FieldProps) {
  const id = useId()
  const hintId = `${id}-hint`
  const errorId = `${id}-error`

  const describedBy = [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(' ')

  return (
    <div className={cn('space-y-1.5', className)}>
      <Label htmlFor={id}>
        {label}
        {required ? (
          <span className="ml-0.5 text-danger" aria-hidden="true">
            *
          </span>
        ) : null}
      </Label>

      {children({
        id,
        'aria-describedby': describedBy || undefined,
        'aria-invalid': error ? true : undefined,
        required: required || undefined,
      })}

      {hint ? (
        <p id={hintId} className="text-xs text-muted-foreground">
          {hint}
        </p>
      ) : null}

      {error ? (
        <p id={errorId} className="text-xs font-medium text-danger" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  )
}
