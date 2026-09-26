export type Format = 'number' | 'percent' | 'duration'

const LOCALE = 'en-US'

function formatNumber(v: number): string {
  if (Math.abs(v) >= 10000) {
    return new Intl.NumberFormat(LOCALE, {
      notation: 'compact',
      maximumFractionDigits: 1,
    }).format(v)
  }
  return new Intl.NumberFormat(LOCALE).format(v)
}

function formatPercent(v: number): string {
  return `${new Intl.NumberFormat(LOCALE, { maximumFractionDigits: 1 }).format(v * 100)}%`
}

function formatDuration(v: number): string {
  const total = Math.round(v)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const seconds = total % 60
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${seconds}s`
  return `${seconds}s`
}

/**
 * Renders a value the way a widget shows it: grouped or compact numbers,
 * fractions as percentages, seconds as a short duration. `null` (and any
 * non-finite value, e.g. a division by zero) renders as an em dash.
 */
export function formatValue(v: number | null, f: Format = 'number'): string {
  if (v === null || !Number.isFinite(v)) return '–'
  switch (f) {
    case 'percent':
      return formatPercent(v)
    case 'duration':
      return formatDuration(v)
    default:
      return formatNumber(v)
  }
}
