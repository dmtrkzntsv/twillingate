import { describe, expect, it } from 'vitest'
import type { Contract, SqlData } from '@/components/widgets/types'
import { pivot, toRecords } from './records'

const contract: Contract = {
  description: 'test',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['day', 'text'] },
      { name: 'y', types: ['number'] },
      { name: 'series', types: ['text'], optional: true },
    ],
  },
  props: { type: 'object', properties: {}, additionalProperties: false },
  defaultWidth: 6,
  defaultHeight: 8,
}

describe('toRecords', () => {
  it('parses declared number columns and nulls out empty strings', () => {
    const data: SqlData = {
      columns: ['x', 'y'],
      rows: [
        ['2026-01-01', '12'],
        ['2026-01-02', ''],
      ],
      truncated: false,
    }
    expect(toRecords(data, contract)).toEqual([
      { x: '2026-01-01', y: 12 },
      { x: '2026-01-02', y: null },
    ])
  })

  it('keeps non-number columns as strings, including empty ones', () => {
    const data: SqlData = {
      columns: ['x', 'y', 'series'],
      rows: [['2026-01-01', '5', '']],
      truncated: false,
    }
    expect(toRecords(data, contract)).toEqual([{ x: '2026-01-01', y: 5, series: '' }])
  })
})

describe('pivot', () => {
  it('turns long rows into one row per x with one key per series', () => {
    const records = [
      { x: '2026-01-01', series: 'a', y: 1 },
      { x: '2026-01-01', series: 'b', y: 2 },
      { x: '2026-01-02', series: 'a', y: 3 },
    ]
    expect(pivot(records, 'x', 'series', 'y')).toEqual({
      rows: [
        { x: '2026-01-01', a: 1, b: 2 },
        { x: '2026-01-02', a: 3 },
      ],
      keys: ['a', 'b'],
    })
  })
})
