import { describe, expect, it } from 'vitest'
import { formatValue } from './format'

describe('formatValue', () => {
  it('renders null as an em dash', () => {
    expect(formatValue(null)).toBe('–')
  })

  it('renders NaN as an em dash', () => {
    expect(formatValue(Number.NaN)).toBe('–')
  })

  describe('number', () => {
    it('groups values below 10,000', () => {
      expect(formatValue(9999)).toBe('9,999')
      expect(formatValue(0)).toBe('0')
    })

    it('compacts values at or above 10,000', () => {
      expect(formatValue(12345)).toBe('12.3K')
      expect(formatValue(1234567)).toBe('1.2M')
    })
  })

  describe('percent', () => {
    it('renders a fraction as a percentage', () => {
      expect(formatValue(0.123, 'percent')).toBe('12.3%')
      expect(formatValue(1, 'percent')).toBe('100%')
      expect(formatValue(-0.05, 'percent')).toBe('-5%')
    })
  })

  describe('duration', () => {
    it('renders seconds under a minute', () => {
      expect(formatValue(45, 'duration')).toBe('45s')
    })

    it('renders minutes and seconds under an hour', () => {
      expect(formatValue(200, 'duration')).toBe('3m 20s')
    })

    it('renders hours and minutes at or above an hour', () => {
      expect(formatValue(7500, 'duration')).toBe('2h 5m')
    })
  })
})
