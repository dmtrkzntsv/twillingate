import { useLayoutEffect, useRef, useState } from 'react'
import { formatExact, formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { formatHeading, ramp } from '@/lib/chart'
import { HoverCard, ScaleLegend, useHover, type HoverRow } from '@/components/chart-parts'
import { CARD_TYPE, useCardMode } from '@/components/share/card-mode'
import type { Contract, Example, SqlData, WidgetProps } from './types'

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

// 120 days ending 2026-09-27, values swinging on a 90-unit cycle with a
// weekend dip, entirely from `i` and the end date (no Math.random/Date.now).
const CALENDAR_END = Date.UTC(2026, 8, 27)
export const examples: Example[] = [
  {
    title: 'Visitors per day',
    props: { format: 'number' },
    data: {
      columns: ['day', 'value'],
      rows: Array.from({ length: 120 }, (_, i) => {
        const date = new Date(CALENDAR_END - (119 - i) * DAY_MS)
        const day = date.toISOString().slice(0, 10)
        const dayOfWeek = date.getUTCDay()
        const value = String(200 + ((i * 37) % 90) + (dayOfWeek >= 5 ? -80 : 0))
        return [day, value]
      }),
      truncated: false,
    },
  },
]

/** One day's square and the gap after it, in SVG units; the drawing scales to fit the card. */
const CELL = 11
const STEP = 13
/** Room above the squares for the month labels. */
const TOP = 14

/** The fewest weeks a share card shows: a short series still reads as a calendar, not a few huge squares. */
const CARD_WEEKS = 13

/**
 * Where the squares and labels go, in SVG units. A tile draws a year in
 * fixed units and lets the drawing scale to the card. A share card draws
 * only the weeks the data reaches (at least CARD_WEEKS), in px of the box
 * it measured, so the squares fill the room and the labels are CARD_TYPE.
 */
function layout(weeks: number, box: { w: number; h: number } | null) {
  if (!box) return { step: STEP, cell: CELL, top: TOP, label: 9 }
  const top = CARD_TYPE * 1.6
  const step = Math.min(box.w / weeks, (box.h - top) / 7)
  return { step, cell: step * (CELL / STEP), top, label: CARD_TYPE }
}

export default function Calendar({ data, props }: WidgetProps<CalendarProps>) {
  const { hovered, bind } = useHover<string>()
  const card = useCardMode()
  // On a card, the drawing's box in px, measured once before paint (jsdom measures nothing: units then).
  const svg = useRef<SVGSVGElement>(null)
  const [box, setBox] = useState<{ w: number; h: number } | null>(null)
  useLayoutEffect(() => {
    if (!card || box || !svg.current) return
    const r = svg.current.getBoundingClientRect()
    if (r.width > 0 && r.height > 0) setBox({ w: r.width, h: r.height })
  })
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const values = new Map<string, number>()
  for (const r of records) {
    if (r.value !== null) values.set(String(r.day), Number(r.value))
  }

  const maxDay = records.reduce((max, r) => (String(r.day) > max ? String(r.day) : max), records[0].day as string)
  const year = grid(parseDay(maxDay))
  const minDay = records.reduce((min, r) => (String(r.day) < min ? String(r.day) : min), records[0].day as string)
  const firstWeek = Math.max(0, year.findIndex((week) => formatDay(week[6]) >= minDay))
  const columns = card ? year.slice(Math.min(firstWeek, WEEKS - CARD_WEEKS)) : year
  const { step, cell, top, label } = layout(columns.length, card ? box : null)

  const present = [...values.values()]
  const min = Math.min(...present)
  const max = Math.max(...present)
  const span = max - min || 1
  const fillOf = (value: number | undefined) =>
    value === undefined ? 'var(--muted)' : ramp((value - min) / span, 'var(--muted)')

  const rowsOf = (day: string): HoverRow[] => {
    const value = values.get(day)
    return value === undefined
      ? [{ label: 'No data', value: '' }]
      : [{ label: 'Value', value: formatExact(value, format), color: fillOf(value) }]
  }

  return (
    <div className="flex h-full w-full flex-col gap-1.5 p-1">
      {/* An SVG rather than a CSS grid: it keeps the squares square at any card size. */}
      <svg
        ref={svg}
        viewBox={`0 0 ${columns.length * step - (step - cell)} ${top + 7 * step - (step - cell)}`}
        className="min-h-0 w-full flex-1"
        preserveAspectRatio="xMidYMid meet"
      >
        {columns.map((week, ci) => {
          const monthStart = week.find((d) => d.getUTCDate() === 1)
          return (
            <text
              key={`label-${ci}`}
              data-month-label
              x={ci * step}
              y={label}
              fontSize={label}
              className="fill-muted-foreground"
            >
              {monthStart ? monthFormatter.format(monthStart) : ''}
            </text>
          )
        })}
        {columns.map((week, ci) =>
          week.map((date, ri) => {
            const day = formatDay(date)
            return (
              <rect
                key={day}
                data-day={day}
                {...bind(day)}
                x={ci * step}
                y={top + ri * step}
                width={cell}
                height={cell}
                rx={cell * (2.5 / CELL)}
                strokeWidth={1}
                className="stroke-transparent hover:stroke-foreground/60"
                style={{ fill: fillOf(values.get(day)) }}
              />
            )
          })
        )}
      </svg>
      <ScaleLegend min={formatValue(min, format)} max={formatValue(max, format)} base="var(--muted)" />
      {hovered && <HoverCard at={hovered} heading={formatHeading(hovered.item)} rows={rowsOf(hovered.item)} />}
    </div>
  )
}
