import { Bar as RechartsBar, BarChart, CartesianGrid, LabelList, XAxis, YAxis } from 'recharts'
import {
  ChartContainer,
  ChartTooltip,
} from '@/components/ui/chart'
import { axis, formatTick, grid, MAX_BAR, niceTicks, seriesColor, seriesConfig, stackTotals, valueAxis, valuesOf } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { pivot, toRecords } from '@/lib/records'
import { legend, tooltip } from '@/components/chart-parts'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface BarProps {
  format?: Format
  horizontal?: boolean
  stacked?: boolean
}

export const contract: Contract = {
  description:
    'Compare values across categories; use `series` for grouped or `stacked` bars, `horizontal` for long labels.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['text', 'day'] },
      { name: 'y', types: ['number'] },
      { name: 'series', types: ['text'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
      horizontal: { type: 'boolean' },
      stacked: { type: 'boolean' },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Daily active users, new and returning',
    props: { stacked: true },
    data: {
      columns: ['x', 'series', 'y'],
      rows: [
        ['2026-09-01', 'new', '85'],
        ['2026-09-01', 'returning', '210'],
        ['2026-09-02', 'new', '92'],
        ['2026-09-02', 'returning', '225'],
        ['2026-09-03', 'new', '78'],
        ['2026-09-03', 'returning', '230'],
        ['2026-09-04', 'new', '104'],
        ['2026-09-04', 'returning', '240'],
        ['2026-09-05', 'new', '112'],
        ['2026-09-05', 'returning', '255'],
        ['2026-09-06', 'new', '95'],
        ['2026-09-06', 'returning', '220'],
        ['2026-09-07', 'new', '120'],
        ['2026-09-07', 'returning', '260'],
      ],
      truncated: false,
    },
  },
  {
    title: 'Top product events',
    props: { horizontal: true },
    data: {
      columns: ['x', 'y'],
      rows: [
        ['page_view', '3200'],
        ['search', '610'],
        ['signup', '480'],
        ['checkout_started', '260'],
        ['checkout_completed', '150'],
        ['share', '95'],
      ],
      truncated: false,
    },
  },
]

/** At most this many bars of one series, each also prints its value. */
const LABELLED = 12

/** Room for the longest category label on a horizontal chart's axis, within reason. */
function categoryWidth(labels: string[]): number {
  const longest = Math.max(...labels.map((l) => l.length))
  return Math.min(160, Math.max(40, Math.round(longest * 6.4) + 8))
}

export default function Bar({ data, props }: WidgetProps<BarProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const horizontal = props.horizontal ?? false
  const stacked = props.stacked ?? false
  const hasSeries = sql.columns.includes('series')
  const { rows, keys } = hasSeries ? pivot(records, 'x', 'series', 'y') : { rows: records, keys: ['y'] }
  const config = hasSeries ? seriesConfig(keys) : { y: { label: 'y' } }
  const ticks = niceTicks(stacked ? stackTotals(rows, keys) : valuesOf(rows, keys))
  const labelled = keys.length === 1 && rows.length <= LABELLED
  const show = (v: unknown) => formatValue(Number(v), format)
  // Rounded at the data end only; in a stack, only the outermost segment.
  const end: [number, number, number, number] = horizontal ? [0, 4, 4, 0] : [4, 4, 0, 0]
  const radius = (i: number) => (!stacked || i === keys.length - 1 ? end : 0)

  return (
    <ChartContainer config={config} className="h-full w-full">
      <BarChart
        data={rows}
        layout={horizontal ? 'vertical' : 'horizontal'}
        margin={{ top: labelled && !horizontal ? 20 : 8, right: labelled && horizontal ? 48 : 8, bottom: 0, left: 0 }}
        barGap={3}
        barCategoryGap="24%"
      >
        {horizontal ? (
          <>
            <XAxis
              type="number"
              hide={labelled}
              {...axis}
              ticks={ticks}
              domain={[ticks[0], ticks[ticks.length - 1]]}
              tickFormatter={show}
            />
            <YAxis
              dataKey="x"
              type="category"
              {...axis}
              width={categoryWidth(rows.map((r) => formatTick(r.x)))}
              tickFormatter={formatTick}
              interval={0}
            />
            {!labelled && <CartesianGrid vertical horizontal={false} />}
          </>
        ) : (
          <>
            <CartesianGrid {...grid} />
            <XAxis dataKey="x" {...axis} interval="preserveStartEnd" minTickGap={16} tickFormatter={formatTick} />
            <YAxis {...valueAxis} ticks={ticks} domain={[ticks[0], ticks[ticks.length - 1]]} tickFormatter={show} />
          </>
        )}
        <ChartTooltip
          cursor={{ fill: 'var(--muted)', opacity: 0.6 }}
          content={tooltip(format, { heading: 'x', total: stacked })}
        />
        {hasSeries && legend()}
        {keys.map((key, i) => (
          <RechartsBar
            key={key}
            dataKey={key}
            stackId={stacked ? 'stack' : undefined}
            fill={seriesColor(i)}
            radius={radius(i)}
            maxBarSize={MAX_BAR}
            // A hairline in the card's color keeps stacked segments apart.
            stroke={stacked ? 'var(--card)' : undefined}
            strokeWidth={stacked ? 1.5 : 0}
            isAnimationActive={false}
          >
            {labelled && (
              <LabelList
                dataKey={key}
                position={horizontal ? 'right' : 'top'}
                offset={8}
                fontSize={11}
                className="fill-muted-foreground"
                formatter={show}
              />
            )}
          </RechartsBar>
        ))}
      </BarChart>
    </ChartContainer>
  )
}
