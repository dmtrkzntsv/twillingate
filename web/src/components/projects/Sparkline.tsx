import { Area, AreaChart, ResponsiveContainer } from 'recharts'
import type { UsageDay } from '@/lib/api'
import { eventCount } from '@/lib/units'

/** Events per day (`eventCount`), as a small area with no axes. */
export default function Sparkline({ series }: { series: UsageDay[] }) {
  const data = series.map((d) => ({ day: d.day, n: eventCount(d) }))
  return (
    <div className="h-10 w-full" aria-hidden>
      <ResponsiveContainer>
        <AreaChart data={data} margin={{ top: 2, right: 0, bottom: 0, left: 0 }}>
          <Area type="monotone" dataKey="n" stroke="var(--chart-1)" fill="var(--chart-1)" fillOpacity={0.15} strokeWidth={1.5} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}
