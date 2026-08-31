/**
 * Presentation helpers shared across components.
 *
 * These live here rather than beside the components that use them so each
 * component file exports only components — which keeps fast refresh working
 * and makes the helpers straightforward to test on their own.
 */

/** Derives up to two initials from a name, for an avatar bubble. */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return (parts[0] ?? '').slice(0, 2).toUpperCase()
  return ((parts[0]?.[0] ?? '') + (parts[parts.length - 1]?.[0] ?? '')).toUpperCase()
}

/**
 * Turns a user-agent string into something a person recognises.
 *
 * The value is client-supplied and untrusted, so it is never rendered raw: the
 * result is always one of a fixed set of labels, never an echo of the input.
 */
export function describeUserAgent(userAgent: string | undefined): string {
  // The server now supplies a `device` label on every session and audit event.
  // Prefer that; this exists as a fallback for a response that predates it, and
  // so a session with no label at all still reads as something.
  if (!userAgent) return 'Unknown device'
  const ua = userAgent.toLowerCase()

  const browser = ua.includes('firefox')
    ? 'Firefox'
    : ua.includes('edg/')
      ? 'Edge'
      : ua.includes('chrome') && !ua.includes('chromium')
        ? 'Chrome'
        : ua.includes('safari')
          ? 'Safari'
          : ua.includes('curl')
            ? 'curl'
            : 'Unknown browser'

  const platform =
    ua.includes('iphone') || ua.includes('ipad')
      ? 'iOS'
      : ua.includes('android')
        ? 'Android'
        : ua.includes('mac os') || ua.includes('macintosh')
          ? 'macOS'
          : ua.includes('windows')
            ? 'Windows'
            : ua.includes('linux')
              ? 'Linux'
              : ''

  return platform ? `${browser} on ${platform}` : browser
}

/** Formats a timestamp as a short relative description. */
export function formatRelative(iso: string, now: number = Date.now()): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return 'unknown'

  const seconds = Math.round((now - then) / 1000)
  // A clock skew between server and browser can produce a future timestamp;
  // "just now" reads better than "in -3 minutes".
  if (seconds < 60) return 'just now'
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60)
    return minutes === 1 ? '1 minute ago' : `${minutes} minutes ago`
  }
  if (seconds < 86_400) {
    const hours = Math.floor(seconds / 3600)
    return hours === 1 ? '1 hour ago' : `${hours} hours ago`
  }
  const days = Math.floor(seconds / 86_400)
  return days === 1 ? 'yesterday' : `${days} days ago`
}

/** A plain-language description of what a public link allows. */
export function describeLinkKind(kind: 'view' | 'download' | 'upload' | 'upload_view'): string {
  switch (kind) {
    case 'view':
      return 'View only'
    case 'download':
      return 'View and download'
    case 'upload':
      return 'Send files'
    case 'upload_view':
      return 'Send files and browse'
  }
}
