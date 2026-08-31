import { useQuery } from '@tanstack/react-query'
import { Download, FileQuestion } from 'lucide-react'
import { useState } from 'react'
import { downloadUrl } from '@/api/nodes'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { can } from '@/features/files/can'
import { formatBytes } from '@/lib/utils'
import type { FileNode } from '@/types/api'
import { previewKindFor } from '@/lib/preview'

/**
 * The file preview.
 *
 * Everything here renders from the ordinary download endpoint, which already
 * sends `X-Content-Type-Options: nosniff` and a `sandbox` Content-Security-
 * Policy on every response — so an uploaded file cannot run script or reach
 * anything, whatever it claims to be. The type used to choose a renderer is the
 * server's own sniffed value, never the uploader's claim.
 *
 * `previewKindFor` admits only the types the server will actually serve inline.
 * Everything else offers a download instead, because a dialog that stays blank
 * while the browser quietly saves the file is worse than saying plainly that
 * there is no preview.
 */
export function PreviewDialog({
  node,
  open,
  onOpenChange,
}: {
  node: FileNode | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle className="truncate pr-4">{node?.name ?? 'Preview'}</DialogTitle>
        </DialogHeader>

        {node ? <PreviewBody node={node} /> : null}

        {node ? (
          <div className="mt-4 flex items-center justify-between gap-3 border-t border-border pt-3">
            <p className="text-xs text-muted-foreground">
              {formatBytes(node.size_bytes)}
              {node.mime_type ? ` · ${node.mime_type}` : ''}
            </p>
            <Button variant="outline" size="sm" asChild disabled={!can(node, 'download')}>
              <a href={downloadUrl(node.id, { forceDownload: true })}>
                <Download aria-hidden="true" />
                Download
              </a>
            </Button>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function PreviewBody({ node }: { node: FileNode }) {
  const [failed, setFailed] = useState(false)
  const kind = previewKindFor(node.mime_type)
  // The inline URL, not the forced-download one: `?download=1` would send
  // Content-Disposition: attachment and the browser would save it instead.
  const src = downloadUrl(node.id)

  if (failed || kind === 'none') {
    return (
      <div className="flex flex-col items-center rounded-card border border-dashed border-border py-14 text-center">
        <FileQuestion className="size-8 text-muted-foreground" aria-hidden="true" />
        <p className="mt-3 text-sm font-medium">
          {failed ? 'This file could not be displayed' : 'No preview for this kind of file'}
        </p>
        <p className="mt-1 max-w-sm text-sm text-muted-foreground">
          Download it to open in an application that understands it.
        </p>
      </div>
    )
  }

  const frame = 'max-h-[65vh] w-full rounded-card border border-border bg-surface-muted'

  switch (kind) {
    case 'image':
      return (
        <img
          src={src}
          alt={node.name}
          onError={() => setFailed(true)}
          className={`${frame} object-contain`}
        />
      )
    case 'video':
      return (
        // No <track>: the file is the user's own, PiFiles has no captions to
        // supply, and an empty track element would announce captions that do
        // not exist.
        <video src={src} controls preload="metadata" onError={() => setFailed(true)} className={frame} />
      )
    case 'audio':
      return (
        <div className="rounded-card border border-border bg-surface-muted p-6">
          <audio src={src} controls preload="metadata" onError={() => setFailed(true)} className="w-full" />
        </div>
      )
    case 'pdf':
      return (
        // The response carries `sandbox` in its own CSP, so the embedded
        // document cannot script, navigate, or reach anything. It also carries
        // `frame-ancestors 'self'`, which is what permits this frame at all.
        <iframe src={src} title={`Preview of ${node.name}`} className={`${frame} h-[65vh]`} />
      )
    case 'text':
      return <TextPreview node={node} onFailure={() => setFailed(true)} />
  }
}

/**
 * Text, CSV, Markdown, and JSON.
 *
 * Fetched and rendered here rather than framed. An iframe of a text/plain
 * response is the browser's own unstyled black-on-white, which ignores the
 * dark theme and cannot be scrolled with the rest of the dialog. Reading it
 * into a `<pre>` costs one request and looks like the rest of the application.
 *
 * The content is put in a text node, never as markup — this is somebody's
 * uploaded file, and rendering it as HTML is precisely the thing not to do.
 */
function TextPreview({ node, onFailure }: { node: FileNode; onFailure: () => void }) {
  const content = useQuery({
    queryKey: ['preview', 'text', node.id],
    queryFn: async ({ signal }) => {
      const response = await fetch(downloadUrl(node.id), {
        credentials: 'same-origin',
        signal,
      })
      if (!response.ok) throw new Error(`The file could not be read (${response.status}).`)

      // Bounded: a 200 MB log file must not be pulled into memory to be
      // glanced at. The tail is what a person wants from a log anyway, but
      // truncating the head is the honest choice — it is what the file starts
      // with, and the notice says so.
      const text = await response.text()
      return text.length > MAX_TEXT_CHARS
        ? { body: text.slice(0, MAX_TEXT_CHARS), truncated: true }
        : { body: text, truncated: false }
    },
  })

  if (content.isError) {
    onFailure()
    return null
  }

  return (
    <div className="max-h-[65vh] overflow-auto rounded-card border border-border bg-surface-muted">
      {content.isPending ? (
        <p className="p-4 text-sm text-muted-foreground" role="status">
          Loading…
        </p>
      ) : (
        <>
          {content.data.truncated ? (
            <p className="border-b border-border bg-warning-muted px-4 py-2 text-xs text-warning-foreground">
              Showing the first {MAX_TEXT_CHARS.toLocaleString()} characters. Download the file to
              read all of it.
            </p>
          ) : null}
          <pre className="overflow-x-auto p-4 text-xs leading-relaxed">{content.data.body}</pre>
        </>
      )}
    </div>
  )
}

/** How much of a text file the dialog will hold in memory. */
const MAX_TEXT_CHARS = 200_000
