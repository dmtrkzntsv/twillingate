import { describe, expect, it } from 'vitest'
import { FULL_GRID_PX, span } from './grid'

describe('span (D37)', () => {
  it('keeps every width as defined at 1200px', () => {
    for (let w = 1; w <= 12; w++) expect(span(w, 1200)).toBe(w)
  })

  it('makes up to 6 into 6 and above 6 into 12 at 800px', () => {
    expect([1, 3, 4, 6, 7, 9, 12].map((w) => span(w, 800))).toEqual([6, 6, 6, 6, 12, 12, 12])
  })

  it('makes up to 3 into 6 and above 3 into 12 at 400px', () => {
    expect([1, 2, 3, 4, 6, 8, 12].map((w) => span(w, 400))).toEqual([6, 6, 6, 12, 12, 12, 12])
  })

  it('keeps every width at 976px, a 1280px screen beside the sidebar', () => {
    for (let w = 1; w <= 12; w++) expect(span(w, 976)).toBe(w)
  })

  it('switches exactly at 640 and FULL_GRID_PX (900)', () => {
    expect(FULL_GRID_PX).toBe(900)
    expect(span(4, 900)).toBe(4)
    expect(span(4, 899)).toBe(6)
    expect(span(4, 640)).toBe(6)
    expect(span(4, 639)).toBe(12)
  })
})
