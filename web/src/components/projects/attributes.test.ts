import { describe, expect, it } from 'vitest'
import { describeKey } from './attributes'

describe('describeKey', () => {
  it('says what a key received over the range, and when it is not received', () => {
    expect(describeKey({ key: 'plan', events: 900, values: 3, received: true, blocked: false })).toBe('900 events · 3 values')
    expect(describeKey({ key: 'k', events: 1, values: 1, received: true, blocked: false })).toBe('1 event · 1 value')
    expect(describeKey({ key: 'k', events: 12_000, values: 4_120, received: true, blocked: false })).toBe('12,000 events · 4,120 values')
    expect(describeKey({ key: 'k', events: 0, values: 0, received: false, blocked: true })).toBe('not received')
  })
})
