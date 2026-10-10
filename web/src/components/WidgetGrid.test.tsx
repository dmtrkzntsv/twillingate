import { act, fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { WidgetActions } from '@/hooks/use-widget-actions'
import { endpoints, type Widget } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import WidgetGrid from './WidgetGrid'

// jsdom lays nothing out: the grid is as wide as each test says.
let gridPx = 1200
vi.mock('@/hooks/use-element-width', () => ({ useElementWidth: () => gridPx }))

function note(id: number, over: Partial<Widget> = {}): Widget {
  return {
    widget_id: id,
    dashboard_id: 1,
    name: `note-${id}`,
    component: 'markdown',
    title: `Note ${id}`,
    width: 6,
    height: 4,
    props: {},
    source: { type: 'md', content: 'text' },
    follows_project: false,
    follows_range: false,
    ...over,
  }
}

function actions(over: Partial<WidgetActions> = {}): WidgetActions {
  return { move: vi.fn().mockResolvedValue(true), resize: vi.fn().mockResolvedValue(true), ...over }
}

function renderGrid(widgets: Widget[], arrange?: WidgetActions) {
  return renderWithProviders(<WidgetGrid widgets={widgets} paramsFor={() => ({})} arrange={arrange} />)
}

/** The grid cell holding widget `id`'s card. */
function cell(title: string): HTMLElement {
  return screen.getByRole('button', { name: `Resize ${title}` }).closest('[data-slot="widget-cell"]') as HTMLElement
}

beforeEach(() => {
  gridPx = 1200
  vi.spyOn(endpoints, 'widgetData').mockReturnValue(new Promise(() => {}))
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('WidgetGrid', () => {
  it('only shows: no grip and no corner without arrange', () => {
    renderGrid([note(1), note(2)])
    expect(screen.queryByRole('button', { name: /^Move / })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Resize / })).toBeNull()
  })

  it('gives each card a grip and a corner on a full-width grid', () => {
    renderGrid([note(1), note(2)], actions())
    expect(screen.getByRole('button', { name: 'Move Note 1' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Resize Note 2' })).toBeInTheDocument()
  })

  it('keeps the grip but drops the corner below the full width, where the spans are narrowed', () => {
    gridPx = 900
    renderGrid([note(1)], actions())
    expect(screen.getByRole('button', { name: 'Move Note 1' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Resize / })).toBeNull()
  })

  it('resizes by the arrow keys and saves once they pause', async () => {
    vi.useFakeTimers()
    const arrange = actions()
    renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    corner.focus()
    fireEvent.keyDown(corner, { key: 'ArrowRight' })
    fireEvent.keyDown(corner, { key: 'ArrowRight' })
    fireEvent.keyDown(corner, { key: 'ArrowDown' })
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    expect(cell('Note 1').style.gridRow).toBe('span 5 / span 5')
    expect(screen.getByText('8 × 5')).toBeInTheDocument()
    expect(arrange.resize).not.toHaveBeenCalled()
    await act(() => vi.advanceTimersByTimeAsync(600))
    expect(arrange.resize).toHaveBeenCalledWith(1, { width: 8, height: 5 })
    // The new size stays while the server's is on its way.
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
  })

  it('keeps the size within the 12 by 12 grid, and Escape drops it unsaved', async () => {
    vi.useFakeTimers()
    const arrange = actions()
    renderGrid([note(1, { width: 12, height: 1 })], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.keyDown(corner, { key: 'ArrowRight' })
    fireEvent.keyDown(corner, { key: 'ArrowUp' })
    expect(screen.getByText('12 × 1')).toBeInTheDocument()
    fireEvent.keyDown(corner, { key: 'ArrowLeft' })
    fireEvent.keyDown(corner, { key: 'Escape' })
    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(arrange.resize).not.toHaveBeenCalled()
    expect(cell('Note 1').style.gridColumn).toBe('span 12 / span 12')
  })

  it('resizes by dragging the corner a column and a row at a time', async () => {
    const arrange = actions()
    renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    // A 1200px grid: a column is (1200 + 12) / 12 = 101px, a row 52px.
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 500, clientY: 300 })
    fireEvent.pointerMove(corner, { pointerId: 1, clientX: 500 + 3 * 101, clientY: 300 - 52 })
    expect(screen.getByText('9 × 3')).toBeInTheDocument()
    fireEvent.pointerMove(corner, { pointerId: 1, clientX: 500 - 40, clientY: 300 + 2 * 52 + 30 })
    expect(screen.getByText('6 × 7')).toBeInTheDocument()
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 500 - 60, clientY: 300 + 2 * 52 + 30 })
    expect(arrange.resize).toHaveBeenCalledWith(1, { width: 5, height: 7 })
  })

  it('saves nothing when the corner is let go where it started', () => {
    const arrange = actions()
    renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 500, clientY: 300 })
    fireEvent.pointerMove(corner, { pointerId: 1, clientX: 540, clientY: 310 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 520, clientY: 300 })
    expect(arrange.resize).not.toHaveBeenCalled()
  })

  it('snaps back when the server refuses the size', async () => {
    const user = userEvent.setup()
    const arrange = actions({ resize: vi.fn().mockResolvedValue(false) })
    renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    expect(arrange.resize).toHaveBeenCalledWith(1, { width: 8, height: 4 })
    await user.hover(corner) // lets the refusal settle
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')
  })

  it('shows the server size once it comes back', () => {
    const arrange = actions({ resize: vi.fn().mockReturnValue(new Promise(() => {})) })
    const { client, rerender } = renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    rerender(
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <WidgetGrid widgets={[note(1, { width: 3 })]} paramsFor={() => ({})} arrange={arrange} />
        </TooltipProvider>
      </QueryClientProvider>
    )
    expect(cell('Note 1').style.gridColumn).toBe('span 3 / span 3')
  })
})
