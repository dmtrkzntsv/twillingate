import { Bar, CartesianGrid, ComposedChart, Line, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '@/components/ui/chart'
import { activeDot, axis, formatHeading, formatTick, grid, MAX_BAR, niceTicks, seriesColor, valueAxis } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { legend } from '@/components/chart-parts'
import { CARD_TICK_GAP, useCardMode } from '@/components/share/card-mode'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface ComboProps {
  bar_format?: Format
  line_format?: Format
}

export const contract: Contract = {
  description:
    'Two related metrics on two axes over the same `x`, e.g. visitors (bar) and bounce rate (line).',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['day', 'text'] },
      { name: 'bar', types: ['number'] },
      { name: 'line', types: ['number'] },
    ],
  },
  props: {
    type: 'object',
    properties: {
      bar_format: { enum: ['number', 'percent', 'duration'] },
      line_format: { enum: ['number', 'percent', 'duration'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Visitors and bounce rate',
    props: { bar_format: 'number', line_format: 'percent' },
    data: {
      columns: ['x', 'bar', 'line'],
      rows: [
        ['2026-09-01', '240', '0.52'],
        ['2026-09-02', '255', '0.50'],
        ['2026-09-03', '260', '0.49'],
        ['2026-09-04', '275', '0.47'],
        ['2026-09-05', '300', '0.46'],
        ['2026-09-06', '290', '0.48'],
        ['2026-09-07', '310', '0.45'],
        ['2026-09-08', '320', '0.44'],
        ['2026-09-09', '335', '0.43'],
        ['2026-09-10', '340', '0.42'],
        ['2026-09-11', '355', '0.41'],
        ['2026-09-12', '360', '0.40'],
        ['2026-09-13', '375', '0.39'],
        ['2026-09-14', '390', '0.38'],
      ],
      truncated: false,
    },
  },
]

export default function Combo({ data, props }: WidgetProps<ComboProps>) {
  const records = toRecords(data as SqlData, contract)
  // Fewer x ticks on a share card, for its larger type.
  const card = useCardMode()
  if (records.length === 0) return null

  const barFormat = props.bar_format ?? 'number'
  const lineFormat = props.line_format ?? 'number'
  const config = { bar: { label: 'bar' }, line: { label: 'line' } }
  const barTicks = niceTicks(records.map((r) => Number(r.bar)))
  // The line's axis gets as many steps as the bar's, so both share one set of gridlines.
  const lineTicks = alignedTicks(
    records.map((r) => Number(r.line)),
    barTicks.length - 1
  )

  return (
    <ChartContainer config={config} className="h-full w-full">
      <ComposedChart data={records} margin={{ top: 8, right: 0, bottom: 0, left: 0 }} barCategoryGap="24%">
        <CartesianGrid {...grid} />
        <XAxis dataKey="x" {...axis} interval="preserveStartEnd" minTickGap={card ? CARD_TICK_GAP : 24} tickFormatter={formatTick} />
        <YAxis
          yAxisId="bar"
          {...valueAxis}
          ticks={barTicks}
          domain={[barTicks[0], barTicks[barTicks.length - 1]]}
          tickFormatter={(v: number) => formatValue(v, barFormat)}
        />
        <YAxis
          yAxisId="line"
          orientation="right"
          {...valueAxis}
          ticks={lineTicks}
          domain={[lineTicks[0], lineTicks[lineTicks.length - 1]]}
          tickFormatter={(v: number) => formatValue(v, lineFormat)}
        />
        <ChartTooltip
          cursor={{ fill: 'var(--muted)', opacity: 0.6 }}
          content={
            <ChartTooltipContent
              labelFormatter={(_, payload) => formatHeading(payload?.[0]?.payload?.x)}
              valueFormatter={(v, _, key) => formatValue(v, key === 'line' ? lineFormat : barFormat)}
            />
          }
        />
        {legend()}
        <Bar
          yAxisId="bar"
          dataKey="bar"
          fill={seriesColor(0)}
          // Quieter than the line, so the two read as a pair rather than a clash.
          fillOpacity={0.45}
          radius={[4, 4, 0, 0]}
          maxBarSize={MAX_BAR}
          isAnimationActive={false}
        />
        <Line
          yAxisId="line"
          type="monotone"
          dataKey="line"
          stroke={seriesColor(1)}
          strokeWidth={2}
          dot={false}
          activeDot={activeDot(seriesColor(1))}
          isAnimationActive={false}
        />
      </ComposedChart>
    </ChartContainer>
  )
}

/** Round ticks for `values` in exactly `steps` steps, to line up with another axis. */
function alignedTicks(values: number[], steps: number): number[] {
  const own = niceTicks(values, steps)
  const step = (own[own.length - 1] - own[0]) / steps
  return Array.from({ length: steps + 1 }, (_, i) => Number((own[0] + i * step).toPrecision(12)))
}
