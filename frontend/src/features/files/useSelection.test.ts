import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { useSelection } from './useSelection'

const ids = ['a', 'b', 'c', 'd', 'e']

const plain = { shiftKey: false, ctrlKey: false, metaKey: false }
const withCtrl = { shiftKey: false, ctrlKey: true, metaKey: false }
const withShift = { shiftKey: true, ctrlKey: false, metaKey: false }

describe('useSelection', () => {
  it('starts empty', () => {
    const { result } = renderHook(() => useSelection(ids))
    expect(result.current.count).toBe(0)
  })

  it('a plain click selects only that item', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.handleClick('b', plain))
    expect([...result.current.selected]).toEqual(['b'])

    act(() => result.current.handleClick('d', plain))
    expect([...result.current.selected]).toEqual(['d'])
  })

  it('ctrl-click adds and removes', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.handleClick('a', plain))
    act(() => result.current.handleClick('c', withCtrl))
    expect(result.current.count).toBe(2)
    expect(result.current.isSelected('a')).toBe(true)
    expect(result.current.isSelected('c')).toBe(true)

    // Clicking an already-selected item with ctrl removes it.
    act(() => result.current.handleClick('a', withCtrl))
    expect([...result.current.selected]).toEqual(['c'])
  })

  it('cmd-click behaves like ctrl-click, for macOS', () => {
    const { result } = renderHook(() => useSelection(ids))
    act(() => result.current.handleClick('a', plain))
    act(() => result.current.handleClick('c', { shiftKey: false, ctrlKey: false, metaKey: true }))
    expect(result.current.count).toBe(2)
  })

  it('shift-click selects the range from the anchor', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.handleClick('b', plain))
    act(() => result.current.handleClick('d', withShift))
    expect([...result.current.selected].sort()).toEqual(['b', 'c', 'd'])
  })

  it('shift-click works backwards', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.handleClick('d', plain))
    act(() => result.current.handleClick('b', withShift))
    expect([...result.current.selected].sort()).toEqual(['b', 'c', 'd'])
  })

  // The anchor stays put across successive shift-clicks, so the range grows
  // and shrinks from one fixed point rather than creeping along.
  it('successive shift-clicks extend from the same anchor', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.handleClick('b', plain))
    act(() => result.current.handleClick('d', withShift))
    act(() => result.current.handleClick('c', withShift))

    expect([...result.current.selected].sort()).toEqual(['b', 'c'])
  })

  it('selectAll and clear', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.selectAll())
    expect(result.current.count).toBe(5)

    act(() => result.current.clear())
    expect(result.current.count).toBe(0)
  })

  it('toggle flips one item', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.toggle('c'))
    expect(result.current.isSelected('c')).toBe(true)

    act(() => result.current.toggle('c'))
    expect(result.current.isSelected('c')).toBe(false)
  })

  // After a delete or a refresh, a selection referring to items that no longer
  // exist would make bulk actions target missing ids.
  it('reconcile drops ids that no longer exist', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.select(['a', 'b', 'c']))
    act(() => result.current.reconcile(['a', 'c', 'e']))

    expect([...result.current.selected].sort()).toEqual(['a', 'c'])
  })

  it('reconcile leaves an unaffected selection alone', () => {
    const { result } = renderHook(() => useSelection(ids))

    act(() => result.current.select(['a', 'b']))
    const before = result.current.selected

    act(() => result.current.reconcile(ids))
    expect(result.current.selected).toBe(before)
  })

  it('shift-click falls back to a plain selection when the anchor is gone', () => {
    const { result, rerender } = renderHook(({ list }) => useSelection(list), {
      initialProps: { list: ids },
    })

    act(() => result.current.handleClick('a', plain))
    // The anchor's item disappears, as it would after a delete.
    rerender({ list: ['c', 'd', 'e'] })
    act(() => result.current.reconcile(['c', 'd', 'e']))
    act(() => result.current.handleClick('d', withShift))

    expect([...result.current.selected]).toEqual(['d'])
  })
})
