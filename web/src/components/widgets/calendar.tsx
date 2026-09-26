import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, SqlData, WidgetProps } from './types'

interface CalendarProps {
  format?: Format
}

export const contract: Contract = {
  description: "A year of days shaded by value, GitHub-style: daily active users, events per day.",
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'day', types: ['day'] },
      { name: 'value', types: ['number'] },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 12,
  defaultHeight: 4,
}

const WEEKS = 53
const DAY_MS = 24 * 60 * 60 * 1000
const monthFormatter = new Intl.DateTimeFormat('en-US', { month: 'short', timeZone: 'UTC' })

function parseDay(s: string): Date {
  const [y, m, d] = s.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d))
}

function formatDay(d: Date): string {
  return d.toISOString().slice(0, 10)
}

// Monday=0 ... Sunday=6, unlike Date#getUTCDay (Sunday=0).
function isoWeekday(d: Date): number {
  return (d.getUTCDay() + 6) % 7
}

/** 53 columns of 7 days (Monday..Sunday), the last column ending at `maxDay`. */
function grid(maxDay: Date): Date[][] {
  const end = new Date(maxDay.getTime() + (6 - isoWeekday(maxDay)) * DAY_MS)
  const start = new Date(end.getTime() - (WEEKS * 7 - 1) * DAY_MS)
  return Array.from({ length: WEEKS }, (_, week) =>
    Array.from({ length: 7 }, (_, day) => new Date(start.getTime() + (week * 7 + day) * DAY_MS))
  )
}

export default function Calendar({ data, props }: WidgetProps<CalendarProps>) {
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const values = new Map<string, number>()
  for (const r of records) {
    if (r.value !== null) values.set(String(r.day), Number(r.value))
  }

  const maxDay = records.reduce((max, r) => (String(r.day) > max ? String(r.day) : max), records[0].day as string)
  const columns = grid(parseDay(maxDay))

  const present = [...values.values()]
  const min = Math.min(...present)
  const max = Math.max(...present)
  const span = max - min || 1

  return (
    <div className="h-full w-full overflow-auto p-2">
      <div
        className="grid gap-0.5"
        style={{
          gridTemplateColumns: `repeat(${columns.length}, minmax(0.65rem, 1fr))`,
          gridTemplateRows: `1rem repeat(7, minmax(0.65rem, 1fr))`,
        }}
      >
        {columns.map((week, ci) => {
          const monthStart = week.find((d) => d.getUTCDate() === 1)
          return (
            <div
              key={`label-${ci}`}
              data-month-label
              className="truncate text-[10px] text-muted-foreground"
              style={{ gridColumn: ci + 1, gridRow: 1 }}
            >
              {monthStart ? monthFormatter.format(monthStart) : ''}
            </div>
          )
        })}
        {columns.map((week, ci) =>
          week.map((date, ri) => {
            const day = formatDay(date)
            const value = values.get(day)
            const hasValue = value !== undefined
            const pct = hasValue ? ((value - min) / span) * 100 : 0
            return (
              <div
                key={day}
                data-day={day}
                title={hasValue ? `${day}: ${formatValue(value, format)}` : day}
                className="aspect-square rounded-xs bg-muted"
                style={{
                  gridColumn: ci + 1,
                  gridRow: ri + 2,
                  ...(hasValue
                    ? { backgroundColor: `color-mix(in oklab, var(--chart-1) ${pct}%, transparent)` }
                    : {}),
                }}
              />
            )
          })
        )}
      </div>
    </div>
  )
}
