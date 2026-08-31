import { api, request } from './client'
import type {
  FileNode,
  ListNodesParams,
  NodeDetailResponse,
  NodeListResponse,
  UploadResult,
  UploadSession,
} from '@/types/api'

/**
 * File and folder endpoints.
 *
 * Files and folders are one collection server-side; this module mirrors that
 * rather than pretending they are separate resources.
 */

/** The literal the API accepts in place of the caller's root folder id. */
export const ROOT = 'root'

/** `GET /api/v1/nodes` — lists a folder's children. */
export function listNodes(
  params: ListNodesParams = {},
  signal?: AbortSignal,
): Promise<NodeListResponse> {
  return request<NodeListResponse>('nodes', {
    method: 'GET',
    params: {
      parent_id: params.parentId,
      limit: params.limit,
      offset: params.offset,
      sort: params.sort,
      order: params.order,
      kind: params.kind,
    },
    ...(signal ? { signal } : {}),
  })
}

/** `GET /api/v1/nodes/{id}` */
export function getNode(id: string, signal?: AbortSignal): Promise<NodeDetailResponse> {
  return api.get<NodeDetailResponse>(`nodes/${encodeURIComponent(id)}`, signal ? { signal } : {})
}

/** `POST /api/v1/nodes/folders` */
export function createFolder(parentId: string, name: string): Promise<{ node: FileNode }> {
  return api.post<{ node: FileNode }>('nodes/folders', { parent_id: parentId, name })
}

/** `PATCH /api/v1/nodes/{id}` — renames, moves, or both in one request. */
export function updateNode(
  id: string,
  changes: { name?: string; parent_id?: string },
): Promise<{ node: FileNode }> {
  return api.patch<{ node: FileNode }>(`nodes/${encodeURIComponent(id)}`, changes)
}

/** `POST /api/v1/nodes/{id}/copy` */
export function copyNode(
  id: string,
  parentId: string,
  name?: string,
): Promise<{ node: FileNode; nodes_copied: number }> {
  return api.post<{ node: FileNode; nodes_copied: number }>(
    `nodes/${encodeURIComponent(id)}/copy`,
    name ? { parent_id: parentId, name } : { parent_id: parentId },
  )
}

/** `DELETE /api/v1/nodes/{id}` — moves to Trash, or purges when permanent. */
export function deleteNode(id: string, permanent = false): Promise<void> {
  return request<void>(`nodes/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    ...(permanent ? { params: { permanent: 'true' } } : {}),
  })
}

/** `POST /api/v1/nodes/{id}/restore` */
export function restoreNode(id: string): Promise<{ node: FileNode }> {
  return api.post<{ node: FileNode }>(`nodes/${encodeURIComponent(id)}/restore`)
}

/** `GET /api/v1/trash` */
export function listTrash(
  params: { limit?: number; offset?: number } = {},
  signal?: AbortSignal,
): Promise<{ nodes: FileNode[]; total: number; has_more: boolean }> {
  return request('trash', {
    method: 'GET',
    params: { limit: params.limit, offset: params.offset },
    ...(signal ? { signal } : {}),
  })
}

/**
 * Builds the download URL for a node.
 *
 * Downloads are plain navigations rather than fetch calls: the browser's own
 * download manager handles progress, resumption, and writing to disk far better
 * than anything reimplemented here, and it does not hold the file in memory.
 * The session cookie travels automatically.
 */
export function downloadUrl(id: string, options: { forceDownload?: boolean } = {}): string {
  const base = `/api/v1/nodes/${encodeURIComponent(id)}/download`
  return options.forceDownload ? `${base}?download=1` : base
}

// --- Uploads ----------------------------------------------------------------

/** `POST /api/v1/uploads` — begins a resumable session. */
export function beginUpload(body: {
  parent_id: string
  name: string
  size: number
  replace_id?: string
}): Promise<UploadSession> {
  return api.post<UploadSession>('uploads', body)
}

/** `GET /api/v1/uploads/{id}` — progress, for resuming. */
export function getUpload(id: string): Promise<UploadSession> {
  return api.get<UploadSession>(`uploads/${encodeURIComponent(id)}`)
}

/** `POST /api/v1/uploads/{id}/complete` */
export function completeUpload(id: string, checksum?: string): Promise<UploadResult> {
  return api.post<UploadResult>(
    `uploads/${encodeURIComponent(id)}/complete`,
    checksum ? { checksum } : {},
  )
}

/** `DELETE /api/v1/uploads/{id}` */
export function abortUpload(id: string): Promise<void> {
  return api.delete<void>(`uploads/${encodeURIComponent(id)}`)
}

/** `GET /api/v1/uploads` — in-flight sessions, so a reload can resume them. */
export function listUploads(signal?: AbortSignal): Promise<{ uploads: UploadSession[] }> {
  return api.get<{ uploads: UploadSession[] }>('uploads', signal ? { signal } : {})
}

/** React Query keys. Kept together so invalidation stays consistent. */
export const nodeKeys = {
  all: ['nodes'] as const,
  lists: () => [...nodeKeys.all, 'list'] as const,
  list: (params: ListNodesParams) => [...nodeKeys.lists(), params] as const,
  details: () => [...nodeKeys.all, 'detail'] as const,
  detail: (id: string) => [...nodeKeys.details(), id] as const,
  trash: () => ['trash'] as const,
  uploads: () => ['uploads'] as const,
}
