import type { ReceivedKey } from '@/lib/api'

/** What a key received: its events and its most distinct values in a day, or "not received". `valuesCap` 0 is no cap. */
export function describeKey(r: ReceivedKey, valuesCap: number): string {
  if (!r.received) return 'not received'
  // Recorded at ingest, counted by the daily pass: a key first seen today has no counts yet.
  if (r.events === 0) return 'received today · counted tonight'
  const values =
    r.max_values === null
      ? 'values counted tonight'
      : valuesCap > 0 && r.max_values > valuesCap
        ? `${r.max_values.toLocaleString()} values · folds past ${valuesCap}`
        : `${r.max_values.toLocaleString()} ${r.max_values === 1 ? 'value' : 'values'}`
  return `${r.events.toLocaleString()} events · ${values}`
}

/** How many breakdowns all active projects declare, against ATTRIBUTE_BREAKDOWNS_MAX (0 is no limit). */
export function usedLabel(used: number, max: number): string {
  return max > 0 ? `${used} of ${max} in use` : `${used} in use`
}
