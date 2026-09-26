import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Radial, { contract } from './radial'

function renderRadial(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 400 }}>
      <Radial data={data} props={props} />
    </div>
  )
}

describe('radial', () => {
  it('renders one ring per row', () => {
    const { container } = renderRadial({
      columns: ['label', 'value'],
      rows: [
        ['goal a', '40'],
        ['goal b', '70'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-radial-bar-sector')).toHaveLength(2)
    expect(screen.getByText('goal a')).toBeInTheDocument()
  })

  it('scales rings toward max when present (missing optional input otherwise)', () => {
    const withMax = renderRadial({
      columns: ['label', 'value', 'max'],
      rows: [['goal', '40', '80']],
      truncated: false,
    })
    const withoutMax = renderRadial({
      columns: ['label', 'value'],
      rows: [['goal', '40']],
      truncated: false,
    })
    const angle = (c: HTMLElement) => c.querySelector('.recharts-radial-bar-sector')?.getAttribute('d')
    expect(angle(withMax.container)).not.toEqual(angle(withoutMax.container))
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderRadial({ columns: ['label', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(4)
    expect(contract.defaultHeight).toBe(8)
  })
})
