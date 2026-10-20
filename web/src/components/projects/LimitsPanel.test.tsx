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
  const limits: Limit[] = [{ group: 'retention', name: 'Event files', value: 400, unit: 'days', description: 'kept' }]

  it('says how many events the last 30 days hold', () => {
    render(<LimitsPanel limits={limits} events30d={{ count: 1234567, from: '2026-09-21', to: '2026-10-20' }} />)
    const panel = screen.getByRole('region', { name: 'Limits' })
    expect(within(panel).getByText('Events in the last 30 days: 1,234,567')).toBeInTheDocument()
  })

  it('says nothing about the events when the server could not count them', () => {
    render(<LimitsPanel limits={limits} />)
    expect(screen.queryByText(/Events in the last 30 days/)).not.toBeInTheDocument()
  })

  it('has no caps group', () => {
    render(<LimitsPanel limits={limits} />)
    expect(screen.queryByRole('region', { name: 'Caps' })).not.toBeInTheDocument()
    expect(screen.queryByText(/caps/i)).not.toBeInTheDocument()
  })
})
