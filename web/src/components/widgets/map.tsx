import { lazy } from 'react'
import type { Contract, Example } from './types'

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

// d3-geo, topojson and the world atlas load with the first map on screen.
export default lazy(() => import('./lazy/map'))
