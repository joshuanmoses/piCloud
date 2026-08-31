/** How the browser should render a file in the preview dialog. */
export type PreviewKind = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'none'

/**
 * Decides how to render a file.
 *
 * The MIME type passed in is the server's own sniffed value, never the
 * uploader's claim — the backend refuses to trust a client-supplied type, and
 * this must not undo that by reading a file extension.
 *
 * The list below deliberately mirrors the backend's inline allowlist
 * (`files.IsInlineSafe`) rather than being broader. The server sends anything
 * outside that list as `Content-Disposition: attachment`, so offering to
 * preview it would open a dialog that stays blank while the browser quietly
 * starts a download — worse than saying there is no preview.
 *
 * SVG and HTML are the two that matter here: both are script-bearing
 * documents. The sandbox Content-Security-Policy on the download response would
 * stop them running, but the safer answer is not to hand them to a renderer at
 * all, and the server already refuses to serve them inline.
 */
export function previewKindFor(mimeType: string | undefined): PreviewKind {
  if (!mimeType) return 'none'
  const base = mimeType.split(';')[0]?.trim().toLowerCase() ?? ''

  switch (base) {
    case 'application/pdf':
      return 'pdf'
    case 'text/plain':
    case 'text/markdown':
    case 'text/csv':
    case 'application/json':
      return 'text'
    // Explicitly not an image, however much the type looks like one.
    case 'image/svg+xml':
      return 'none'
  }

  if (base.startsWith('image/')) return 'image'
  if (base.startsWith('video/')) return 'video'
  if (base.startsWith('audio/')) return 'audio'
  return 'none'
}
