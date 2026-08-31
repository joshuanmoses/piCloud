import { api, request } from './client'
import type {
  FavoritesResponse,
  PreviewStatusResponse,
  RecentResponse,
  SearchParams,
  SearchResponse,
  Tag,
} from '@/types/api'

/**
 * Search, favourites, recent activity, tags, previews, and archive downloads.
 *
 * Everything here is a personal view of content the server has already decided
 * the caller may see. The interface uses these to decide what to show; the
 * server re-checks on every request, so nothing here is a control.
 */

// --- Search -----------------------------------------------------------------

/** `GET /api/v1/search` */
export function search(params: SearchParams, signal?: AbortSignal): Promise<SearchResponse> {
  return request<SearchResponse>('search', {
    method: 'GET',
    params: {
      q: params.q,
      kind: params.kind,
      type: params.type,
      in_folder: params.inFolder,
      // The server reads presence, not truthiness, so a false must be omitted
      // rather than sent as "false".
      owned_by_me: params.ownedByMe ? '1' : undefined,
      modified_after: params.modifiedAfter,
      modified_before: params.modifiedBefore,
      min_size: params.minSize,
      max_size: params.maxSize,
      tag: params.tags,
      limit: params.limit,
      offset: params.offset,
    },
    ...(signal ? { signal } : {}),
  })
}

/** The shortest query the server will act on. Below this it returns nothing. */
export const MIN_QUERY_LENGTH = 2

// --- Favourites --------------------------------------------------------------

/** `GET /api/v1/favorites` */
export function listFavorites(
  params: { limit?: number; offset?: number } = {},
  signal?: AbortSignal,
): Promise<FavoritesResponse> {
  return request<FavoritesResponse>('favorites', {
    method: 'GET',
    params: { limit: params.limit, offset: params.offset },
    ...(signal ? { signal } : {}),
  })
}

/** `PUT /api/v1/nodes/{id}/favorite` */
export function addFavorite(id: string): Promise<{ favorite: boolean }> {
  return api.put<{ favorite: boolean }>(`nodes/${encodeURIComponent(id)}/favorite`)
}

/** `DELETE /api/v1/nodes/{id}/favorite` */
export function removeFavorite(id: string): Promise<{ favorite: boolean }> {
  return api.delete<{ favorite: boolean }>(`nodes/${encodeURIComponent(id)}/favorite`)
}

// --- Recent ------------------------------------------------------------------

/** `GET /api/v1/recent` */
export function listRecent(
  params: { limit?: number; offset?: number } = {},
  signal?: AbortSignal,
): Promise<RecentResponse> {
  return request<RecentResponse>('recent', {
    method: 'GET',
    params: { limit: params.limit, offset: params.offset },
    ...(signal ? { signal } : {}),
  })
}

// --- Tags --------------------------------------------------------------------

/** `GET /api/v1/tags` */
export function listTags(signal?: AbortSignal): Promise<{ tags: Tag[] }> {
  return api.get<{ tags: Tag[] }>('tags', signal ? { signal } : {})
}

/** `POST /api/v1/tags` */
export function createTag(name: string, color = ''): Promise<Tag> {
  return api.post<Tag>('tags', { name, color })
}

/** `PATCH /api/v1/tags/{id}` */
export function updateTag(id: string, name: string, color = ''): Promise<Tag> {
  return api.patch<Tag>(`tags/${encodeURIComponent(id)}`, { name, color })
}

/** `DELETE /api/v1/tags/{id}` */
export function deleteTag(id: string): Promise<void> {
  return api.delete<void>(`tags/${encodeURIComponent(id)}`)
}

/** `GET /api/v1/nodes/{id}/tags` — the caller's own tags on one node. */
export function listNodeTags(nodeId: string, signal?: AbortSignal): Promise<{ tags: Tag[] }> {
  return api.get<{ tags: Tag[] }>(
    `nodes/${encodeURIComponent(nodeId)}/tags`,
    signal ? { signal } : {},
  )
}

/** `PUT /api/v1/nodes/{id}/tags/{tagId}` */
export function applyTag(nodeId: string, tagId: string): Promise<void> {
  return api.put<void>(
    `nodes/${encodeURIComponent(nodeId)}/tags/${encodeURIComponent(tagId)}`,
  )
}

/** `DELETE /api/v1/nodes/{id}/tags/{tagId}` */
export function removeTag(nodeId: string, tagId: string): Promise<void> {
  return api.delete<void>(
    `nodes/${encodeURIComponent(nodeId)}/tags/${encodeURIComponent(tagId)}`,
  )
}

// --- Previews ----------------------------------------------------------------

/** `GET /api/v1/nodes/{id}/preview/status` */
export function previewStatus(
  nodeId: string,
  signal?: AbortSignal,
): Promise<PreviewStatusResponse> {
  return api.get<PreviewStatusResponse>(
    `nodes/${encodeURIComponent(nodeId)}/preview/status`,
    signal ? { signal } : {},
  )
}

/**
 * The URL of a node's thumbnail.
 *
 * Used as an `<img src>` rather than fetched: the browser caches it, revalidates
 * it with the ETag, and never holds it in JavaScript memory. A missing preview
 * is a 404, which the `onError` handler turns into a generic icon.
 */
export function previewUrl(nodeId: string): string {
  return `/api/v1/nodes/${encodeURIComponent(nodeId)}/preview`
}

// --- Archives ----------------------------------------------------------------

/**
 * The URL that downloads a folder as a ZIP.
 *
 * A plain navigation, like every other download: the browser's own download
 * manager handles it, and the archive is streamed rather than held in memory on
 * either end. There is no Content-Length — the total is unknowable until the
 * last entry is written — so the progress bar is indeterminate.
 */
export function folderArchiveUrl(folderId: string): string {
  return `/api/v1/nodes/${encodeURIComponent(folderId)}/archive`
}

/** The URL that downloads an explicit selection as one ZIP. */
export function selectionArchiveUrl(nodeIds: readonly string[]): string {
  const params = new URLSearchParams()
  for (const id of nodeIds) params.append('id', id)
  return `/api/v1/archive?${params.toString()}`
}

/** React Query keys, kept together so invalidation stays consistent. */
export const productivityKeys = {
  search: (params: SearchParams) => ['search', params] as const,
  favorites: (params: { limit?: number; offset?: number } = {}) =>
    ['favorites', params] as const,
  recent: (params: { limit?: number; offset?: number } = {}) => ['recent', params] as const,
  tags: () => ['tags'] as const,
  nodeTags: (nodeId: string) => ['tags', 'node', nodeId] as const,
  previewStatus: (nodeId: string) => ['preview', nodeId] as const,
}
