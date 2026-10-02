import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Sankey, { colorSlots, contract, graph } from './sankey'

function renderSankey(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 500, height: 300 }}>
      <Sankey data={data} props={props} />
    </div>
  )
}

const rows = (r: string[][]): SqlData => ({ columns: ['source', 'target', 'value'], rows: r, truncated: false })

describe('sankey graph', () => {
  it('makes one node per name, so a name in two columns joins two stages', () => {
    const g = graph([
      { source: 'google.com', target: '/', value: 10 },
      { source: '/', target: 'signup', value: 4 },
    ])
    expect(g.nodes).toEqual(['google.com', '/', 'signup'])
    expect(g.links).toEqual([
      { source: 0, target: 1, value: 10 },
      { source: 1, target: 2, value: 4 },
    ])
  })

  it('sums repeated pairs', () => {
    const g = graph([
      { source: 'a', target: 'b', value: 3 },
      { source: 'a', target: 'b', value: 4 },
    ])
    expect(g.links).toEqual([{ source: 0, target: 1, value: 7 }])
  })

  it('leaves out a row that would loop back, and a self-link, and drops nodes only they named', () => {
    const g = graph([
      { source: 'a', target: 'b', value: 5 },
      { source: 'b', target: 'c', value: 5 },
      { source: 'c', target: 'a', value: 1 },
      { source: 'x', target: 'x', value: 2 },
    ])
    expect(g.nodes).toEqual(['a', 'b', 'c'])
    expect(g.links).toHaveLength(2)
    expect(g.loops).toEqual([
      { source: 'c', target: 'a' },
      { source: 'x', target: 'x' },
    ])
  })

  it('skips rows with no positive value', () => {
    const g = graph([
      { source: 'a', target: 'b', value: 0 },
      { source: 'a', target: 'c', value: null },
      { source: 'a', target: 'd', value: -1 },
      { source: 'a', target: 'e', value: 1 },
    ])
    expect(g.links).toEqual([{ source: 0, target: 1, value: 1 }])
    expect(g.nodes).toEqual(['a', 'e'])
  })
})

describe('sankey colors', () => {
  it('numbers nodes column by column, sinks in the last column', () => {
    // a -> b -> d, c -> d, a -> e: columns [a, c], [b], [d, e]
    const links = [
      { source: 0, target: 1, value: 1 },
      { source: 1, target: 3, value: 1 },
      { source: 2, target: 3, value: 1 },
      { source: 0, target: 4, value: 1 },
    ]
    expect(colorSlots(5, links)).toEqual([0, 2, 1, 3, 4])
  })
})

describe('sankey', () => {
  it('draws a node per name and a ribbon per pair', () => {
    const { container } = renderSankey(
      rows([
        ['google.com', '/', '10'],
        ['google.com', '/docs', '5'],
        ['/', 'signup', '4'],
      ])
    )
    const names = [...container.querySelectorAll('[data-sankey-node]')].map((n) => n.getAttribute('data-node-name'))
    expect(names.sort()).toEqual(['/', '/docs', 'google.com', 'signup'])
    const links = [...container.querySelectorAll('[data-sankey-link]')].map(
      (l) => `${l.getAttribute('data-link-source')}>${l.getAttribute('data-link-target')}`
    )
    expect(links.sort()).toEqual(['/>signup', 'google.com>/', 'google.com>/docs'])
  })

  it('labels each node with its name and value', () => {
    const { container } = renderSankey(rows([['google.com', '/', '1200']]), { format: 'number' })
    const label = container.querySelector('[data-node-name="google.com"] text')
    expect(label?.textContent).toContain('google.com')
    expect(label?.textContent).toContain('1,200')
  })

  it('lists the rows it left out as loops', () => {
    const { container } = renderSankey(
      rows([
        ['/', '/pricing', '5'],
        ['/pricing', '/', '2'],
      ])
    )
    expect(container.querySelector('[data-sankey-loops]')?.textContent).toContain('/pricing → /')
    expect(container.querySelectorAll('[data-sankey-link]')).toHaveLength(1)
  })

  it('shows a hover card with a ribbon’s value and shares', () => {
    const { container } = renderSankey(
      rows([
        ['a', 'b', '30'],
        ['a', 'c', '10'],
      ])
    )
    const link = container.querySelector('[data-link-target="b"]')!
    fireEvent.pointerEnter(link, { clientX: 10, clientY: 10 })
    const card = screen.getByRole('tooltip')
    expect(card.textContent).toContain('a → b')
    expect(card.textContent).toContain('Of the source')
    expect(card.textContent).toContain('75%')
    fireEvent.pointerLeave(link)
    expect(screen.queryByRole('tooltip')).toBeNull()
  })

  it('shows a node’s inflow and outflow on hover', () => {
    const { container } = renderSankey(
      rows([
        ['a', 'b', '30'],
        ['b', 'c', '20'],
      ])
    )
    fireEvent.pointerEnter(container.querySelector('[data-node-name="b"]')!, { clientX: 10, clientY: 10 })
    const card = screen.getByRole('tooltip')
    expect(card.textContent).toContain('In')
    expect(card.textContent).toContain('30')
    expect(card.textContent).toContain('Out')
    expect(card.textContent).toContain('20')
  })

  it('renders nothing for an empty result', () => {
    const { container } = renderSankey(rows([]))
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.inputs.columns.map((c) => c.name)).toEqual(['source', 'target', 'value'])
    expect(contract.defaultWidth).toBe(12)
    expect(contract.defaultHeight).toBe(8)
  })
})
