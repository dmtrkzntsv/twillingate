import { lazy } from 'react'
import type { Contract, Example } from './types'

export const contract: Contract = {
  description: 'Free-form text: a note, a caveat, instructions for reading the dashboard.',
  accepts: ['md'],
  inputs: { open: false, columns: [] },
  props: { type: 'object', properties: {}, additionalProperties: false },
  defaultWidth: 12,
  defaultHeight: 2,
}

export const examples: Example[] = [
  {
    title: 'Notes',
    props: {},
    data: {
      markdown:
        '## Weekly notes\n\nVisitors are **up 12%** week over week, mostly from `google.com`.\n\n- Pricing page redesign shipped on Sep 22\n- Docs traffic keeps growing',
    },
  },
]

// react-markdown and micromark load with the first markdown widget on screen.
export default lazy(() => import('./lazy/markdown'))
