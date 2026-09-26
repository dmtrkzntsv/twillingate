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

/**
 * Resolves a preset to a `{from, to}` range, both inclusive, computed from
 * "today" in the instance timezone. `custom` uses the given `from`/`to`
 * verbatim, falling back to today when they are missing.
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
      return { from: addDays(today, -6), to: today }
    case '30d':
      return { from: addDays(today, -29), to: today }
    case '90d':
      return { from: addDays(today, -89), to: today }
    case 'custom':
      return { from: custom?.from ?? today, to: custom?.to ?? today }
  }
}
