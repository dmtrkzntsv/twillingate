import type { Limit } from '@/lib/api'

const GROUPS: { group: Limit['group']; title: string; note: string; cols: string }[] = [
  { group: 'retention', title: 'Retention', note: 'How long data is kept.', cols: 'sm:grid-cols-3' },
  { group: 'caps', title: 'Caps', note: 'Values kept per day before the rest fold into (other).', cols: 'sm:grid-cols-3' },
  { group: 'ingest', title: 'Ingest', note: 'Fixed by the wire format, the same on every server.', cols: 'sm:grid-cols-2 lg:grid-cols-4' },
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

/** The limits in force by group: retention and caps (set in the environment, each with its default), then the fixed ingest limits. */
export default function LimitsPanel({ limits }: { limits: Limit[] }) {
  return (
    <section aria-label="Limits" className="flex flex-col gap-4 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Limits</h2>
        <p className="text-sm text-muted-foreground">Retention and caps are set in twillingate.env.</p>
      </header>
      {GROUPS.map(({ group, title, note, cols }) => {
        const items = limits.filter((l) => l.group === group)
        if (items.length === 0) return null
        return (
          <section key={group} aria-label={title} className="flex flex-col gap-2">
            <h3 className="text-sm font-medium">
              {title} <span className="font-normal text-muted-foreground">· {note}</span>
            </h3>
            <dl className={`grid gap-3 ${cols}`}>
              {items.map((l) => (
                <div key={l.name} className="flex flex-col gap-0.5">
                  <dt className="text-xs text-muted-foreground">{l.name}</dt>
                  <dd className="text-lg font-semibold">{formatLimit(l)}</dd>
                  <dd className="text-xs text-muted-foreground">{l.description}</dd>
                  {l.setting && (
                    <dd className="text-xs text-muted-foreground">
                      <span className="font-mono">{l.setting}</span>
                      {l.default !== undefined && ` · default ${formatLimit(l, l.default)}`}
                    </dd>
                  )}
                </div>
              ))}
            </dl>
          </section>
        )
      })}
    </section>
  )
}
