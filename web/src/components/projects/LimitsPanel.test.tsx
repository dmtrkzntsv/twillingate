import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import type { Limit } from '@/lib/api'
import LimitsPanel, { formatLimit } from './LimitsPanel'

describe('formatLimit', () => {
  it('formats a value in its unit', () => {
    expect(formatLimit({ value: 30, unit: 'days' })).toBe('30 days')
    expect(formatLimit({ value: 1, unit: 'days' })).toBe('1 day')
    expect(formatLimit({ value: 262144, unit: 'bytes' })).toBe('256 KiB')
    expect(formatLimit({ value: 1000, unit: 'bytes' })).toBe('1,000 bytes')
    expect(formatLimit({ value: 512, unit: 'characters' })).toBe('512 characters')
    expect(formatLimit({ value: 300, unit: 'seconds' })).toBe('5 min')
    expect(formatLimit({ value: 45, unit: 'seconds' })).toBe('45 s')
  })

  it('keeps plain numbers readable at both ends', () => {
    expect(formatLimit({ value: 500 })).toBe('500')
    expect(formatLimit({ value: 2000 })).toBe('2,000')
    expect(formatLimit({ value: 1e15 })).toBe('1e15')
    expect(formatLimit({ value: 0.0001 })).toBe('0.0001')
  })

  it('reads 0 as what it means, and only 0', () => {
    expect(formatLimit({ value: 0, zero: 'no cap' })).toBe('no cap')
    expect(formatLimit({ value: 0, unit: 'days', zero: 'kept forever' })).toBe('kept forever')
    expect(formatLimit({ value: 0, unit: 'days' })).toBe('0 days')
    // A default formats with the limit's unit and zero.
    expect(formatLimit({ value: 0, unit: 'days', zero: 'kept forever' }, 30)).toBe('30 days')
  })
})

describe('LimitsPanel', () => {
  const limits: Limit[] = [{ group: 'retention', name: 'Raw events', value: 30, unit: 'days', description: 'kept' }]

  it('says how many raw events the server holds, and in how many days', () => {
    render(<LimitsPanel limits={limits} rawEvents={{ held: 1234567, window_days: 30 }} />)
    const panel = screen.getByRole('region', { name: 'Limits' })
    expect(within(panel).getByText('Raw events held: 1,234,567 (the last 30 days)')).toBeInTheDocument()
  })

  it('says a one-day window in the singular', () => {
    render(<LimitsPanel limits={limits} rawEvents={{ held: 0, window_days: 1 }} />)
    expect(screen.getByText('Raw events held: 0 (the last day)')).toBeInTheDocument()
  })

  it('says today for a raw window of 0 days, which keeps only today raw', () => {
    render(<LimitsPanel limits={limits} rawEvents={{ held: 12, window_days: 0 }} />)
    expect(screen.getByText('Raw events held: 12 (today)')).toBeInTheDocument()
  })

  it('says nothing about raw events when the server could not count them', () => {
    render(<LimitsPanel limits={limits} />)
    expect(screen.queryByText(/Raw events held/)).not.toBeInTheDocument()
  })
})
