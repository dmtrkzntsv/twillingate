import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import worldAtlas from 'world-atlas/countries-110m.json'
import { ISO_ALPHA2_TO_NUMERIC } from '@/lib/iso-countries'
import type { SqlData, WidgetProps } from './types'
import Map, { contract } from './map'

function renderMap(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 600, height: 400 }}>
      <Map data={data} props={props} />
    </div>
  )
}

// Small islands, dependent territories and micro-states too small to get a
// shape at this resolution (world-atlas ships 110m/50m/10m; only 110m is
// used here). Computed once from countries-110m.json's actual ids against
// the full ISO 3166-1 list -- see iso-countries.ts.
const ABSENT_AT_110M = new Set([
  'AD', 'AG', 'AI', 'AS', 'AW', 'AX', 'BB', 'BH', 'BL', 'BM', 'BQ', 'BV', 'CC', 'CK', 'CV', 'CW',
  'CX', 'DM', 'FM', 'FO', 'GD', 'GF', 'GG', 'GI', 'GP', 'GS', 'GU', 'HK', 'HM', 'IM', 'IO', 'JE',
  'KI', 'KM', 'KN', 'KY', 'LC', 'LI', 'MC', 'MF', 'MH', 'MO', 'MP', 'MQ', 'MS', 'MT', 'MU', 'MV',
  'NF', 'NR', 'NU', 'PF', 'PM', 'PN', 'PW', 'RE', 'SC', 'SG', 'SH', 'SJ', 'SM', 'ST', 'SX', 'TC',
  'TK', 'TO', 'TV', 'UM', 'VA', 'VC', 'VG', 'VI', 'WF', 'WS', 'YT',
])

describe('map', () => {
  it('every alpha-2 code resolves to a feature id present in countries-110m, except the documented, absent territories', () => {
    const presentIds = new Set(
      (worldAtlas as { objects: { countries: { geometries: { id?: string }[] } } }).objects.countries
        .geometries.map((g) => g.id)
        .filter((id): id is string => Boolean(id))
    )
    for (const [code, numericId] of Object.entries(ISO_ALPHA2_TO_NUMERIC)) {
      if (ABSENT_AT_110M.has(code)) {
        expect(presentIds.has(numericId), `${code} (${numericId}) unexpectedly has a shape`).toBe(false)
      } else {
        expect(presentIds.has(numericId), `${code} (${numericId}) has no shape at 110m`).toBe(true)
      }
    }
  })

  it('renders one path per feature in the topology', () => {
    const { container } = renderMap({
      columns: ['country', 'value'],
      rows: [
        ['US', '10'],
        ['FR', '20'],
      ],
      truncated: false,
    })
    const geometries = (
      worldAtlas as { objects: { countries: { geometries: unknown[] } } }
    ).objects.countries.geometries
    expect(container.querySelectorAll('[data-country-feature]')).toHaveLength(geometries.length)
  })

  it('shades a matched country by its value scaled between the min and max, via color-mix on --chart-1', () => {
    const { container } = renderMap({
      columns: ['country', 'value'],
      rows: [
        ['US', '0'],
        ['FR', '100'],
      ],
      truncated: false,
    })
    const us = container.querySelector(`[data-country-feature][data-id="${ISO_ALPHA2_TO_NUMERIC.US}"]`)
    const fr = container.querySelector(`[data-country-feature][data-id="${ISO_ALPHA2_TO_NUMERIC.FR}"]`)
    expect((us as HTMLElement).style.fill).toContain('0%')
    expect((fr as HTMLElement).style.fill).toContain('100%')
  })

  it('lists a code that matches no shape below the map, instead of dropping it', () => {
    const { getByText } = renderMap({
      columns: ['country', 'value'],
      rows: [['ZZ', '12']],
      truncated: false,
    })
    expect(getByText(/Not on the map:/)).toHaveTextContent('Not on the map: ZZ 12')
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderMap({ columns: ['country', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
