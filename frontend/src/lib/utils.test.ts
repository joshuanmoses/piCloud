import { describe, expect, it } from 'vitest'
import { cn, formatBytes, formatDuration, formatPercent } from './utils'

describe('cn', () => {
  it('merges class names', () => {
    expect(cn('a', 'b')).toBe('a b')
  })

  it('lets a later Tailwind class win a conflict', () => {
    // Without this, a caller-supplied className cannot override a component default.
    expect(cn('p-2', 'p-4')).toBe('p-4')
    expect(cn('text-sm text-muted-foreground', 'text-danger')).toBe('text-sm text-danger')
  })

  it('ignores falsy values', () => {
    expect(cn('a', false, undefined, null, '', 'b')).toBe('a b')
  })
})

describe('formatBytes', () => {
  it('formats using binary units, matching the backend and df', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(16 * 1024 ** 2)).toBe('16 MB')
    expect(formatBytes(20 * 1024 ** 3)).toBe('20 GB')
    expect(formatBytes(2 * 1024 ** 4)).toBe('2 TB')
  })

  it('shows one decimal for non-round values', () => {
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(2_411_724)).toBe('2.3 MB')
  })

  it('returns a placeholder for nonsense input rather than NaN', () => {
    expect(formatBytes(-1)).toBe('—')
    expect(formatBytes(Number.NaN)).toBe('—')
    expect(formatBytes(Number.POSITIVE_INFINITY)).toBe('—')
  })
})

describe('formatDuration', () => {
  it('formats across the useful ranges', () => {
    expect(formatDuration(0)).toBe('0s')
    expect(formatDuration(45)).toBe('45s')
    expect(formatDuration(90)).toBe('1m')
    expect(formatDuration(3600)).toBe('1h')
    expect(formatDuration(3725)).toBe('1h 2m')
    expect(formatDuration(86_400)).toBe('1d')
    expect(formatDuration(90_000)).toBe('1d 1h')
  })

  it('returns a placeholder for nonsense input', () => {
    expect(formatDuration(-5)).toBe('—')
    expect(formatDuration(Number.NaN)).toBe('—')
  })
})

describe('formatPercent', () => {
  it('clamps to a sensible range', () => {
    expect(formatPercent(42.7)).toBe('43%')
    expect(formatPercent(-5)).toBe('0%')
    expect(formatPercent(150)).toBe('100%')
    expect(formatPercent(42.75, 1)).toBe('42.8%')
  })
})
