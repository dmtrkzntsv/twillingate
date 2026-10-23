import { describe, expect, it } from 'vitest'
import { numericX } from './chart'

const rows = (xs: string[]) => xs.map((x, i) => ({ x, y: i }))

describe('numericX', () => {
  it('keeps version strings on a category axis', () => {
    expect(numericX(rows(['1.9', '1.10', '1.2']))).toBeNull()
  })

  it('keeps non-canonical number text as text', () => {
    for (const x of ['007', '1e3', ' 3', '1.50', '']) expect(numericX(rows(['1', x]))).toBeNull()
  })

  it('sorts a numeric axis ascending', () => {
    const n = numericX(rows(['10', '0', '2', '1']))
    expect(n?.rows.map((r) => r.x)).toEqual([0, 1, 2, 10])
    expect(n?.axis.allowDecimals).toBe(false)
  })

  it('takes negative and fractional values', () => {
    const n = numericX(rows(['-2', '1.5', '0']))
    expect(n?.rows.map((r) => r.x)).toEqual([-2, 0, 1.5])
    expect(n?.axis.allowDecimals).toBe(true)
  })

  it('plots a retention curve to scale', () => {
    const xs = Array.from({ length: 46 }, (_, i) => String(i))
    const n = numericX(rows(xs))
    expect(n?.rows.map((r) => r.x)).toEqual(xs.map(Number))
  })

  it('falls back on mixed types and days', () => {
    expect(numericX(rows(['0', 'a']))).toBeNull()
    expect(numericX(rows(['2026-09-01', '2026-09-02']))).toBeNull()
  })
})
