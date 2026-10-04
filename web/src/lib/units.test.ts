import { describe, expect, it } from 'vitest'
import { formatAgo, formatBytes, formatDay, formatGrowth } from './units'

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

describe('formatGrowth', () => {
  const now = new Date('2026-10-03T12:00:00Z')
  it('says the change between the first and the last measured day', () => {
    const series = [
      { day: '2026-09-04', bytes: null },
      { day: '2026-09-05', bytes: 2_000_000 },
      { day: '2026-09-06', bytes: null },
      { day: '2026-10-03', bytes: 14_300_000 },
    ]
    expect(formatGrowth(series, now)).toBe('+12.3 MB since Sep 5')
    expect(formatGrowth([{ day: '2026-10-02', bytes: 5_000 }, { day: '2026-10-03', bytes: 4_000 }], now)).toBe('−1.0 kB since yesterday')
  })

  it('says nothing with under two measured days or no change', () => {
    expect(formatGrowth([], now)).toBeNull()
    expect(formatGrowth([{ day: '2026-10-03', bytes: 5 }, { day: '2026-10-02', bytes: null }], now)).toBeNull()
    expect(formatGrowth([{ day: '2026-10-02', bytes: 5 }, { day: '2026-10-03', bytes: 5 }], now)).toBeNull()
  })
})
