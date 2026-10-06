import type { TreemapNode } from 'recharts'
import { Treemap as RechartsTreemap } from 'recharts'
import { ChartContainer, ChartTooltip } from '@/components/ui/chart'
import { seriesColor } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { tooltip } from '@/components/chart-parts'
import { CARD_TYPE, useCardMode } from '@/components/share/card-mode'
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

/** Ink for a label on `seriesColor(i)`: the light amber slot needs dark text, the rest take white. */
function labelInk(i: number): string {
  return i % 5 === 2 ? 'oklch(0.25 0.04 250)' : 'white'
}

type Cell = TreemapNode & { index: number; parentName?: string; colorIndex?: number }

/**
 * Draws one node. A group (a `parent` with children) draws nothing of its
 * own: its leaves carry its color and name, so no label sits on another.
 */
/** A label's type: a share card's is larger, for a card seen small in a feed. */
const TYPE = { tile: { name: 12, value: 11, line: 16 }, card: { name: 21, value: CARD_TYPE, line: 26 } }

function cell(format: Format, card: boolean) {
  return function Cell({ x, y, width, height, name, depth, index, value, parentName, colorIndex }: Cell) {
    const grouped = parentName !== undefined
    if (depth !== (grouped ? 2 : 1)) return <g data-treemap-node data-node-name={name} data-node-depth={depth} />
    const color = grouped ? (colorIndex ?? 0) : index
    const label = grouped ? `${parentName} ${name}` : name
    const type = card ? TYPE.card : TYPE.tile
    // On a card nothing may run past its cell: a name too long at its size
    // steps down to the value's, and shows only where it fits (about 0.6em a character).
    const fitsAt = (size: number) => width > label.length * size * 0.6 + 16
    const nameSize = !card || fitsAt(type.name) ? type.name : type.value
    const fits = !card || fitsAt(nameSize)
    return (
      <g data-treemap-node data-node-name={name} data-node-depth={depth}>
        <rect
          x={x}
          y={y}
          width={width}
          height={height}
          rx={4}
          fill={seriesColor(color)}
          // The card's color between cells: a gap, not an outline.
          stroke="var(--card)"
          strokeWidth={2}
        />
        {width > 48 && height > type.line + 12 && fits && (
          <text x={x + 8} y={y + 6 + type.line * 0.75} fontSize={nameSize} fontWeight={500} fill={labelInk(color)}>
            {label}
          </text>
        )}
        {width > 48 && height > type.line * 2 + 14 && fits && (
          <text x={x + 8} y={y + 6 + type.line * 1.75} fontSize={type.value} fill={labelInk(color)} fillOpacity={0.8}>
            {formatValue(Number(value), format)}
          </text>
        )}
      </g>
    )
  }
}

export default function Treemap({ data, props }: WidgetProps<TreemapProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  const card = useCardMode()
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
      byParent.get(parent)!.push({
        name: String(r.label),
        value: Number(r.value ?? 0),
        parentName: parent,
        colorIndex: parents.indexOf(parent),
      })
    }
    rows = parents.map((parent) => ({ name: parent, children: byParent.get(parent) }))
  } else {
    rows = records.map((r) => ({ name: String(r.label), value: Number(r.value ?? 0) }))
  }

  const config = { value: { label: 'value' } }

  return (
    <ChartContainer config={config} className="h-full w-full">
      <RechartsTreemap
        data={rows}
        dataKey="value"
        nameKey="name"
        isAnimationActive={false}
        content={cell(format, card) as never}
      >
        <ChartTooltip content={tooltip(format)} />
      </RechartsTreemap>
    </ChartContainer>
  )
}
