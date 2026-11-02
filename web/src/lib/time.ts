const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** A short, rounded-down duration: "under a minute", "5 min", "3 h", "2 days". */
export function formatDuration(ms: number): string {
  if (ms < MINUTE) return 'under a minute'
  if (ms < HOUR) return `${Math.floor(ms / MINUTE)} min`
  if (ms < DAY) return `${Math.floor(ms / HOUR)} h`
  const days = Math.floor(ms / DAY)
  return days === 1 ? '1 day' : `${days} days`
}

/** An interval given in seconds, as a menu names it: "90 s", "15 min". */
export function formatInterval(seconds: number): string {
  return seconds >= 60 && seconds % 60 === 0 ? `${seconds / 60} min` : `${seconds} s`
}

/**
 * When the data on screen was computed, in the browser's own time: just the
 * time for today, the date too for anything older.
 */
export function formatAsOf(at: Date, now: Date = new Date()): string {
  const sameDay = at.toDateString() === now.toDateString()
  return new Intl.DateTimeFormat(undefined, {
    ...(sameDay ? {} : { month: 'short', day: 'numeric' }),
    hour: '2-digit',
    minute: '2-digit',
  }).format(at)
}

/**
 * A purge date, day and short month only ("1 Oct"), formatted in UTC so
 * the label does not shift with the viewer's time zone (D17a).
 */
export function formatPurgeDate(date: Date): string {
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' }).format(date)
}

/** Whole days from `now` until `at`, rounded down; negative once passed. */
export function daysUntil(at: Date, now: Date = new Date()): number {
  return Math.floor((at.getTime() - now.getTime()) / DAY)
}

/** Calendar days from `now`'s UTC date to the `YYYY-MM-DD` date `day`; negative once passed. */
export function daysUntilDate(day: string, now: Date = new Date()): number {
  const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())
  return Math.round((Date.parse(`${day}T00:00:00Z`) - today) / DAY)
}

/** "73 days", or "expired" once the day has passed. */
export function formatDaysLeft(days: number): string {
  return days < 0 ? 'expired' : `${days} day${days === 1 ? '' : 's'}`
}
