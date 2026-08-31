import { create } from 'zustand'

/**
 * Interface preferences that persist between visits.
 *
 * These are conveniences, never security state. Authentication lives in an
 * HttpOnly cookie the browser manages; nothing here is trusted by the server,
 * and losing it costs the user a click.
 *
 * Persistence goes through this store rather than scattered localStorage calls
 * so the lint rule that forbids web storage elsewhere stays meaningful.
 */

export type ViewMode = 'list' | 'grid'

const VIEW_KEY = 'pifiles.view'

interface PreferencesState {
  view: ViewMode
  setView: (view: ViewMode) => void
}

function readView(): ViewMode {
  if (typeof window === 'undefined') return 'list'
  try {
    return window.localStorage.getItem(VIEW_KEY) === 'grid' ? 'grid' : 'list'
  } catch {
    // Private browsing can throw. The default is perfectly usable.
    return 'list'
  }
}

export const usePreferences = create<PreferencesState>((set) => ({
  view: readView(),
  setView: (view) => {
    try {
      window.localStorage.setItem(VIEW_KEY, view)
    } catch {
      // Not being able to remember the choice is not a reason to refuse it.
    }
    set({ view })
  },
}))
