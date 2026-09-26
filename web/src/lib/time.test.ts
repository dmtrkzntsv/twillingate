import { describe, expect, it } from 'vitest'
import { formatDuration } from './time'

describe('formatDuration', () => {
  it('rounds down to the largest unit', () => {
    expect(formatDuration(30_000)).toBe('under a minute')
    expect(formatDuration(5 * 60_000 + 59_000)).toBe('5 min')
    expect(formatDuration(3 * 3_600_000)).toBe('3 h')
    expect(formatDuration(24 * 3_600_000)).toBe('1 day')
    expect(formatDuration(50 * 3_600_000)).toBe('2 days')
  })
})
