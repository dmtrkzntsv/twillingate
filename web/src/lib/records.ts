import type { Contract, SqlData } from '@/components/widgets/types'

export type Record_ = Record<string, string | number | null>

/**
 * Turns a query's raw string rows into typed records, keyed by the columns
 * the contract declares. A column typed `number` is parsed, with an empty
 * string (SQL `NULL`) becoming `null`; every other column stays a string,
 * empty or not.
 */
export function toRecords(d: SqlData, c: Contract): Record_[] {
  const numberColumns = new Set(
    c.inputs.columns.filter((col) => col.types.includes('number')).map((col) => col.name)
  )
  return d.rows.map((row) => {
    const record: Record_ = {}
    d.columns.forEach((name, i) => {
      const raw = row[i] ?? ''
      record[name] = numberColumns.has(name) ? (raw === '' ? null : Number(raw)) : raw
    })
    return record
  })
}

/**
 * Reshapes long rows (one per x/series pair) into one row per `x`, with one
 * key per distinct `series` value — what Recharts' multi-line/area/bar
 * charts expect. `keys` lists the series in first-seen order, for stable
 * colors and legend order.
 */
export function pivot(
  records: Record<string, unknown>[],
  x: string,
  series: string,
  y: string
): { rows: Record<string, unknown>[]; keys: string[] } {
  const rows: Record<string, unknown>[] = []
  const rowIndex = new Map<unknown, number>()
  const keys: string[] = []
  const seenKeys = new Set<string>()

  for (const record of records) {
    const xValue = record[x]
    const seriesKey = String(record[series])
    if (!seenKeys.has(seriesKey)) {
      seenKeys.add(seriesKey)
      keys.push(seriesKey)
    }
    let position = rowIndex.get(xValue)
    if (position === undefined) {
      position = rows.length
      rowIndex.set(xValue, position)
      rows.push({ [x]: xValue })
    }
    rows[position][seriesKey] = record[y]
  }

  return { rows, keys }
}
