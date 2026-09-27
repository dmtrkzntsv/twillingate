import { describe, expect, it } from 'vitest'
import { PRESETS, resolve, todayIn } from './ranges'

const NOW = new Date('2026-09-26T12:00:00Z')

describe('PRESETS', () => {
  it('lists every preset with a label', () => {
    expect(PRESETS.map((p) => p.id)).toEqual(['today', 'yesterday', '7d', '30d', '90d', 'custom'])
    expect(PRESETS.every((p) => p.label.length > 0)).toBe(true)
  })
})

describe('todayIn', () => {
  it('resolves in the instance timezone', () => {
    // 07:30 UTC is 23:30 on the 25th in UTC-8: today must follow the given
    // tz, never the test runner's local timezone.
    const now = new Date('2026-09-26T07:30:00Z')
    expect(todayIn('UTC', now)).toBe('2026-09-26')
    expect(todayIn('Etc/GMT+8', now)).toBe('2026-09-25')
  })
})

describe('resolve', () => {
  it('today', () => {
    expect(resolve('today', 'UTC', NOW)).toEqual({ from: '2026-09-26', to: '2026-09-26' })
  })

  it('yesterday', () => {
    expect(resolve('yesterday', 'UTC', NOW)).toEqual({ from: '2026-09-25', to: '2026-09-25' })
  })

  it('7d', () => {
    expect(resolve('7d', 'UTC', NOW)).toEqual({ from: '2026-09-20', to: '2026-09-26' })
  })

  it('30d', () => {
    expect(resolve('30d', 'UTC', NOW)).toEqual({ from: '2026-08-28', to: '2026-09-26' })
  })

  it('90d', () => {
    expect(resolve('90d', 'UTC', NOW)).toEqual({ from: '2026-06-29', to: '2026-09-26' })
  })

  it('custom uses the given from/to', () => {
    expect(resolve('custom', 'UTC', NOW, { from: '2026-01-01', to: '2026-01-31' })).toEqual({
      from: '2026-01-01',
      to: '2026-01-31',
    })
  })

  it('resolves in the instance timezone', () => {
    const now = new Date('2026-09-26T07:30:00Z')
    expect(resolve('today', 'Etc/GMT+8', now)).toEqual({
      from: '2026-09-25',
      to: '2026-09-25',
    })
  })

  it('crosses a month boundary', () => {
    expect(resolve('7d', 'UTC', new Date('2026-10-03T12:00:00Z'))).toEqual({
      from: '2026-09-27',
      to: '2026-10-03',
    })
  })

  it('crosses a year boundary', () => {
    expect(resolve('30d', 'UTC', new Date('2027-01-05T12:00:00Z'))).toEqual({
      from: '2026-12-07',
      to: '2027-01-05',
    })
  })
})
