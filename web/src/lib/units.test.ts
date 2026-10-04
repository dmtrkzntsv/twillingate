import { describe, expect, it } from 'vitest'
import { formatAgo, formatBytes, formatDay } from './units'

describe('formatBytes', () => {
  it('uses binary-free decimal units, one decimal past the first', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(999)).toBe('999 B')
    expect(formatBytes(2_262_000)).toBe('2.3 MB')
    expect(formatBytes(2_254_000_000)).toBe('2.3 GB')
  })
})

describe('formatAgo', () => {
  const now = new Date('2026-10-03T16:05:00Z')
  it('says just now, minutes, hours and days', () => {
    expect(formatAgo('2026-10-03T16:04:40Z', now)).toBe('just now')
    expect(formatAgo('2026-10-03T16:02:11Z', now)).toBe('2 min ago')
    expect(formatAgo('2026-10-03T13:00:00Z', now)).toBe('3 h ago')
    expect(formatAgo('2026-09-30T16:05:00Z', now)).toBe('3 days ago')
  })
})

describe('formatDay', () => {
  const now = new Date('2026-10-03T01:00:00Z')
  it('says today and yesterday by the UTC day, then the date, with a year only when it differs', () => {
    expect(formatDay('2026-10-03', now)).toBe('today')
    expect(formatDay('2026-10-02', now)).toBe('yesterday')
    expect(formatDay('2026-09-28', now)).toBe('Sep 28')
    expect(formatDay('2025-12-31', now)).toBe('Dec 31, 2025')
  })
})
