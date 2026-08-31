import { useCallback, useMemo, useRef, useState } from 'react'

/**
 * Multi-selection with the interaction model people already know from a
 * desktop file manager:
 *
 *   click          select only this
 *   ctrl/cmd+click add or remove this
 *   shift+click    select the range from the anchor to this
 *
 * The anchor is the last item selected without shift, which is what makes a
 * sequence of shift-clicks extend from a fixed point rather than creeping.
 */
export interface SelectionApi {
  selected: ReadonlySet<string>
  isSelected: (id: string) => boolean
  count: number
  /** Handles a click with its modifier keys. */
  handleClick: (id: string, event: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => void
  select: (ids: string[]) => void
  toggle: (id: string) => void
  selectAll: () => void
  clear: () => void
  /** Drops ids that no longer exist, after a delete or a navigation. */
  reconcile: (availableIds: string[]) => void
}

export function useSelection(orderedIds: string[]): SelectionApi {
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set())
  const anchor = useRef<string | null>(null)

  const isSelected = useCallback((id: string) => selected.has(id), [selected])

  const handleClick = useCallback(
    (id: string, event: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => {
      const ids = orderedIds

      if (event.shiftKey && anchor.current) {
        const from = ids.indexOf(anchor.current)
        const to = ids.indexOf(id)
        if (from !== -1 && to !== -1) {
          const [start, end] = from <= to ? [from, to] : [to, from]
          setSelected(new Set(ids.slice(start, end + 1)))
          return
        }
      }

      if (event.ctrlKey || event.metaKey) {
        setSelected((current) => {
          const next = new Set(current)
          if (next.has(id)) next.delete(id)
          else next.add(id)
          return next
        })
        anchor.current = id
        return
      }

      setSelected(new Set([id]))
      anchor.current = id
    },
    [orderedIds],
  )

  const select = useCallback((ids: string[]) => {
    setSelected(new Set(ids))
    anchor.current = ids.at(-1) ?? null
  }, [])

  const toggle = useCallback((id: string) => {
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
    anchor.current = id
  }, [])

  const selectAll = useCallback(() => {
    setSelected(new Set(orderedIds))
  }, [orderedIds])

  const clear = useCallback(() => {
    setSelected(new Set())
    anchor.current = null
  }, [])

  const reconcile = useCallback((availableIds: string[]) => {
    const available = new Set(availableIds)
    setSelected((current) => {
      // Only replace the set when something actually dropped out, so a
      // listing refresh does not cause a pointless re-render.
      let changed = false
      const next = new Set<string>()
      for (const id of current) {
        if (available.has(id)) next.add(id)
        else changed = true
      }
      return changed ? next : current
    })
    if (anchor.current && !available.has(anchor.current)) {
      anchor.current = null
    }
  }, [])

  return useMemo(
    () => ({
      selected,
      isSelected,
      count: selected.size,
      handleClick,
      select,
      toggle,
      selectAll,
      clear,
      reconcile,
    }),
    [selected, isSelected, handleClick, select, toggle, selectAll, clear, reconcile],
  )
}
