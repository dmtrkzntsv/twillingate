import type { Limit, RawEvents } from '@/lib/api'

const GROUPS: { group: Limit['group']; title: string; note: string }[] = [
  { group: 'retention', title: 'Retention', note: 'How long data is kept.' },
  { group: 'caps', title: 'Caps', note: 'Values kept per day before the rest fold into (other).' },
  { group: 'ingest', title: 'Ingest', note: 'Fixed by the wire format, the same on every server.' },
]

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`

/** A limit's value in its unit: 30 days, 256 KiB, 5 min, 1e15; 0 reads as what it means (no cap, kept forever). */
export function formatLimit(l: Pick<Limit, 'value' | 'unit' | 'zero'>, value = l.value): string {
  if (value === 0 && l.zero) return l.zero
  switch (l.unit) {
    case 'days':
      return plural(value, 'day', 'days')
    case 'characters':
      return plural(value, 'character', 'characters')
    case 'bytes':
      return value % 1024 === 0 ? `${(value / 1024).toLocaleString()} KiB` : plural(value, 'byte', 'bytes')
    case 'seconds':
      return value % 60 === 0 ? `${value / 60} min` : `${value} s`
  }
  if (value >= 1e9) return value.toExponential().replace('e+', 'e')
  return Number.isInteger(value) ? value.toLocaleString() : String(value)
}

/**
 * The limits in force by group: retention and caps (set in the environment, each with its default), then the fixed
 * ingest limits; and, when the server could count them, the raw events it holds in its raw window.
 */
export default function LimitsPanel({ limits, rawEvents }: { limits: Limit[]; rawEvents?: RawEvents }) {
  return (
    <section aria-label="Limits" className="flex flex-col gap-4 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Limits</h2>
        <p className="text-sm text-muted-foreground">Retention and caps are set in twillingate.env.</p>
        {rawEvents && (
          <p className="text-sm">
            Raw events held: {rawEvents.held.toLocaleString()} (the last{' '}
            {rawEvents.window_days === 1 ? 'day' : `${rawEvents.window_days.toLocaleString()} days`})
          </p>
        )}
      </header>
      {GROUPS.map(({ group, title, note }) => {
        const items = limits.filter((l) => l.group === group)
        if (items.length === 0) return null
        return (
          <section key={group} aria-label={title} className="flex flex-col gap-3 border-t pt-4">
            <header className="flex flex-col gap-0.5 border-l-2 border-primary pl-2">
              <h3 className="text-sm font-semibold uppercase tracking-wide">{title}</h3>
              <p className="text-xs text-muted-foreground">{note}</p>
            </header>
            <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-3">
              {items.map((l) => (
                <div key={l.name} className="flex flex-col gap-0.5">
                  <dt className="text-xs text-muted-foreground">{l.name}</dt>
                  <dd className="text-lg font-semibold">{formatLimit(l)}</dd>
                  {l.setting && (
                    <dd className="text-xs text-muted-foreground">
                      <span className="font-mono break-all">{l.setting}</span>
                      {l.default !== undefined && ` · default ${formatLimit(l, l.default)}`}
                    </dd>
                  )}
                  <dd className="text-xs text-muted-foreground">{l.description}</dd>
                </div>
              ))}
            </dl>
          </section>
        )
      })}
    </section>
  )
}
