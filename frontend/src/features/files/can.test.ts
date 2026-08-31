import { describe, expect, it } from 'vitest'
import type { FileNode } from '@/types/api'
import { can, isSharedWithMe } from './can'

function node(overrides: Partial<FileNode> = {}): FileNode {
  return {
    id: 'n1',
    parent_id: 'p1',
    kind: 'file',
    name: 'file.txt',
    size_bytes: 10,
    created_at: '',
    updated_at: '',
    is_root: false,
    ...overrides,
  }
}

describe('can', () => {
  it('permits what the server granted', () => {
    const n = node({ permissions: ['read', 'download'] })
    expect(can(n, 'read')).toBe(true)
    expect(can(n, 'download')).toBe(true)
  })

  it('refuses what the server did not grant', () => {
    const n = node({ permissions: ['read', 'download'] })
    expect(can(n, 'delete')).toBe(false)
    expect(can(n, 'share')).toBe(false)
    expect(can(n, 'manage')).toBe(false)
  })

  it('refuses everything on an empty grant', () => {
    const n = node({ permissions: [] })
    expect(can(n, 'read')).toBe(false)
  })

  // Hiding every action because a field is missing would make the application
  // look broken while adding no protection — the server still refuses, and
  // with a clearer message than a silently disabled button.
  it('offers the action when the server sent no permission list', () => {
    const n = node()
    expect(can(n, 'delete')).toBe(true)
    expect(can(n, 'manage')).toBe(true)
  })

  it('refuses on a missing node', () => {
    expect(can(null, 'read')).toBe(false)
    expect(can(undefined, 'read')).toBe(false)
  })
})

describe('isSharedWithMe', () => {
  it('reports the shared flag', () => {
    expect(isSharedWithMe(node({ shared: true }))).toBe(true)
    expect(isSharedWithMe(node({ shared: false }))).toBe(false)
    expect(isSharedWithMe(node())).toBe(false)
  })
})
