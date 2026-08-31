import { api, request } from './client'
import type {
  CreateGroupRequest,
  CreateShareRequest,
  Group,
  GroupMember,
  Share,
  SharingVocabulary,
} from '@/types/api'

/**
 * Sharing and group endpoints.
 *
 * A share is a permission grant presented for people; the server stores one
 * row that both this and the access evaluator read, so a revoked share can
 * never leave access behind.
 */

/** `POST /api/v1/shares` */
export function createShare(body: CreateShareRequest): Promise<{ share: Share }> {
  return api.post<{ share: Share }>('shares', body)
}

/** `DELETE /api/v1/shares/{id}` */
export function revokeShare(id: string): Promise<void> {
  return api.delete<void>(`shares/${encodeURIComponent(id)}`)
}

/** `GET /api/v1/nodes/{id}/shares` — who has access to one item. */
export function listNodeShares(
  nodeId: string,
  signal?: AbortSignal,
): Promise<{ shares: Share[] }> {
  return api.get<{ shares: Share[] }>(
    `nodes/${encodeURIComponent(nodeId)}/shares`,
    signal ? { signal } : {},
  )
}

/** `GET /api/v1/shared-with-me` */
export function listSharedWithMe(signal?: AbortSignal): Promise<{ shares: Share[] }> {
  return api.get<{ shares: Share[] }>('shared-with-me', signal ? { signal } : {})
}

/**
 * `GET /api/v1/sharing/vocabulary`
 *
 * The permission and role names come from the server so the interface cannot
 * offer a role the server does not recognise, or miss one it adds.
 */
export function fetchSharingVocabulary(signal?: AbortSignal): Promise<SharingVocabulary> {
  return api.get<SharingVocabulary>('sharing/vocabulary', signal ? { signal } : {})
}

/** `GET /api/v1/groups` — search groups by name. */
export function listGroups(search: string, signal?: AbortSignal): Promise<{ groups: Group[] }> {
  return request<{ groups: Group[] }>('groups', {
    method: 'GET',
    params: search ? { search } : {},
    ...(signal ? { signal } : {}),
  })
}

/** `GET /api/v1/groups/mine` */
export function listMyGroups(signal?: AbortSignal): Promise<{ groups: Group[] }> {
  return api.get<{ groups: Group[] }>('groups/mine', signal ? { signal } : {})
}

/** `GET /api/v1/groups/{id}` — includes members, for members and admins. */
export function getGroup(
  id: string,
  signal?: AbortSignal,
): Promise<{ group: Group; members: GroupMember[] }> {
  return api.get<{ group: Group; members: GroupMember[] }>(
    `groups/${encodeURIComponent(id)}`,
    signal ? { signal } : {},
  )
}

export const sharingKeys = {
  all: ['sharing'] as const,
  vocabulary: () => [...sharingKeys.all, 'vocabulary'] as const,
  nodeShares: (nodeId: string) => [...sharingKeys.all, 'node', nodeId] as const,
  sharedWithMe: () => [...sharingKeys.all, 'shared-with-me'] as const,
  groups: (search: string) => [...sharingKeys.all, 'groups', search] as const,
  group: (id: string) => [...sharingKeys.all, 'groups', 'detail', id] as const,
  myGroups: () => [...sharingKeys.all, 'groups', 'mine'] as const,
}

// --- Group management --------------------------------------------------------
//
// Creating and deleting a group needs an administrator: a group is an
// instance-wide building block of access control, and one anybody could create
// would quickly become a directory of unmanaged ones. Changing membership needs
// an administrator *or* a manager of that group.

/** `POST /api/v1/groups` */
export function createGroup(body: CreateGroupRequest): Promise<{ group: Group }> {
  return api.post<{ group: Group }>('groups', body)
}

/** `PATCH /api/v1/groups/{id}` */
export function updateGroup(
  id: string,
  changes: { name?: string; description?: string },
): Promise<{ group: Group }> {
  return api.patch<{ group: Group }>(`groups/${encodeURIComponent(id)}`, changes)
}

/** `DELETE /api/v1/groups/{id}` */
export function deleteGroup(id: string): Promise<void> {
  return api.delete<void>(`groups/${encodeURIComponent(id)}`)
}

/** `POST /api/v1/groups/{id}/members` */
export function addGroupMember(
  id: string,
  username: string,
  role: 'member' | 'manager' = 'member',
): Promise<void> {
  return api.post<void>(`groups/${encodeURIComponent(id)}/members`, { username, role })
}

/** `DELETE /api/v1/groups/{id}/members/{userId}` */
export function removeGroupMember(id: string, userId: string): Promise<void> {
  return api.delete<void>(
    `groups/${encodeURIComponent(id)}/members/${encodeURIComponent(userId)}`,
  )
}
