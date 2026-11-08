import type { UsageDay } from './api'
import { eventCount } from './units'

/** How many calendar months the Overview's monthly chart shows, the current one included. */
export const MONTHS = 6

/** One calendar month of a project's usage, its days added up (UTC, as usage is). */
export interface MonthTotals {
  /** `YYYY-MM`. */
  month: string
  views: number
  events: number
  measures: number
  /** Views, events and measures (`eventCount`). */
  total: number
  /** The month under way, counted only so far. */
  current: boolean
}

/** Today's date in UTC, `YYYY-MM-DD`: the day usage ranges end on. */
export function utcToday(now = new Date()): string {
  return now.toISOString().slice(0, 10)
}

/** The `YYYY-MM` of the `MONTHS` months ending with `today`'s, oldest first. */
export function monthKeys(today: string): string[] {
  const y = Number(today.slice(0, 4))
  const m = Number(today.slice(5, 7)) - 1
  return Array.from({ length: MONTHS }, (_, i) => {
    const d = new Date(Date.UTC(y, m - (MONTHS - 1 - i), 1))
    return d.toISOString().slice(0, 7)
  })
}

/** The usage range the monthly chart reads: the 1st of its oldest month through today, one query. */
export function monthsRange(today: string): { from: string; to: string } {
  return { from: `${monthKeys(today)[0]}-01`, to: today }
}

/**
 * The daily series added up by calendar month, for the `MONTHS` months
 * ending with `today`'s.
 */
export function monthlyTotals(series: UsageDay[], today: string): MonthTotals[] {
  const keys = monthKeys(today)
  const months = new Map(
    keys.map((month) => [month, { month, views: 0, events: 0, measures: 0, total: 0, current: month === keys[MONTHS - 1] } as MonthTotals])
  )
  for (const d of series) {
    const m = months.get(d.day.slice(0, 7))
    if (!m) continue
    m.views += d.views
    m.events += d.events
    m.measures += d.measures
  }
  for (const m of months.values()) m.total = eventCount(m)
  return keys.map((k) => months.get(k)!)
}

/** A month's short name, as the chart's axis shows it ("Nov"). */
export function monthLabel(month: string): string {
  return new Intl.DateTimeFormat('en-GB', { month: 'short', timeZone: 'UTC' }).format(new Date(`${month}-01T00:00:00Z`))
}

/** A month's full name and year, as the chart's tooltip heads it ("November 2026"). */
export function monthHeading(month: string): string {
  return new Intl.DateTimeFormat('en-GB', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(`${month}-01T00:00:00Z`))
}
