import { describe, expect, it } from 'vitest'
import { formatExact, formatValue } from './format'

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

    it('pins the compact boundary at exactly 10,000', () => {
      expect(formatValue(9999)).toBe('9,999')
      expect(formatValue(10000)).toBe('10K')
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

    it('pins the minute boundary at exactly 60 seconds', () => {
      expect(formatValue(59, 'duration')).toBe('59s')
      expect(formatValue(60, 'duration')).toBe('1m 0s')
    })

    it('pins the hour boundary at exactly 3,600 seconds', () => {
      expect(formatValue(3599, 'duration')).toBe('59m 59s')
      expect(formatValue(3600, 'duration')).toBe('1h 0m')
    })
  })
})

describe('formatExact', () => {
  it('shows every digit of a number the widget compacts', () => {
    expect(formatExact(12345)).toBe('12,345')
    expect(formatExact(1234567.891)).toBe('1,234,567.89')
  })

  it('shows percents, durations and missing values as formatValue does', () => {
    expect(formatExact(0.5, 'percent')).toBe('50%')
    expect(formatExact(75, 'duration')).toBe('1m 15s')
    expect(formatExact(null)).toBe('–')
  })
})
