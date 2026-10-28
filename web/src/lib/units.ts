import { formatHeading, formatTick } from './chart'

/** How long ago an ISO time was: just now, 2 min ago, 3 h ago, 3 days ago. */
export function formatAgo(iso: string, now: Date = new Date()): string {
  const s = Math.max(0, (now.getTime() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  const d = Math.floor(s / 86400)
  return `${d} ${d === 1 ? 'day' : 'days'} ago`
}

/** A UTC day (YYYY-MM-DD) as a person says it: today, yesterday, Oct 2, or Oct 2, 2025 in another year. */
export function formatDay(day: string, now: Date = new Date()): string {
  const today = now.toISOString().slice(0, 10)
  const yesterday = new Date(now.getTime() - 86_400_000).toISOString().slice(0, 10)
  if (day === today) return 'today'
  if (day === yesterday) return 'yesterday'
  return day.slice(0, 4) === today.slice(0, 4) ? formatTick(day) : formatHeading(day)
}

/**
 * A range of UTC days as a person says it: "Sep 30 – Oct 6", "Oct 6" for
 * one day, years shown when the range leaves `now`'s year.
 */
export function formatSpan(from: string, to: string, now: Date = new Date()): string {
  const year = now.toISOString().slice(0, 4)
  const fmt = from.slice(0, 4) === year && to.slice(0, 4) === year ? formatTick : formatHeading
  return from === to ? fmt(from) : `${fmt(from)} – ${fmt(to)}`
}

/** The first UTC day of the month `back` months before `now`'s: the start of a range of `back + 1` months. */
export function monthStart(back: number, now: Date = new Date()): string {
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - back, 1)).toISOString().slice(0, 10)
}

/**
 * Every project's views, events and measures summed by UTC month, oldest
 * first, a month with none included: [{month: '2026-09', count: 47210}].
 */
export function eventsByMonth(projects: { series: { day: string; views: number; events: number; measures: number }[] }[]): { month: string; count: number }[] {
  const counts = new Map<string, number>()
  for (const p of projects) {
    for (const d of p.series) {
      const month = d.day.slice(0, 7)
      counts.set(month, (counts.get(month) ?? 0) + d.views + d.events + d.measures)
    }
  }
  return [...counts].sort(([a], [b]) => a.localeCompare(b)).map(([month, count]) => ({ month, count }))
}
