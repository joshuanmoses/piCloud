import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { useThemeStore } from '@/stores/theme'
import { ThemeToggle } from './ThemeToggle'

describe('ThemeToggle', () => {
  beforeEach(() => {
    localStorage.clear()
    useThemeStore.getState().setPreference('light')
  })

  it('is reachable and labelled for assistive technology', () => {
    render(<ThemeToggle />)
    expect(screen.getByRole('button', { name: 'Light theme' })).toBeInTheDocument()
  })

  it('cycles the theme when activated', async () => {
    const user = userEvent.setup()
    render(<ThemeToggle />)

    await user.click(screen.getByRole('button', { name: 'Light theme' }))
    expect(useThemeStore.getState().preference).toBe('dark')
    expect(screen.getByRole('button', { name: 'Dark theme' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Dark theme' }))
    expect(useThemeStore.getState().preference).toBe('system')
  })

  it('is operable from the keyboard', async () => {
    const user = userEvent.setup()
    render(<ThemeToggle />)

    await user.tab()
    expect(screen.getByRole('button', { name: 'Light theme' })).toHaveFocus()

    await user.keyboard('{Enter}')
    expect(useThemeStore.getState().preference).toBe('dark')
  })
})
