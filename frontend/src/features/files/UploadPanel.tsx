import { CheckCircle2, ChevronDown, RotateCw, X, XCircle } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { formatBytes, cn } from '@/lib/utils'
import type { UploadItem } from './uploadQueue'

interface UploadPanelProps {
  items: UploadItem[]
  onCancel: (id: string) => void
  onRetry: (id: string) => void
  onClearFinished: () => void
  onDismiss: () => void
}

/**
 * A floating panel showing upload progress.
 *
 * It is anchored rather than inline so it survives navigating between folders
 * — uploads keep running, and burying their progress in a listing the user
 * just left would make them look stalled.
 */
export function UploadPanel({
  items,
  onCancel,
  onRetry,
  onClearFinished,
  onDismiss,
}: UploadPanelProps) {
  const [collapsed, setCollapsed] = useState(false)

  if (items.length === 0) return null

  const active = items.filter((i) => i.status === 'uploading' || i.status === 'queued')
  const failed = items.filter((i) => i.status === 'failed')
  const done = items.filter((i) => i.status === 'completed')
  const allFinished = active.length === 0

  const totalBytes = items.reduce((sum, i) => sum + i.size, 0)
  const sentBytes = items.reduce(
    (sum, i) => sum + (i.status === 'completed' ? i.size : i.transferred),
    0,
  )
  const overallPercent = totalBytes > 0 ? (sentBytes / totalBytes) * 100 : 0

  const heading = allFinished
    ? failed.length > 0
      ? `${done.length} uploaded, ${failed.length} failed`
      : `${done.length} upload${done.length === 1 ? '' : 's'} complete`
    : `Uploading ${active.length} file${active.length === 1 ? '' : 's'}`

  return (
    <div className="fixed bottom-4 right-4 z-40 w-[calc(100vw-2rem)] max-w-sm overflow-hidden rounded-card border border-border bg-surface shadow-xl">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2">
        <p className="min-w-0 flex-1 truncate text-sm font-medium" aria-live="polite">
          {heading}
        </p>

        {allFinished ? (
          <Button variant="ghost" size="icon-sm" onClick={onClearFinished} aria-label="Clear completed uploads">
            <X aria-hidden="true" />
          </Button>
        ) : null}

        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setCollapsed((v) => !v)}
          aria-label={collapsed ? 'Show upload details' : 'Hide upload details'}
          aria-expanded={!collapsed}
        >
          <ChevronDown className={cn('transition-transform', collapsed && 'rotate-180')} aria-hidden="true" />
        </Button>

        {allFinished ? (
          <Button variant="ghost" size="icon-sm" onClick={onDismiss} aria-label="Close upload panel">
            <X aria-hidden="true" />
          </Button>
        ) : null}
      </div>

      {!allFinished ? (
        <div className="px-3 pt-2">
          <Progress
            value={overallPercent}
            label={`Overall upload progress: ${Math.round(overallPercent)} percent`}
          />
          <p className="mt-1 text-[11px] text-muted-foreground">
            {formatBytes(sentBytes)} of {formatBytes(totalBytes)}
          </p>
        </div>
      ) : null}

      {!collapsed ? (
        <ul className="max-h-64 divide-y divide-border overflow-y-auto p-1">
          {items.map((item) => (
            <UploadRow key={item.id} item={item} onCancel={onCancel} onRetry={onRetry} />
          ))}
        </ul>
      ) : null}
    </div>
  )
}

function UploadRow({
  item,
  onCancel,
  onRetry,
}: {
  item: UploadItem
  onCancel: (id: string) => void
  onRetry: (id: string) => void
}) {
  const percent = item.size > 0 ? (item.transferred / item.size) * 100 : 0

  return (
    <li className="flex items-center gap-2.5 px-2 py-2">
      <div className="min-w-0 flex-1">
        <p className="truncate text-xs font-medium" title={item.file.name}>
          {item.file.name}
        </p>

        {item.status === 'uploading' ? (
          <>
            <Progress
              value={percent}
              className="mt-1.5"
              label={`${item.file.name}: ${Math.round(percent)} percent`}
            />
            <p className="mt-1 text-[11px] text-muted-foreground">
              {formatBytes(item.transferred)} of {formatBytes(item.size)}
            </p>
          </>
        ) : (
          <p
            className={cn(
              'mt-0.5 text-[11px]',
              item.status === 'failed' ? 'text-danger' : 'text-muted-foreground',
            )}
          >
            {statusLabel(item)}
          </p>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-1">
        {item.status === 'completed' ? (
          <CheckCircle2 className="size-4 text-success" aria-label="Uploaded" />
        ) : null}

        {item.status === 'failed' ? (
          <>
            <XCircle className="size-4 text-danger" aria-hidden="true" />
            {item.retryable ? (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => onRetry(item.id)}
                aria-label={`Retry uploading ${item.file.name}`}
              >
                <RotateCw aria-hidden="true" />
              </Button>
            ) : null}
          </>
        ) : null}

        {item.status === 'uploading' || item.status === 'queued' ? (
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => onCancel(item.id)}
            aria-label={`Cancel uploading ${item.file.name}`}
          >
            <X aria-hidden="true" />
          </Button>
        ) : null}
      </div>
    </li>
  )
}

function statusLabel(item: UploadItem): string {
  switch (item.status) {
    case 'queued':
      return 'Waiting…'
    case 'completed':
      return `Uploaded · ${formatBytes(item.size)}`
    case 'cancelled':
      return 'Cancelled'
    case 'failed':
      return item.error ?? 'Upload failed'
    default:
      return ''
  }
}
