import { create } from 'zustand'

export type ThemePreference = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'pifiles.theme'

interface ThemeState {
  /** What the user chose. 'system' follows the operating system. */
  preference: ThemePreference
  /** What is actually rendered right now. */
  resolved: 'light' | 'dark'
  setPreference: (preference: ThemePreference) => void
  /** Cycles light → dark → system, for the header toggle. */
  cycle: () => void
}

function systemPrefersDark(): boolean {
  if (typeof window === 'undefined' || !window.matchMedia) return false
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function resolve(preference: ThemePreference): 'light' | 'dark' {
  if (preference === 'system') return systemPrefersDark() ? 'dark' : 'light'
  return preference
}

function readStoredPreference(): ThemePreference {
  if (typeof window === 'undefined') return 'system'
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY)
    if (stored === 'light' || stored === 'dark' || stored === 'system') return stored
  } catch {
    // Private browsing modes can throw on localStorage access. The default is
    // perfectly usable, so this is not worth surfacing to the user.
  }
  return 'system'
}

function applyToDocument(resolved: 'light' | 'dark'): void {
  if (typeof document === 'undefined') return
  document.documentElement.classList.toggle('dark', resolved === 'dark')
  document.documentElement.style.colorScheme = resolved
}

const initialPreference = readStoredPreference()

export const useThemeStore = create<ThemeState>((set, get) => ({
  preference: initialPreference,
  resolved: resolve(initialPreference),

  setPreference: (preference) => {
    const resolved = resolve(preference)
    try {
      window.localStorage.setItem(STORAGE_KEY, preference)
    } catch {
      // Not being able to persist the choice is not a reason to refuse it.
    }
    applyToDocument(resolved)
    set({ preference, resolved })
  },

  cycle: () => {
    const order: ThemePreference[] = ['light', 'dark', 'system']
    const next = order[(order.indexOf(get().preference) + 1) % order.length]
    get().setPreference(next ?? 'system')
  },
}))

/**
 * Keeps the resolved theme in step with the operating system while the user's
 * preference is 'system'. Called once from the application root.
 *
 * @returns a cleanup function.
 */
export function watchSystemTheme(): () => void {
  if (typeof window === 'undefined' || !window.matchMedia) return () => {}

  const media = window.matchMedia('(prefers-color-scheme: dark)')
  const onChange = () => {
    const { preference, setPreference } = useThemeStore.getState()
    if (preference === 'system') setPreference('system')
  }

  media.addEventListener('change', onChange)
  return () => media.removeEventListener('change', onChange)
}

/** Applies the stored theme to the document. Safe to call more than once. */
export function initializeTheme(): void {
  applyToDocument(useThemeStore.getState().resolved)
}
