import { describe, expect, it } from 'vitest'
import type { UsageDay } from './api'
import { monthHeading, monthKeys, monthLabel, monthlyTotals, monthsRange, utcToday } from './usage-months'

const day = (d: string, views: number, events = 0, measures = 0, checks = 0): UsageDay => ({ day: d, views, events, measures, checks, total_bytes: null })

describe('usage months', () => {
  it('takes today in UTC, whatever the local zone', () => {
    expect(utcToday(new Date('2026-11-30T23:30:00-05:00'))).toBe('2026-12-01')
  })

  it('names the six months ending with the current one, across a year end', () => {
    expect(monthKeys('2026-02-14')).toEqual(['2025-09', '2025-10', '2025-11', '2025-12', '2026-01', '2026-02'])
    expect(monthsRange('2026-02-14')).toEqual({ from: '2025-09-01', to: '2026-02-14' })
  })

  it('adds the days up by month, the current one so far, the checks at their weight', () => {
    const months = monthlyTotals(
      [day('2026-05-31', 9), day('2026-06-01', 1, 2, 3, 100), day('2026-06-30', 4), day('2026-11-01', 5), day('2026-11-07', 6, 1)],
      '2026-11-07',
      0.5
    )
    expect(months.map((m) => m.month)).toEqual(['2026-06', '2026-07', '2026-08', '2026-09', '2026-10', '2026-11'])
    expect(months[0]).toMatchObject({ views: 5, events: 2, measures: 3, checks: 50, total: 60, current: false })
    expect(months[1].total).toBe(0)
    expect(months[5]).toMatchObject({ views: 11, events: 1, total: 12, current: true })
    // A day before the oldest month is not counted.
    expect(months.reduce((n, m) => n + m.total, 0)).toBe(72)
  })

  it('weighs checks by a fractional ratio', () => {
    const months = monthlyTotals([day('2026-10-03', 60, 0, 0, 100), day('2026-11-02', 10, 0, 0, 100)], '2026-11-07', 0.1)
    expect(months[4]).toMatchObject({ checks: 10, total: 70 })
    expect(months[5]).toMatchObject({ checks: 10, total: 20 })
  })

  it('counts no checks at a ratio of 0', () => {
    const months = monthlyTotals([day('2026-11-02', 10, 0, 0, 100)], '2026-11-07', 0)
    expect(months[5]).toMatchObject({ checks: 0, total: 10 })
  })

  it('names months for the axis and the tooltip', () => {
    expect(monthLabel('2026-11')).toBe('Nov')
    expect(monthHeading('2026-11')).toBe('November 2026')
  })
})
