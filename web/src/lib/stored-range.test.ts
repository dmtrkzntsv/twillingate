import { beforeEach, describe, expect, it } from 'vitest'
import { parseStoredRange, RANGE_KEY, readStoredRange, writeStoredRange } from './stored-range'

describe('stored range', () => {
  beforeEach(() => localStorage.clear())

  it('reads back a preset, and a custom range with its dates', () => {
    writeStoredRange({ range: '30d' })
    expect(readStoredRange()).toEqual({ range: '30d' })
    writeStoredRange({ range: 'custom', from: '2026-08-01', to: '2026-08-31' })
    expect(readStoredRange()).toEqual({ range: 'custom', from: '2026-08-01', to: '2026-08-31' })
  })

  it('stores no dates with a preset', () => {
    writeStoredRange({ range: '7d', from: '2026-08-01', to: '2026-08-31' })
    expect(JSON.parse(localStorage.getItem(RANGE_KEY)!)).toEqual({ range: '7d' })
  })

  it('refuses what the switcher would not offer', () => {
    for (const v of [null, 'week', { range: 'week' }, { range: 'custom' }, { range: 'custom', from: '2026-08-31', to: '2026-08-01' }, { range: 'custom', from: '2025-01-01', to: '2026-08-01' }]) {
      expect(parseStoredRange(v)).toBeNull()
    }
  })

  it('reads nothing when none is stored or it is not JSON', () => {
    expect(readStoredRange()).toBeNull()
    localStorage.setItem(RANGE_KEY, '{')
    expect(readStoredRange()).toBeNull()
  })
})
