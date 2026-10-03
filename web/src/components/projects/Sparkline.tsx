import { Area, AreaChart, ResponsiveContainer } from 'recharts'
import type { StatsDay } from '@/lib/api'

/** Events per day, every family summed, as a small area with no axes. */
export default function Sparkline({ series }: { series: StatsDay[] }) {
  const data = series.map((d) => ({ day: d.day, n: d.views + d.events + d.measures }))
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
