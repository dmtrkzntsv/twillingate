import { geoEqualEarth, geoPath } from 'd3-geo'
import type { Feature, FeatureCollection, Geometry } from 'geojson'
import { feature } from 'topojson-client'
import type { GeometryCollection, Topology } from 'topojson-specification'
import worldAtlas from 'world-atlas/countries-110m.json'
import { ISO_ALPHA2_TO_NUMERIC } from '@/lib/iso-countries'
import { formatExact, formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { ramp } from '@/lib/chart'
import { HoverCard, ScaleLegend, useHover, type HoverRow } from '@/components/chart-parts'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface MapProps {
  format?: Format
}

export const contract: Contract = {
  description: 'Countries shaded by value, ISO alpha-2; unmatched codes are listed below the map, not dropped.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'country', types: ['text'] },
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
  defaultWidth: 6,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Visitors by country',
    props: { format: 'number' },
    data: {
      columns: ['country', 'value'],
      rows: [
        ['US', '1240'],
        ['DE', '380'],
        ['GB', '410'],
        ['FR', '290'],
        ['IN', '520'],
        ['BR', '310'],
        ['CA', '260'],
        ['NL', '150'],
        ['JP', '340'],
        ['AU', '180'],
      ],
      truncated: false,
    },
  },
]

const WIDTH = 960
const HEIGHT = 500

const topology = worldAtlas as unknown as Topology<{ countries: GeometryCollection }>
const countries = feature(topology, topology.objects.countries) as unknown as FeatureCollection<
  Geometry,
  { name?: string }
>
const projection = geoEqualEarth().fitSize([WIDTH, HEIGHT], countries)
const path = geoPath(projection)

export default function MapWidget({ data, props }: WidgetProps<MapProps>) {
  const { hovered, bind } = useHover<{ name: string; value?: number }>()
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const valueById = new Map<string, number>()
  const unmatched: { code: string; value: number }[] = []

  for (const r of records) {
    const code = String(r.country)
    const value = Number(r.value ?? 0)
    const numericId = ISO_ALPHA2_TO_NUMERIC[code]
    const hasShape = numericId !== undefined && countries.features.some((f) => f.id === numericId)
    if (hasShape) {
      valueById.set(numericId, value)
    } else {
      unmatched.push({ code, value })
    }
  }

  const matched = [...valueById.values()]
  const min = matched.length ? Math.min(...matched) : 0
  const max = matched.length ? Math.max(...matched) : 0
  const span = max - min || 1
  const fillOf = (value: number | undefined) =>
    value === undefined ? 'var(--muted)' : ramp((value - min) / span, 'var(--muted)')
  // A share only of counts, and only of a whole result: a cut one has no total.
  const sum = records.reduce((a, r) => a + Number(r.value ?? 0), 0)
  const shares = format === 'number' && !(data as SqlData).truncated && sum > 0

  const rowsOf = (value: number | undefined): HoverRow[] =>
    value === undefined
      ? [{ label: 'No data', value: '' }]
      : [
          { label: 'Value', value: formatExact(value, format), color: fillOf(value) },
          ...(shares ? [{ label: 'Share of total', value: formatValue(value / sum, 'percent') }] : []),
        ]

  return (
    <div className="flex h-full w-full flex-col gap-2 overflow-auto p-1">
      <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} className="w-full flex-1" preserveAspectRatio="xMidYMid meet">
        {countries.features.map((f: Feature<Geometry, { name?: string }>, i: number) => {
          const id = f.id === undefined ? undefined : String(f.id)
          const value = id ? valueById.get(id) : undefined
          return (
            <path
              key={id ?? f.properties?.name ?? i}
              d={path(f) ?? undefined}
              data-country-feature
              data-id={id}
              {...bind({ name: f.properties?.name ?? id ?? '', value })}
              // The card's color between countries: borders read as gaps, not ink.
              stroke="var(--card)"
              strokeWidth={0.6}
              className="hover:stroke-foreground/70"
              style={{ fill: fillOf(value) }}
            />
          )
        })}
      </svg>
      {matched.length > 0 && (
        <ScaleLegend min={formatValue(min, format)} max={formatValue(max, format)} base="var(--muted)" />
      )}
      {unmatched.length > 0 && (
        <div data-not-on-map className="text-xs text-muted-foreground">
          Not on the map: {unmatched.map((u) => `${u.code} ${formatValue(u.value, format)}`).join(', ')}
        </div>
      )}
      {hovered && <HoverCard at={hovered} heading={hovered.item.name} rows={rowsOf(hovered.item.value)} />}
    </div>
  )
}
