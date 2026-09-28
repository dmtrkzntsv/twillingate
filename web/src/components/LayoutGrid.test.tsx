import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import LayoutGrid from './LayoutGrid'

describe('LayoutGrid', () => {
  it('places each cell with its row span and content', () => {
    const { container, getByText } = render(
      <LayoutGrid
        cells={[
          { key: 'a', width: 6, height: 8, node: <p>first</p> },
          { key: 'b', width: 12, height: 2, node: <p>second</p> },
        ]}
      />
    )
    const grid = container.querySelector('[data-slot="widget-grid"]')!
    expect(grid.children).toHaveLength(2)
    expect((grid.children[0] as HTMLElement).style.gridRow).toBe('span 8 / span 8')
    expect((grid.children[1] as HTMLElement).style.gridRow).toBe('span 2 / span 2')
    expect(getByText('first')).toBeInTheDocument()
    expect(getByText('second')).toBeInTheDocument()
  })
})
