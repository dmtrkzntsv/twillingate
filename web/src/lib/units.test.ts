import { describe, expect, it } from 'vitest'
import { eventsByMonth, formatAgo, formatDay, formatSpan, monthStart } from './units'

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

describe('formatSpan', () => {
  const now = new Date('2026-10-06T12:00:00Z')
  it('says a range in the current year without years, one day once', () => {
    expect(formatSpan('2026-09-30', '2026-10-06', now)).toBe('Sep 30 – Oct 6')
    expect(formatSpan('2026-10-06', '2026-10-06', now)).toBe('Oct 6')
  })
  it('names the years when the range leaves the current year', () => {
    expect(formatSpan('2025-12-30', '2026-01-05', now)).toBe('Dec 30, 2025 – Jan 5, 2026')
  })
})

describe('monthStart', () => {
  it('goes back whole months, across a year', () => {
    const now = new Date('2026-03-31T23:00:00Z')
    expect(monthStart(0, now)).toBe('2026-03-01')
    expect(monthStart(5, now)).toBe('2025-10-01')
  })
})

describe('eventsByMonth', () => {
  it('sums every family of every project by month, oldest first', () => {
    const day = (d: string, views: number, events = 0, measures = 0) => ({ day: d, views, events, measures })
    expect(eventsByMonth([
      { series: [day('2026-10-01', 1, 2, 3), day('2026-09-30', 4)] },
      { series: [day('2026-09-01', 0), day('2026-10-02', 10)] },
    ])).toEqual([{ month: '2026-09', count: 4 }, { month: '2026-10', count: 16 }])
  })
})
