import { act } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useThemeStore, watchSystemTheme } from './theme'

function setSystemDark(dark: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string) => ({
      matches: dark,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }),
  })
}

describe('theme store', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.classList.remove('dark')
    setSystemDark(false)
    act(() => useThemeStore.getState().setPreference('system'))
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('applies the dark class to the document when dark is selected', () => {
    act(() => useThemeStore.getState().setPreference('dark'))

    expect(useThemeStore.getState().resolved).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.style.colorScheme).toBe('dark')
  })

  it('removes the dark class when light is selected', () => {
    act(() => useThemeStore.getState().setPreference('dark'))
    act(() => useThemeStore.getState().setPreference('light'))

    expect(useThemeStore.getState().resolved).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('follows the operating system when the preference is system', () => {
    setSystemDark(true)
    act(() => useThemeStore.getState().setPreference('system'))

    expect(useThemeStore.getState().preference).toBe('system')
    expect(useThemeStore.getState().resolved).toBe('dark')
  })

  it('persists the preference so it survives a reload', () => {
    act(() => useThemeStore.getState().setPreference('dark'))
    expect(localStorage.getItem('pifiles.theme')).toBe('dark')
  })

  it('cycles light, dark, then system', () => {
    act(() => useThemeStore.getState().setPreference('light'))

    act(() => useThemeStore.getState().cycle())
    expect(useThemeStore.getState().preference).toBe('dark')

    act(() => useThemeStore.getState().cycle())
    expect(useThemeStore.getState().preference).toBe('system')

    act(() => useThemeStore.getState().cycle())
    expect(useThemeStore.getState().preference).toBe('light')
  })

  it('still works when localStorage throws, as in private browsing', () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('QuotaExceededError')
    })

    expect(() => act(() => useThemeStore.getState().setPreference('dark'))).not.toThrow()
    expect(useThemeStore.getState().resolved).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)

    setItem.mockRestore()
  })

  it('watchSystemTheme subscribes and returns a working cleanup', () => {
    const addEventListener = vi.fn()
    const removeEventListener = vi.fn()
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: () => ({ matches: false, addEventListener, removeEventListener }),
    })

    const stop = watchSystemTheme()
    expect(addEventListener).toHaveBeenCalledWith('change', expect.any(Function))

    stop()
    expect(removeEventListener).toHaveBeenCalledWith('change', expect.any(Function))
  })
})
