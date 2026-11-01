import { describe, expect, it } from 'vitest'
import { daysUntil, daysUntilDate, formatDuration } from './time'

describe('formatDuration', () => {
  it('rounds down to the largest unit', () => {
    expect(formatDuration(30_000)).toBe('under a minute')
    expect(formatDuration(5 * 60_000 + 59_000)).toBe('5 min')
    expect(formatDuration(3 * 3_600_000)).toBe('3 h')
    expect(formatDuration(24 * 3_600_000)).toBe('1 day')
    expect(formatDuration(50 * 3_600_000)).toBe('2 days')
  })
})

describe('daysUntil', () => {
  const now = new Date('2026-11-01T12:00:00Z')
  it('counts whole days, rounded down', () => {
    expect(daysUntil(new Date('2026-11-11T12:00:00Z'), now)).toBe(10)
    expect(daysUntil(new Date('2026-11-11T11:59:59Z'), now)).toBe(9)
    expect(daysUntil(new Date('2026-11-01T12:00:00Z'), now)).toBe(0)
  })
  it('is negative once passed', () => {
    expect(daysUntil(new Date('2026-11-01T11:59:59Z'), now)).toBe(-1)
  })
})

describe('daysUntilDate', () => {
  it('counts calendar days in UTC, whatever the hour', () => {
    for (const at of ['2026-11-01T00:00:00Z', '2026-11-01T23:59:59Z']) {
      expect(daysUntilDate('2026-11-11', new Date(at))).toBe(10)
      expect(daysUntilDate('2026-11-01', new Date(at))).toBe(0)
      expect(daysUntilDate('2026-10-31', new Date(at))).toBe(-1)
    }
  })
  it('crosses a month and a daylight-saving change without drifting', () => {
    expect(daysUntilDate('2027-04-01', new Date('2026-11-01T12:00:00Z'))).toBe(151)
  })
})
