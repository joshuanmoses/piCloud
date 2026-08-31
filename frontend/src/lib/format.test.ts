import { describe, expect, it } from 'vitest'
import { describeUserAgent, formatRelative, initials } from './format'

describe('initials', () => {
  it('takes the first and last initial of a full name', () => {
    expect(initials('Josh Moses')).toBe('JM')
    expect(initials('Ann Marie Smith')).toBe('AS')
  })

  it('takes two letters from a single name', () => {
    expect(initials('josh')).toBe('JO')
  })

  it('handles empty and whitespace-only input', () => {
    expect(initials('')).toBe('?')
    expect(initials('   ')).toBe('?')
  })
})

describe('describeUserAgent', () => {
  it('recognises common browser and platform combinations', () => {
    expect(
      describeUserAgent(
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36',
      ),
    ).toBe('Chrome on macOS')

    expect(describeUserAgent('Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0')).toBe(
      'Firefox on Linux',
    )

    expect(describeUserAgent('Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari/604.1')).toBe(
      'Safari on iOS',
    )
  })

  it('falls back gracefully for unknown or missing values', () => {
    expect(describeUserAgent(undefined)).toBe('Unknown device')
    expect(describeUserAgent('')).toBe('Unknown device')
    expect(describeUserAgent('SomeRobot/1.0')).toBe('Unknown browser')
  })

  // The user agent is attacker-controlled. It must never be echoed back into
  // the page, only mapped onto a fixed label.
  it('never echoes the raw value, so it cannot carry markup', () => {
    const hostile = '<img src=x onerror=alert(1)> Chrome Windows'
    const result = describeUserAgent(hostile)
    expect(result).not.toContain('<')
    expect(result).not.toContain('onerror')
    expect(result).toBe('Chrome on Windows')
  })
})

describe('formatRelative', () => {
  const now = new Date('2026-08-26T12:00:00Z').getTime()
  const ago = (seconds: number) => new Date(now - seconds * 1000).toISOString()

  it('formats across the useful ranges', () => {
    expect(formatRelative(ago(5), now)).toBe('just now')
    expect(formatRelative(ago(59), now)).toBe('just now')
    expect(formatRelative(ago(60), now)).toBe('1 minute ago')
    expect(formatRelative(ago(600), now)).toBe('10 minutes ago')
    expect(formatRelative(ago(3600), now)).toBe('1 hour ago')
    expect(formatRelative(ago(7200), now)).toBe('2 hours ago')
    expect(formatRelative(ago(86_400), now)).toBe('yesterday')
    expect(formatRelative(ago(3 * 86_400), now)).toBe('3 days ago')
  })

  it('handles an unparseable timestamp', () => {
    expect(formatRelative('not a date', now)).toBe('unknown')
  })

  // Clock skew between the server and the browser can produce a future
  // timestamp; "in -3 minutes" would look broken.
  it('handles a future timestamp from clock skew', () => {
    const future = new Date(now + 60_000).toISOString()
    expect(formatRelative(future, now)).toBe('just now')
  })
})
