import { Line, LineChart, ResponsiveContainer, YAxis } from 'recharts'
import type { Alert } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  /** The alert's newest evaluations, newest first, as list_alerts answers them. */
  recent: Alert['recent']
  className?: string
}

/**
 * An alert's recent values, oldest to newest, as a small line with no axes,
 * a red dot on each breached evaluation. Hidden from screen readers: the
 * row says the last value and the state. Draws nothing with fewer than two
 * values to join.
 */
export default function AlertSparkline({ recent, className }: Props) {
  // Reversed into a copy: `recent` is the query cache's.
  const data = [...recent].reverse().map((e) => ({ value: e.value, breached: e.outcome === 'breached' }))
  if (data.filter((d) => d.value !== null).length < 2) return null
  return (
    <div className={cn('h-8', className)} aria-hidden>
      <ResponsiveContainer>
        <LineChart data={data} margin={{ top: 3, right: 3, bottom: 3, left: 3 }}>
          {/* Its own range, not from zero: the sparkline is for the shape. */}
          <YAxis hide domain={['dataMin', 'dataMax']} />
          <Line
            type="monotone"
            dataKey="value"
            stroke="var(--chart-1)"
            strokeWidth={1.5}
            connectNulls
            isAnimationActive={false}
            dot={(p: { index: number; cx?: number; cy?: number }) =>
              data[p.index]?.breached && data[p.index].value !== null && p.cx !== undefined && p.cy !== undefined ? (
                <circle key={p.index} data-breached cx={p.cx} cy={p.cy} r={2} fill="var(--destructive)" stroke="none" />
              ) : (
                <g key={p.index} />
              )
            }
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  )
}
