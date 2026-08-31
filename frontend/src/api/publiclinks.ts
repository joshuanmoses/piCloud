import { api } from './client'
import type { CreatePublicLinkRequest, PortalView, PublicLink } from '@/types/api'

/**
 * Public link endpoints.
 *
 * Two very different halves. Management is authenticated and behaves like the
 * rest of the API. The portal is what a link recipient reaches: no session, no
 * CSRF token, and nothing about the instance in the response.
 */

// --- Management -------------------------------------------------------------

/** `POST /api/v1/public-links` — the response carries the URL exactly once. */
export function createPublicLink(body: CreatePublicLinkRequest): Promise<{ link: PublicLink }> {
  return api.post<{ link: PublicLink }>('public-links', body)
}

/** `GET /api/v1/public-links` */
export function listMyPublicLinks(signal?: AbortSignal): Promise<{ links: PublicLink[] }> {
  return api.get<{ links: PublicLink[] }>('public-links', signal ? { signal } : {})
}

/** `GET /api/v1/nodes/{id}/public-links` */
export function listNodePublicLinks(
  nodeId: string,
  signal?: AbortSignal,
): Promise<{ links: PublicLink[] }> {
  return api.get<{ links: PublicLink[] }>(
    `nodes/${encodeURIComponent(nodeId)}/public-links`,
    signal ? { signal } : {},
  )
}

/** `DELETE /api/v1/public-links/{id}` */
export function revokePublicLink(id: string): Promise<void> {
  return api.delete<void>(`public-links/${encodeURIComponent(id)}`)
}

// --- The portal -------------------------------------------------------------

/**
 * `GET /api/v1/portal/{token}`
 *
 * Returns `needs_password: true` and nothing else when the link is protected
 * and the visitor has not yet proved they know it.
 */
export function openPortal(token: string, signal?: AbortSignal): Promise<PortalView> {
  return api.get<PortalView>(`portal/${encodeURIComponent(token)}`, signal ? { signal } : {})
}

/** `POST /api/v1/portal/{token}/password` — sets the access cookie. */
export function submitPortalPassword(token: string, password: string): Promise<void> {
  return api.post<void>(`portal/${encodeURIComponent(token)}/password`, { password })
}

/**
 * Builds the download URL for a portal item.
 *
 * A plain navigation, so the browser's own download manager handles it and the
 * access cookie travels automatically.
 */
export function portalDownloadUrl(token: string, itemId?: string): string {
  const base = `/api/v1/portal/${encodeURIComponent(token)}/download`
  return itemId ? `${base}?item=${encodeURIComponent(itemId)}` : base
}

/** `POST /api/v1/portal/{token}/upload` */
export function portalUploadUrl(token: string, name: string): string {
  return `/api/v1/portal/${encodeURIComponent(token)}/upload?name=${encodeURIComponent(name)}`
}

export const linkKeys = {
  all: ['public-links'] as const,
  mine: () => [...linkKeys.all, 'mine'] as const,
  forNode: (nodeId: string) => [...linkKeys.all, 'node', nodeId] as const,
  portal: (token: string) => ['portal', token] as const,
}
