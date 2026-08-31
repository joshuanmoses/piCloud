import type { FileNode, Permission } from '@/types/api'

/**
 * Whether the interface should offer an action on a node.
 *
 * This is a usability affordance and nothing more. Every endpoint evaluates
 * permissions server-side on every request, so a user who forces a hidden
 * action gains nothing.
 *
 * When the server did not send a permission list — an older build, or a
 * response shape that omits it — the action is offered. Hiding everything
 * because a field is missing would make the application look broken while
 * adding no protection: the server would still refuse, and with a clearer
 * message than a silently disabled button.
 */
export function can(node: FileNode | null | undefined, permission: Permission): boolean {
  if (!node) return false
  if (node.permissions === undefined) return true
  return node.permissions.includes(permission)
}

/** True when the node is definitely not the caller's own. */
export function isSharedWithMe(node: FileNode): boolean {
  return node.shared === true
}
