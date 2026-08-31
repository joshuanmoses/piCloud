import { api, request } from './client'
import type { FileNode, VersionListResponse } from '@/types/api'

/**
 * File version history.
 *
 * A version has no permissions of its own: every endpoint here is addressed
 * through the file it belongs to, and the server checks against that file. The
 * URLs mirror that — there is no `/versions/{id}` that stands alone.
 */

/** `GET /api/v1/nodes/{id}/versions` */
export function listVersions(
  nodeId: string,
  signal?: AbortSignal,
): Promise<VersionListResponse> {
  return api.get<VersionListResponse>(
    `nodes/${encodeURIComponent(nodeId)}/versions`,
    signal ? { signal } : {},
  )
}

/** `POST /api/v1/nodes/{id}/versions/{versionId}/restore` */
export function restoreVersion(nodeId: string, versionId: string): Promise<{ node: FileNode }> {
  return api.post<{ node: FileNode }>(
    `nodes/${encodeURIComponent(nodeId)}/versions/${encodeURIComponent(versionId)}/restore`,
  )
}

/** `DELETE /api/v1/nodes/{id}/versions/{versionId}` */
export function deleteVersion(nodeId: string, versionId: string): Promise<void> {
  return request<void>(
    `nodes/${encodeURIComponent(nodeId)}/versions/${encodeURIComponent(versionId)}`,
    { method: 'DELETE' },
  )
}

/**
 * The URL that downloads one version.
 *
 * A plain navigation, like every other download. The server names the file with
 * its version number, so downloading three of them does not leave you with
 * "Report.pdf", "Report (1).pdf" and no idea which is which.
 */
export function versionDownloadUrl(nodeId: string, versionId: string): string {
  return `/api/v1/nodes/${encodeURIComponent(nodeId)}/versions/${encodeURIComponent(versionId)}/download`
}

/** React Query keys. */
export const versionKeys = {
  all: ['versions'] as const,
  forNode: (nodeId: string) => [...versionKeys.all, nodeId] as const,
}
