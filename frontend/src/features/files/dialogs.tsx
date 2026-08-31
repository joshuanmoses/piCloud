import { useState, type FormEvent } from 'react'
import { ApiError } from '@/api/client'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { TagPicker } from '@/features/productivity/TagPicker'
import { VersionHistory } from '@/features/versions/VersionHistory'
import type { FileNode } from '@/types/api'

interface NameDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: string
  label: string
  initialValue?: string
  submitLabel: string
  pending: boolean
  error: unknown
  onSubmit: (name: string) => void
  /** Selects the base name rather than the extension when opening. */
  selectBaseName?: boolean
}

/**
 * A dialog that asks for a single name. Used for both New Folder and Rename.
 *
 * On rename it preselects the base name and leaves the extension out of the
 * selection, so typing replaces "Report" and not "Report.pdf" — the behaviour
 * every desktop file manager has.
 */
export function NameDialog({
  open,
  onOpenChange,
  initialValue = '',
  ...rest
}: NameDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/*
        The form is keyed on the value it starts from, so opening the dialog for
        a different item remounts it with fresh state. That replaces the usual
        "sync a prop into state with an effect" pattern, which causes a second
        render and can briefly show the previous item's name.
      */}
      {open ? <NameDialogForm key={initialValue} initialValue={initialValue} {...rest} /> : null}
    </Dialog>
  )
}

type NameDialogFormProps = Omit<NameDialogProps, 'open' | 'onOpenChange' | 'initialValue'> & {
  initialValue: string
}

function NameDialogForm({
  title,
  description,
  label,
  initialValue,
  submitLabel,
  pending,
  error,
  onSubmit,
  selectBaseName,
}: NameDialogFormProps) {
  const [value, setValue] = useState(initialValue)

  const fieldError =
    error instanceof ApiError ? (error.fields?.name ?? error.message) : undefined

  function handleSubmit(event: FormEvent) {
    event.preventDefault()
    const trimmed = value.trim()
    if (trimmed) onSubmit(trimmed)
  }

  return (
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-4">
          <Field label={label} error={fieldError} required>
            {(props) => (
              <Input
                {...props}
                value={value}
                onChange={(event) => setValue(event.target.value)}
                autoFocus
                disabled={pending}
                autoComplete="off"
                spellCheck={false}
                onFocus={(event) => {
                  if (!selectBaseName) {
                    event.target.select()
                    return
                  }
                  const dot = value.lastIndexOf('.')
                  event.target.setSelectionRange(0, dot > 0 ? dot : value.length)
                }}
              />
            )}
          </Field>

          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline" type="button" disabled={pending}>
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={pending || !value.trim()}>
              {pending ? 'Working…' : submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
  )
}

interface DetailsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  node: FileNode | null
  subtreeSize?: number | undefined
  subtreeFiles?: number | undefined
}

/** Shows a node's details. Never displays a filesystem path or content hash. */
export function DetailsDialog({
  open,
  onOpenChange,
  node,
  subtreeSize,
  subtreeFiles,
}: DetailsDialogProps) {
  if (!node) return null

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="break-all">{node.name}</DialogTitle>
        </DialogHeader>

        <dl className="space-y-3 text-sm">
          <DetailRow label="Type" value={describeNodeType(node)} />
          <DetailRow
            label="Size"
            value={
              node.kind === 'folder'
                ? subtreeSize !== undefined
                  ? `${formatSize(subtreeSize)}${subtreeFiles !== undefined ? ` in ${subtreeFiles} file${subtreeFiles === 1 ? '' : 's'}` : ''}`
                  : 'Calculating…'
                : formatSize(node.size_bytes)
            }
          />
          <DetailRow label="Owner" value={node.owner?.display_name ?? '—'} />
          <DetailRow label="Modified" value={formatTimestamp(node.updated_at)} />
          <DetailRow label="Created" value={formatTimestamp(node.created_at)} />
        </dl>

        <div className="mt-4 border-t border-border pt-4">
          <TagPicker node={node} />
        </div>

        {node.kind === 'file' ? (
          <div className="mt-4 border-t border-border pt-4">
            <VersionHistory node={node} />
          </div>
        ) : null}

        <DialogFooter>
          <DialogClose asChild>
            <Button variant="outline">Close</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border pb-2 last:border-b-0">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-right font-medium">{value}</dd>
    </div>
  )
}

function describeNodeType(node: FileNode): string {
  if (node.kind === 'folder') return 'Folder'
  return node.mime_type?.split(';')[0] ?? 'File'
}

function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** exponent
  const digits = exponent === 0 || Number.isInteger(value) ? 0 : 1
  return `${value.toFixed(digits)} ${units[exponent]}`
}

function formatTimestamp(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  return date.toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  })
}

interface ErrorAlertProps {
  error: unknown
  title: string
  className?: string
}

/** Renders an API error, using the server's message where it has one. */
export function ErrorAlert({ error, title, className }: ErrorAlertProps) {
  if (!error) return null
  const message =
    error instanceof ApiError ? error.message : 'Something went wrong. Please try again.'
  const requestId = error instanceof ApiError ? error.requestId : undefined

  return (
    <Alert tone="danger" title={title} className={className}>
      <p>{message}</p>
      {requestId ? <p className="mt-1 font-mono text-xs">Request ID: {requestId}</p> : null}
    </Alert>
  )
}
