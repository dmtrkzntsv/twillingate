/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { applyView, cellNumber, compareText, distinctValues, emptyView, isDecimal, liveFilters, parseView, remoteArgs, valueOptions, viewChanged, type Filter, type Sort, type TableView } from './table-view'

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

describe('remote tables', () => {
  const country: Filter = { column: 'Country', op: '=', value: 'DE' }
  const browsers: Filter = { column: 'Browser', op: 'in', value: ['Firefox', 'Safari'] }
  const gone: Filter = { column: 'Gone', op: '!=', value: 'x' }
  const view = (v: Partial<TableView>): TableView => ({ ...emptyView, ...v })

  describe('remoteArgs', () => {
    it('sends nothing for the default view, with or without columns', () => {
      expect(remoteArgs(emptyView, undefined)).toEqual({})
      expect(remoteArgs(emptyView, ['Country'])).toEqual({})
      expect(remoteArgs(emptyView, [])).toEqual({})
    })

    it('leaves the offset out at 0 and sends it past the first page', () => {
      expect(remoteArgs(view({ offset: 0 }), ['Country'])).not.toHaveProperty('offset')
      expect(remoteArgs(view({ offset: 50 }), ['Country'])).toEqual({ offset: 50 })
    })

    it('sends the filters as JSON and the sort as column:dir', () => {
      const got = remoteArgs(view({ filters: [country, browsers], sort: { column: 'Country', dir: 'desc' }, offset: 25 }), ['Country', 'Browser'])
      expect(got).toEqual({ filters: JSON.stringify([country, browsers]), sort: 'Country:desc', offset: 25 })
      expect(JSON.parse(got.filters!)).toEqual([country, browsers])
    })

    it('sends the view whole before any answer has named the columns', () => {
      const got = remoteArgs(view({ filters: [country, gone], sort: { column: 'Gone', dir: 'asc' } }), undefined)
      expect(got).toEqual({ filters: JSON.stringify([country, gone]), sort: 'Gone:asc' })
    })

    it('drops a filter or sort on a column the answer lacks, keeping the rest', () => {
      const got = remoteArgs(view({ filters: [gone, country], sort: { column: 'Gone', dir: 'asc' }, offset: 10 }), ['Country'])
      expect(got).toEqual({ filters: JSON.stringify([country]), offset: 10 })
    })

    it('sends no filters or sort once an answer has named no columns, but still the offset', () => {
      expect(remoteArgs(view({ filters: [country], sort: { column: 'Country', dir: 'asc' }, offset: 5 }), [])).toEqual({ offset: 5 })
    })
  })

  describe('viewChanged', () => {
    const base = view({ filters: [country], sort: { column: 'Country', dir: 'asc' }, offset: 50 })

    it('does not count a change of page alone', () => {
      expect(viewChanged(base, { ...base, offset: 0 })).toBe(false)
      expect(viewChanged(base, { ...base, offset: 100 })).toBe(false)
      expect(viewChanged(base, { ...base, filters: [{ ...country }], sort: { column: 'Country', dir: 'asc' } })).toBe(false)
    })

    it('counts a change of filters', () => {
      expect(viewChanged(base, { ...base, filters: [] })).toBe(true)
      expect(viewChanged(base, { ...base, filters: [country, browsers] })).toBe(true)
      expect(viewChanged(base, { ...base, filters: [{ ...country, value: 'FR' }] })).toBe(true)
      expect(viewChanged(base, { ...base, filters: [{ ...country, op: '!=' }] })).toBe(true)
    })

    it('counts filters in another order as a change', () => {
      const two = { ...base, filters: [country, browsers] }
      expect(viewChanged(two, { ...two, filters: [browsers, country] })).toBe(true)
    })

    it('counts a change of sort: set, cleared, other column or direction', () => {
      expect(viewChanged(emptyView, { ...emptyView, sort: { column: 'Country', dir: 'asc' } })).toBe(true)
      expect(viewChanged(base, { ...base, sort: null })).toBe(true)
      expect(viewChanged(base, { ...base, sort: { column: 'Browser', dir: 'asc' } })).toBe(true)
      expect(viewChanged(base, { ...base, sort: { column: 'Country', dir: 'desc' } })).toBe(true)
    })
  })

  describe('valueOptions', () => {
    const rows = [
      ['DE', '12'],
      ['FR', '3'],
    ]

    it('reads each row as a value and its row count', () => {
      expect(valueOptions(rows, undefined)).toEqual([
        { value: 'DE', rows: 12, capped: false },
        { value: 'FR', rows: 3, capped: false },
      ])
      expect(valueOptions([], undefined)).toEqual([])
    })

    it('is not capped without a page', () => {
      expect(valueOptions(rows, undefined).every((o) => !o.capped)).toBe(true)
    })

    it('is not capped when the page holds every matched value', () => {
      expect(valueOptions(rows, { matched: 2, offset: 0 }).every((o) => !o.capped)).toBe(true)
      expect(valueOptions(rows, { matched: 12, offset: 10 }).every((o) => !o.capped)).toBe(true)
    })

    it('is capped when matched values lie past the page', () => {
      expect(valueOptions(rows, { matched: 3, offset: 0 }).every((o) => o.capped)).toBe(true)
      expect(valueOptions(rows, { matched: 13, offset: 10 }).every((o) => o.capped)).toBe(true)
    })
  })
})
