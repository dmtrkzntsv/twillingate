import { describe, expect, it } from 'vitest'
import { describeKey, usedLabel } from './breakdowns'

describe('describeKey', () => {
  it('says what a key received, and when it is not received', () => {
    expect(describeKey({ key: 'plan', events: 900, max_values: 3, received: true, declared: true }, 50)).toBe('900 events · 3 values')
    expect(describeKey({ key: 'k', events: 5, max_values: 1, received: true, declared: true }, 50)).toBe('5 events · 1 value')
    expect(describeKey({ key: 'k', events: 5, max_values: null, received: true, declared: true }, 50)).toBe('5 events · values counted tonight')
    expect(describeKey({ key: 'k', events: 0, max_values: null, received: false, declared: true }, 50)).toBe('not received')
    expect(describeKey({ key: 'k', events: 0, max_values: null, received: true, declared: false }, 50)).toBe('received today · counted tonight')
  })

  it('flags a key that folds past the cap, and never with no cap', () => {
    expect(describeKey({ key: 'k', events: 980, max_values: 412, received: true, declared: false }, 50)).toBe('980 events · 412 values · folds past 50')
    expect(describeKey({ key: 'k', events: 980, max_values: 412, received: true, declared: false }, 0)).toBe('980 events · 412 values')
  })
})

describe('usedLabel', () => {
  it('says how many breakdowns are in use, against the limit when there is one', () => {
    expect(usedLabel(3, 10)).toBe('3 of 10 in use')
    expect(usedLabel(3, 0)).toBe('3 in use')
  })
})
