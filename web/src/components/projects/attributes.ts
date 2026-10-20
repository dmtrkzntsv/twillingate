import type { ProjectAttribute } from '@/lib/api'

/** What a key received over the range: its events and its distinct values, or "not received". */
export function describeKey(r: ProjectAttribute): string {
  if (!r.received) return 'not received'
  return `${r.events.toLocaleString()} ${r.events === 1 ? 'event' : 'events'} · ${r.values.toLocaleString()} ${r.values === 1 ? 'value' : 'values'}`
}
