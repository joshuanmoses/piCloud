import { Monitor, Moon, Sun } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useThemeStore, type ThemePreference } from '@/stores/theme'

const icons: Record<ThemePreference, typeof Sun> = {
  light: Sun,
  dark: Moon,
  system: Monitor,
}

const labels: Record<ThemePreference, string> = {
  light: 'Light theme',
  dark: 'Dark theme',
  system: 'Match system theme',
}

/** Cycles the colour theme: light → dark → follow the system. */
export function ThemeToggle() {
  const preference = useThemeStore((s) => s.preference)
  const cycle = useThemeStore((s) => s.cycle)
  const Icon = icons[preference]

  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={cycle}
      // The label states the *current* setting; a button whose name changes to
      // describe the next state is disorienting with a screen reader.
      aria-label={labels[preference]}
      title={labels[preference]}
    >
      <Icon aria-hidden="true" />
    </Button>
  )
}
