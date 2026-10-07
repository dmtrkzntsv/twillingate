import { Sankey as RechartsSankey, type SankeyLinkProps, type SankeyNodeProps } from 'recharts'
import { ChartContainer } from '@/components/ui/chart'
import { HoverCard, useHover, type HoverRow } from '@/components/chart-parts'
import { seriesColor } from '@/lib/chart'
import { formatExact, formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { CARD_TYPE, useCardMode } from '@/components/share/card-mode'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface SankeyProps {
  format?: Format
}

export const contract: Contract = {
  description:
    'Flows from one stage to the next, ribbons sized by value; referrer -> landing page -> event. A node is its name, so a name in two columns is one node; a row that would loop back is left out.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'source', types: ['text'] },
      { name: 'target', types: ['text'] },
      { name: 'value', types: ['number'] },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 12,
  defaultHeight: 8,
}

// Three stages from two kinds of row: a landing page is a target of its
// referrers and the source of its events, under the same name, so it is
// one node in the middle column.
export const examples: Example[] = [
  {
    title: 'Referrer, landing page, event',
    props: { format: 'number' },
    data: {
      columns: ['source', 'target', 'value'],
      rows: [
        ['google.com', '/', '420'],
        ['google.com', '/docs', '260'],
        ['news.ycombinator.com', '/', '310'],
        ['news.ycombinator.com', '/pricing', '90'],
        ['(direct)', '/', '180'],
        ['(direct)', '/pricing', '140'],
        ['/', 'signup', '120'],
        ['/', 'docs_search', '60'],
        ['/pricing', 'signup', '95'],
        ['/docs', 'docs_search', '140'],
      ],
      truncated: false,
    },
  },
]

/** A name shown in a label: long paths cut short, the hover card has the rest. */
const MAX_LABEL = 28

interface Flow {
  source: number
  target: number
  value: number
}

/**
 * The graph the rows describe: one node per distinct name, in first-seen
 * order, and one link per source/target pair, repeats summed. A row whose
 * link would close a cycle (a self-link included) is set aside in `loops`:
 * Recharts lays columns out by walking links and never returns from one.
 * A row with no positive value carries nothing to draw and is skipped.
 */
export function graph(records: Record<string, string | number | null>[]) {
  const names: string[] = []
  const index = new Map<string, number>()
  const nodeOf = (name: string) => {
    let i = index.get(name)
    if (i === undefined) {
      i = names.length
      names.push(name)
      index.set(name, i)
    }
    return i
  }

  const flows: Flow[] = []
  const flowOf = new Map<string, Flow>()
  const next = new Map<number, number[]>()
  const loops: { source: string; target: string }[] = []

  const reaches = (from: number, to: number) => {
    const seen = new Set<number>()
    const stack = [from]
    while (stack.length > 0) {
      const at = stack.pop()!
      if (at === to) return true
      if (seen.has(at)) continue
      seen.add(at)
      stack.push(...(next.get(at) ?? []))
    }
    return false
  }

  for (const r of records) {
    const value = Number(r.value)
    if (!(value > 0)) continue
    const s = nodeOf(String(r.source))
    const t = nodeOf(String(r.target))
    const key = `${s}\u0000${t}`
    const known = flowOf.get(key)
    if (known) {
      known.value += value
      continue
    }
    if (reaches(t, s)) {
      loops.push({ source: names[s], target: names[t] })
      continue
    }
    const flow = { source: s, target: t, value }
    flows.push(flow)
    flowOf.set(key, flow)
    next.set(s, [...(next.get(s) ?? []), t])
  }

  // A node only a loop named has nothing to draw: drop it and renumber.
  const used = new Set(flows.flatMap((f) => [f.source, f.target]))
  const kept = names.map((_, i) => i).filter((i) => used.has(i))
  const renumber = new Map(kept.map((old, i) => [old, i]))
  return {
    nodes: kept.map((i) => names[i]),
    links: flows.map((f) => ({ source: renumber.get(f.source)!, target: renumber.get(f.target)!, value: f.value })),
    loops,
  }
}

/**
 * Each node's color slot: nodes numbered column by column, first seen
 * first, so neighbours in a column differ until the palette runs out. A
 * column is the longest path from a source, and a node with no way out
 * sits in the last one, as Recharts lays them out.
 */
export function colorSlots(count: number, links: Flow[]): number[] {
  const depth = new Array<number>(count).fill(0)
  // Relax every link until nothing moves; `graph` left no cycle, so it settles.
  for (let changed = true; changed; ) {
    changed = false
    for (const l of links) {
      if (depth[l.target] < depth[l.source] + 1) {
        depth[l.target] = depth[l.source] + 1
        changed = true
      }
    }
  }
  const last = Math.max(...depth)
  const sinks = new Set(links.map((l) => l.target).filter((t) => !links.some((l) => l.source === t)))
  const column = depth.map((d, i) => (sinks.has(i) ? last : d))
  const order = column.map((_, i) => i).sort((a, b) => column[a] - column[b] || a - b)
  const slots = new Array<number>(count)
  order.forEach((node, slot) => (slots[node] = slot))
  return slots
}

type Hovered = { kind: 'node'; index: number } | { kind: 'link'; index: number }

// Recharts hands each shape its node or link with our fields spread in;
// `i` is our own index, whatever order Recharts draws in.
type Node = { name: string; i: number }
type Link = { i: number }

export default function Sankey({ data, props }: WidgetProps<SankeyProps>) {
  const { hovered, bind } = useHover<Hovered>()
  // A share card's labels are larger, for a card seen small in a feed.
  const cardMode = useCardMode()
  const records = toRecords(data as SqlData, contract)
  const { nodes, links, loops } = graph(records)
  if (links.length === 0) return null

  const format = props.format ?? 'number'
  const slots = colorSlots(nodes.length, links)
  const colorOf = (i: number) => seriesColor(slots[i])
  const into = nodes.map((_, i) => links.filter((l) => l.target === i).reduce((a, l) => a + l.value, 0))
  const out = nodes.map((_, i) => links.filter((l) => l.source === i).reduce((a, l) => a + l.value, 0))
  // A share only of counts: a sum of percents or durations is not a whole.
  const shares = format === 'number'

  const hoveredLink = hovered?.item.kind === 'link' ? hovered.item.index : null
  const hoveredNode = hovered?.item.kind === 'node' ? hovered.item.index : null
  const lit = (l: { source: number; target: number }, i: number) =>
    hoveredLink === i || hoveredNode === l.source || hoveredNode === l.target

  function drawNode({ x, y, width, height, payload }: SankeyNodeProps) {
    const node = payload as unknown as Node
    const index = node.i
    // A last column's label sits to its left, inside the chart; every other to its right.
    const sink = out[index] === 0
    const name = node.name.length > MAX_LABEL ? `${node.name.slice(0, MAX_LABEL - 1)}…` : node.name
    return (
      <g data-sankey-node data-node-name={node.name} {...bind({ kind: 'node', index })}>
        <rect x={x} y={y} width={width} height={Math.max(height, 1)} rx={2} fill={colorOf(index)} />
        <text
          x={sink ? x - 8 : x + width + 8}
          y={y + height / 2}
          dominantBaseline="middle"
          textAnchor={sink ? 'end' : 'start'}
          fontSize={cardMode ? CARD_TYPE : 11}
          className="pointer-events-none fill-foreground"
        >
          <tspan fontWeight={500}>{name}</tspan>
          <tspan dx={6} className="fill-muted-foreground">
            {formatValue(Math.max(into[index], out[index]), format)}
          </tspan>
        </text>
      </g>
    )
  }

  function drawLink({ sourceX, targetX, sourceY, targetY, sourceControlX, targetControlX, linkWidth, payload }: SankeyLinkProps) {
    const index = (payload as unknown as Link).i
    const link = links[index]
    return (
      <path
        data-sankey-link
        data-link-source={nodes[link.source]}
        data-link-target={nodes[link.target]}
        {...bind({ kind: 'link', index })}
        d={`M${sourceX},${sourceY} C${sourceControlX},${sourceY} ${targetControlX},${targetY} ${targetX},${targetY}`}
        fill="none"
        // Each ribbon in its source's color: where a flow comes from stays readable downstream.
        stroke={colorOf(link.source)}
        strokeOpacity={lit(link, index) ? 0.55 : 0.25}
        strokeWidth={Math.max(linkWidth, 1)}
      />
    )
  }

  const rowsOf = (h: Hovered): { heading: string; rows: HoverRow[] } => {
    if (h.kind === 'link') {
      const l = links[h.index]
      const rows: HoverRow[] = [{ label: 'Value', value: formatExact(l.value, format), color: colorOf(l.source) }]
      if (shares) {
        rows.push({ label: 'Of the source', value: formatValue(l.value / out[l.source], 'percent') })
        rows.push({ label: 'Of the target', value: formatValue(l.value / into[l.target], 'percent') })
      }
      return { heading: `${nodes[l.source]} → ${nodes[l.target]}`, rows }
    }
    const i = h.index
    const rows: HoverRow[] = []
    if (into[i] > 0) rows.push({ label: 'In', value: formatExact(into[i], format) })
    if (out[i] > 0) rows.push({ label: 'Out', value: formatExact(out[i], format) })
    rows[0].color = colorOf(i)
    return { heading: nodes[i], rows }
  }
  const card = hovered && rowsOf(hovered.item)

  return (
    <div className="flex h-full w-full flex-col gap-2">
      <ChartContainer config={{ value: { label: 'value' } }} className="aspect-auto min-h-0 w-full flex-1">
        <RechartsSankey
          data={{ nodes: nodes.map((name, i) => ({ name, i })), links: links.map((l, i) => ({ ...l, i })) }}
          nodeWidth={cardMode ? 14 : 10}
          nodePadding={cardMode ? 24 : 14}
          margin={{ top: 4, right: 4, bottom: 4, left: 4 }}
          node={drawNode}
          link={drawLink}
        />
      </ChartContainer>
      {loops.length > 0 && (
        <div data-sankey-loops className="text-xs text-muted-foreground">
          Left out, as they loop back: {loops.map((l) => `${l.source} → ${l.target}`).join(', ')}
        </div>
      )}
      {card && <HoverCard at={hovered} heading={card.heading} rows={card.rows} />}
    </div>
  )
}
