import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import WidgetSkeleton from './WidgetSkeleton'

function blocks(component: string | null, props: Record<string, unknown> = {}) {
  const { container } = render(<WidgetSkeleton widget={{ component, props }} />)
  return container.querySelectorAll('[data-slot=skeleton]')
}

describe('WidgetSkeleton', () => {
  it('announces the card as loading', () => {
    render(<WidgetSkeleton widget={{ component: 'stat', props: {} }} />)
    expect(screen.getByRole('status', { name: 'Loading' })).toBeInTheDocument()
  })

  it('takes the rough shape of its component', () => {
    expect(blocks('stat')).toHaveLength(2)
    expect(blocks('bar').length).toBeGreaterThan(5)
    expect(blocks('heatmap').length).toBeGreaterThan(50)
    expect(blocks('sankey')).toHaveLength(9)
  })

  it('draws a ring only for a donut pie', () => {
    expect((blocks('pie', { donut: true })[0] as HTMLElement).style.mask).toContain('radial-gradient')
    expect((blocks('pie')[0] as HTMLElement).style.mask).toBe('')
  })

  it('falls back to one block for a component it has no shape for', () => {
    expect(blocks('something_new')).toHaveLength(1)
    expect(blocks(null)).toHaveLength(1)
  })
})
