import { isValidRange } from './selection'
import type { Preset } from './ranges'

/**
 * The date range every dashboard and project page opens on when its URL
 * names none: one for the whole app, kept in this browser alone (the server
 * stores none). Only a dashboard page's range switcher changes it; a project
 * page's changes stay in its URL.
 */
export interface StoredRange {
  range: Preset
  from?: string
  to?: string
}

export const RANGE_KEY = 'twillingate.range'

/** What localStorage holds under RANGE_KEY, when it is a range the switcher offers; null otherwise. */
export function parseStoredRange(v: unknown): StoredRange | null {
  if (typeof v !== 'object' || v === null) return null
  const { range, from, to } = v as Record<string, unknown>
  if (typeof range !== 'string') return null
  const f = typeof from === 'string' ? from : undefined
  const t = typeof to === 'string' ? to : undefined
  if (!isValidRange(range, f, t)) return null
  return range === 'custom' ? { range, from: f, to: t } : { range }
}

/** The stored range, or null when there is none (or storage is blocked). */
export function readStoredRange(): StoredRange | null {
  try {
    const raw = localStorage.getItem(RANGE_KEY)
    return raw === null ? null : parseStoredRange(JSON.parse(raw))
  } catch {
    return null
  }
}

/** Stores `r` as the range every page opens on; a blocked or full storage keeps the old one. */
export function writeStoredRange(r: StoredRange): void {
  const value: StoredRange = r.range === 'custom' ? { range: r.range, from: r.from, to: r.to } : { range: r.range }
  try {
    localStorage.setItem(RANGE_KEY, JSON.stringify(value))
  } catch {
    // Storage full or blocked: the URL still carries the range.
  }
}
