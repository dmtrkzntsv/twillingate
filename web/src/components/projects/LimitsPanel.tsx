import type { Limit } from '@/lib/api'

/** The caps in force, each with its default and what it caps; 0 reads "no cap". Set in the environment. */
export default function LimitsPanel({ limits }: { limits: Limit[] }) {
  return (
    <section aria-label="Limits" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Limits</h2>
        <p className="text-sm text-muted-foreground">
          Values kept per day before the rest fold into (other). Set in twillingate.env; 0 keeps every value.
        </p>
      </header>
      <dl className="grid gap-3 sm:grid-cols-3">
        {limits.map((l) => (
          <div key={l.setting} className="flex flex-col gap-0.5">
            <dt className="font-mono text-xs text-muted-foreground">{l.setting}</dt>
            <dd className="text-lg font-semibold">{l.value === 0 ? 'no cap' : l.value.toLocaleString()}</dd>
            <dd className="text-xs text-muted-foreground">
              default {l.default.toLocaleString()} · {l.caps}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}
