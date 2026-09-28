import type { TreemapNode } from 'recharts'
import { Treemap as RechartsTreemap } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '@/components/ui/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface TreemapProps {
  format?: Format
}

export const contract: Contract = {
  description: 'Nested rectangles sized by value; give `parent` for two levels, e.g. browser -> version.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'label', types: ['text'] },
      { name: 'value', types: ['number'] },
      { name: 'parent', types: ['text'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

// Two levels via one flat `parent` column: the component groups rows by
// `parent`, one top-level box per distinct value, so every row here is a
// leaf (a version) whose `parent` names its browser -- no row stands for
// the browser itself, or it would double as a second, duplicate box.
export const examples: Example[] = [
  {
    title: 'Browsers and versions',
    props: { format: 'number' },
    data: {
      columns: ['label', 'value', 'parent'],
      rows: [
        ['128', '380', 'Chrome'],
        ['127', '240', 'Chrome'],
        ['17', '300', 'Safari'],
        ['16', '180', 'Safari'],
        ['129', '130', 'Firefox'],
        ['128', '80', 'Firefox'],
        ['121', '70', 'Edge'],
        ['120', '20', 'Edge'],
      ],
      truncated: false,
    },
  },
]

interface Node {
  name: string
  value?: number
  children?: Node[]
  [key: string]: unknown
}

function content({ x, y, width, height, name, depth, index }: TreemapNode & { index: number }) {
  const fill = depth === 1 ? `var(--chart-${((index ?? 0) % 5) + 1})` : 'var(--chart-1)'
  const showLabel = width > 40 && height > 16
  return (
    <g data-treemap-node data-node-name={name} data-node-depth={depth}>
      <rect
        x={x}
        y={y}
        width={width}
        height={height}
        style={{ fill, fillOpacity: depth === 1 ? 1 : 0.65 }}
        stroke="var(--background)"
      />
      {showLabel && (
        <text x={x + 4} y={y + 14} fontSize={11} fill="var(--background)">
          {name}
        </text>
      )}
    </g>
  )
}

export default function Treemap({ data, props }: WidgetProps<TreemapProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const hasParent = sql.columns.includes('parent')

  let rows: Node[]
  if (hasParent) {
    const parents: string[] = []
    const byParent = new Map<string, Node[]>()
    for (const r of records) {
      const parent = String(r.parent)
      if (!byParent.has(parent)) {
        parents.push(parent)
        byParent.set(parent, [])
      }
      byParent.get(parent)!.push({ name: String(r.label), value: Number(r.value ?? 0) })
    }
    rows = parents.map((parent) => ({ name: parent, children: byParent.get(parent) }))
  } else {
    rows = records.map((r) => ({ name: String(r.label), value: Number(r.value ?? 0) }))
  }

  const config = { value: { label: 'value', color: 'var(--chart-1)' } }

  return (
    <ChartContainer config={config} className="h-full w-full">
      <RechartsTreemap
        data={rows}
        dataKey="value"
        nameKey="name"
        isAnimationActive={false}
        content={content as never}
      >
        <ChartTooltip content={<ChartTooltipContent formatter={(v) => formatValue(Number(v), format)} />} />
      </RechartsTreemap>
    </ChartContainer>
  )
}
