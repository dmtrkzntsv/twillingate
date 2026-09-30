export type Preset = 'today' | 'yesterday' | '7d' | '30d' | '90d' | 'custom'

export const PRESETS: { id: Preset; label: string }[] = [
  { id: 'today', label: 'Today' },
  { id: 'yesterday', label: 'Yesterday' },
  { id: '7d', label: 'Last week' },
  { id: '30d', label: 'Last month' },
  { id: '90d', label: 'Last 90 days' },
  { id: 'custom', label: 'Custom…' },
]

/** The calendar date in `tz`, as `YYYY-MM-DD`. */
export function todayIn(tz: string, now: Date = new Date()): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: tz,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(now)
  const part = (type: string) => parts.find((p) => p.type === type)?.value ?? ''
  return `${part('year')}-${part('month')}-${part('day')}`
}

/** Parses a `YYYY-MM-DD` string into a UTC midnight `Date`. */
function parseDate(s: string): Date {
  const [y, m, d] = s.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d))
}

function formatDate(d: Date): string {
  return d.toISOString().slice(0, 10)
}

/** Adds (or subtracts) whole days to a `YYYY-MM-DD` string, in UTC. */
function addDays(s: string, delta: number): string {
  const d = parseDate(s)
  d.setUTCDate(d.getUTCDate() + delta)
  return formatDate(d)
}

/** The whole number of days from one `YYYY-MM-DD` string to another, in UTC. */
export function daysBetween(from: string, to: string): number {
  const ms = parseDate(to).getTime() - parseDate(from).getTime()
  return Math.round(ms / 86_400_000)
}

/**
 * Resolves a preset to a `{from, to}` range, both inclusive, computed from
 * "today" in the instance timezone. The multi-day presets are whole days, so
 * they end yesterday: a partial today would drag the last point down.
 * `custom` uses the given `from`/`to` verbatim, falling back to today when
 * they are missing.
 */
export function resolve(
  p: Preset,
  tz: string,
  now: Date = new Date(),
  custom?: { from: string; to: string }
): { from: string; to: string } {
  const today = todayIn(tz, now)
  switch (p) {
    case 'today':
      return { from: today, to: today }
    case 'yesterday': {
      const y = addDays(today, -1)
      return { from: y, to: y }
    }
    case '7d':
      return { from: addDays(today, -7), to: addDays(today, -1) }
    case '30d':
      return { from: addDays(today, -30), to: addDays(today, -1) }
    case '90d':
      return { from: addDays(today, -90), to: addDays(today, -1) }
    case 'custom':
      return { from: custom?.from ?? today, to: custom?.to ?? today }
  }
}
