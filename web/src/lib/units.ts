import { formatHeading, formatTick } from './chart'

/** Bytes in decimal units: 999 B, 2.3 MB, 2.3 GB. */
export function formatBytes(n: number): string {
  if (n < 1000) return `${n} B`
  const units = ['kB', 'MB', 'GB', 'TB']
  let v = n
  let i = -1
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

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
 * How much the database grew over the measured days of a series:
 * "+12.3 MB since Sep 4", "−1.0 MB since Sep 4". Null with fewer than two
 * measured days, or none of them changed.
 */
export function formatGrowth(series: { day: string; bytes: number | null }[], now: Date = new Date()): string | null {
  const measured = series.filter((d): d is { day: string; bytes: number } => d.bytes !== null)
  if (measured.length < 2) return null
  const first = measured[0]
  const delta = measured[measured.length - 1].bytes - first.bytes
  if (delta === 0) return null
  return `${delta > 0 ? '+' : '−'}${formatBytes(Math.abs(delta))} since ${formatDay(first.day, now)}`
}
