/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { applyView, cellNumber, compareText, distinctValues, emptyView, isDecimal, liveFilters, parseView, type Filter, type Sort } from './table-view'

// jsdom replaces the URL global, so import.meta.url cannot build a file path here.
const file = resolve(import.meta.dirname, '../../../internal/reporting/testdata/table-filters.json')
const cases = JSON.parse(readFileSync(file, 'utf8')) as {
  columns: string[]
  cells: string[][]
  cases: { name: string; filters: Filter[]; sort?: Sort; expect: number[] }[]
  distinct: { name: string; column: string; filters: Filter[]; expect: [string, number][] }[]
}

describe('the shared cases (internal/reporting/testdata/table-filters.json)', () => {
  for (const c of cases.cases) {
    it(c.name, () => {
      const got = applyView(cases.cells, cases.columns, { filters: c.filters, sort: c.sort ?? null, offset: 0 }, 1000)
      expect(got.rows).toEqual(c.expect.map((i) => cases.cells[i]))
      expect(got.matched).toBe(c.expect.length)
      expect(got.total).toBe(cases.cells.length)
    })
  }
  for (const c of cases.distinct) {
    it(`distinct: ${c.name}`, () => {
      expect(distinctValues(cases.cells, cases.columns, c.column, c.filters).map((d) => [d.value, d.rows])).toEqual(c.expect)
    })
  }
})

describe('numbers', () => {
  it('reads decimals only', () => {
    for (const s of ['0', '-12', '1.5', '1e21', '1e+21', '1e-06']) expect(isDecimal(s)).toBe(true)
    for (const s of ['', ' 1', '+1', '1.', '.5', '0x10', 'NaN', 'Infinity', '12abc', '2026-09-25']) expect(isDecimal(s)).toBe(false)
    expect(cellNumber('1e999')).toBeNull()
    expect(cellNumber('-0.25E3')).toBe(-250)
  })
})

describe('text', () => {
  it('compares by code point, not UTF-16 unit', () => {
    // U+FF5E (BMP, high) sorts before U+1F600 (astral) by code point; by UTF-16 unit it would not.
    expect(compareText('～', '\u{1F600}')).toBeLessThan(0)
  })
})

describe('paging and stored views', () => {
  it('slices after filtering and sorting', () => {
    const got = applyView(cases.cells, cases.columns, { filters: [], sort: { column: 'Count', dir: 'desc' }, offset: 2 }, 3)
    expect(got.rows).toHaveLength(3)
    expect(got.matched).toBe(cases.cells.length)
  })
  it('ignores filters on columns the result does not have', () => {
    const view = { ...emptyView, filters: [{ column: 'Gone', op: '=' as const, value: 'x' }] }
    expect(liveFilters(view, cases.columns)).toEqual([])
    expect(applyView(cases.cells, cases.columns, view, 1000).matched).toBe(cases.cells.length)
  })
  it('parses stored views tolerantly', () => {
    expect(parseView(null)).toBeNull()
    expect(parseView({ filters: 'x' })).toEqual(emptyView)
    expect(parseView({ filters: [{ column: 'a', op: 'in', value: ['1'] }, { column: 'b', op: '~', value: '1' }], sort: { column: 'a', dir: 'asc' } }))
      .toEqual({ filters: [{ column: 'a', op: 'in', value: ['1'] }], sort: { column: 'a', dir: 'asc' }, offset: 0 })
  })
})
