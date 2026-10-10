import type { ReactElement } from 'react'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { WidgetActions, WidgetSize } from '@/hooks/use-widget-actions'
import { endpoints, type Widget, type WidgetData, type WidgetDataQuery } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import WidgetGrid from './WidgetGrid'

// jsdom lays nothing out: the grid is as wide as each test says.
let gridPx = 1200
vi.mock('@/hooks/use-element-width', () => ({ useElementWidth: () => gridPx }))

// Each stat card's body renders, by its stateKey: what a drag must leave alone.
const statRenders = vi.hoisted(() => new Map<string, number>())
vi.mock('@/components/widgets/stat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/widgets/stat')>()),
  default: ({ stateKey }: { stateKey: string }) => {
    statRenders.set(stateKey, (statRenders.get(stateKey) ?? 0) + 1)
    return <div data-testid="stat-body" />
  },
}))

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

function stat(id: number): Widget {
  return note(id, {
    name: `stat-${id}`,
    component: 'stat',
    title: `Stat ${id}`,
    source: { type: 'sql', content: 'SELECT 1 AS value' },
  })
}

function statAnswer(id: number): WidgetData {
  return {
    widget_id: id,
    source_type: 'sql',
    removed: false,
    data: { columns: ['value'], rows: [['1']], truncated: false },
  }
}

/** Saves that the server takes, its size then the one asked for. */
function actions(over: Partial<WidgetActions> = {}): WidgetActions {
  return {
    move: vi.fn().mockResolvedValue(true),
    resize: vi.fn(async (_dashboardId: number, _id: number, size: WidgetSize) => size),
    ...over,
  }
}

/** The server's size after a save: `width` columns by the notes' 4 rows. */
const wide = (width: number): WidgetSize => ({ width, height: 4 })

function renderGrid(widgets: Widget[], arrange?: WidgetActions) {
  return renderWithProviders(<WidgetGrid widgets={widgets} paramsFor={() => ({})} arrange={arrange} />)
}

/** The grid cell holding widget `id`'s card. */
function cell(title: string): HTMLElement {
  return screen.getByRole('button', { name: `Resize ${title}` }).closest('[data-slot="widget-cell"]') as HTMLElement
}

beforeEach(() => {
  gridPx = 1200
  statRenders.clear()
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
    gridPx = 899
    renderGrid([note(1)], actions())
    expect(screen.getByRole('button', { name: 'Move Note 1' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Resize / })).toBeNull()
  })

  it('resizes by the arrow keys and saves once they pause', async () => {
    vi.useFakeTimers()
    const arrange = actions({ resize: vi.fn().mockReturnValue(new Promise(() => {})) })
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
    expect(arrange.resize).toHaveBeenCalledWith(1, 1, { width: 8, height: 5 })
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

  it('announces the size while it is being chosen, and clears it on Escape', () => {
    renderGrid([note(1)], actions())
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    // The card's loading skeleton is a status too, with no text of its own.
    const announced = () =>
      within(cell('Note 1'))
        .getAllByRole('status')
        .map((el) => el.textContent)
        .join('')
    expect(announced()).toBe('')
    fireEvent.keyDown(corner, { key: 'ArrowRight' })
    expect(announced()).toBe('Note 1: 7 of 12 columns wide, 4 rows tall')
    fireEvent.keyDown(corner, { key: 'Escape' })
    expect(announced()).toBe('')
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
    expect(arrange.resize).toHaveBeenCalledWith(1, 1, { width: 5, height: 7 })
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

  it('sends a resize back to the stored size while another is on its way', () => {
    const arrange = actions({ resize: vi.fn().mockReturnValue(new Promise(() => {})) })
    renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    // 6 to 8 wide, then, before the server's 8 is back, to 6 again.
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    fireEvent.pointerDown(corner, { button: 0, pointerId: 2, clientX: 202, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 2, clientX: 0, clientY: 0 })
    expect(arrange.resize).toHaveBeenCalledTimes(2)
    expect(arrange.resize).toHaveBeenLastCalledWith(1, 1, { width: 6, height: 4 })
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')
    // Let go where it was picked up: the size shown is unchanged, nothing is sent.
    fireEvent.pointerDown(corner, { button: 0, pointerId: 3, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 3, clientX: 0, clientY: 0 })
    expect(arrange.resize).toHaveBeenCalledTimes(2)
  })

  it('drops the size being chosen when the corner goes mid-resize', () => {
    const arrange = actions()
    const { client, rerender } = renderGrid([note(1)], arrange)
    const again = () =>
      rerender(
        <QueryClientProvider client={client}>
          <TooltipProvider>
            <WidgetGrid widgets={[note(1)]} paramsFor={() => ({})} arrange={arrange} />
          </TooltipProvider>
        </QueryClientProvider>
      )
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 500, clientY: 300 })
    fireEvent.pointerMove(corner, { pointerId: 1, clientX: 500 + 3 * 101, clientY: 300 - 52 })
    expect(screen.getByText('9 × 3')).toBeInTheDocument()

    // The grid narrows below the full width while the corner is held.
    gridPx = 899
    again()
    expect(screen.queryByRole('button', { name: /^Resize / })).toBeNull()
    expect(screen.queryByText('9 × 3')).toBeNull()
    const moving = screen.getByRole('button', { name: 'Move Note 1' })
    expect((moving.closest('[data-slot="widget-cell"]') as HTMLElement).className).not.toContain('ring-2')

    // Back at the full width, the card has its stored size and a fresh corner.
    gridPx = 1200
    again()
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')
    expect(screen.queryByText(/ × /)).toBeNull()
    expect(arrange.resize).not.toHaveBeenCalled()
  })

  it('drops the keys pressed when the corner goes before they are saved', async () => {
    vi.useFakeTimers()
    const arrange = actions()
    const { client, rerender } = renderGrid([note(1)], arrange)
    fireEvent.keyDown(screen.getByRole('button', { name: 'Resize Note 1' }), { key: 'ArrowRight' })
    expect(screen.getByText('7 × 4')).toBeInTheDocument()
    gridPx = 899
    rerender(
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <WidgetGrid widgets={[note(1)]} paramsFor={() => ({})} arrange={arrange} />
        </TooltipProvider>
      </QueryClientProvider>
    )
    expect(screen.queryByText('7 × 4')).toBeNull()
    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(arrange.resize).not.toHaveBeenCalled()
  })

  it('snaps back when the server refuses the size', async () => {
    const user = userEvent.setup()
    const arrange = actions({ resize: vi.fn().mockResolvedValue(null) })
    renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    expect(arrange.resize).toHaveBeenCalledWith(1, 1, { width: 8, height: 4 })
    await user.hover(corner) // lets the refusal settle
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')
  })

  it('keeps the last size asked for while older saves come back with theirs', async () => {
    // Two saves in flight: 6 to 8, then back to 6.
    const settle: ((size: WidgetSize | null) => void)[] = []
    const resize = vi.fn(() => new Promise<WidgetSize | null>((resolve) => settle.push(resolve)))
    const arrange = actions({ resize })
    const { client, rerender } = renderGrid([note(1)], arrange)
    const serverHas = (width: number) =>
      rerender(
        <QueryClientProvider client={client}>
          <TooltipProvider>
            <WidgetGrid widgets={[note(1, { width })]} paramsFor={() => ({})} arrange={arrange} />
          </TooltipProvider>
        </QueryClientProvider>
      )
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 2, clientX: 202, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 2, clientX: 0, clientY: 0 })
    expect(resize).toHaveBeenCalledTimes(2)
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')

    // The first save's refetch lands with 8, and the first resolves: the
    // second is still on its way, so 8 must not show.
    serverHas(8)
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')
    await act(async () => settle[0](wide(8)))
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')

    // The second one's refetch lands with 6, and it resolves.
    serverHas(6)
    await act(async () => settle[1](wide(6)))
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')

    // Nothing is held over the server's size any more.
    serverHas(3)
    expect(cell('Note 1').style.gridColumn).toBe('span 3 / span 3')
  })

  it('keeps the size asked for between the save resolving and the new props arriving', async () => {
    // The hook resolves after the refetch, but the cache hands the page its
    // new widgets a tick later: the old size must not show in between.
    let settle: (size: WidgetSize | null) => void = () => {}
    const arrange = actions({ resize: vi.fn(() => new Promise<WidgetSize | null>((resolve) => (settle = resolve))) })
    const { client, rerender } = renderGrid([note(1)], arrange)
    const serverHas = (width: number) =>
      rerender(
        <QueryClientProvider client={client}>
          <TooltipProvider>
            <WidgetGrid widgets={[note(1, { width })]} paramsFor={() => ({})} arrange={arrange} />
          </TooltipProvider>
        </QueryClientProvider>
      )
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    await act(async () => settle(wide(8)))
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    serverHas(8)
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    // Nothing is held any more: a later size from the server shows.
    serverHas(3)
    expect(cell('Note 1').style.gridColumn).toBe('span 3 / span 3')
  })

  it('shows the server size when it differs from the one asked for, before and after it reaches the props', async () => {
    let settle: (size: WidgetSize | null) => void = () => {}
    const arrange = actions({ resize: vi.fn(() => new Promise<WidgetSize | null>((resolve) => (settle = resolve))) })
    const { client, rerender } = renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    // Another writer set 5 before the save's refetch: the save resolves
    // with 5 while the props still carry 6, and 5 shows at once.
    await act(async () => settle(wide(5)))
    expect(cell('Note 1').style.gridColumn).toBe('span 5 / span 5')
    rerender(
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <WidgetGrid widgets={[note(1, { width: 5 })]} paramsFor={() => ({})} arrange={arrange} />
        </TooltipProvider>
      </QueryClientProvider>
    )
    expect(cell('Note 1').style.gridColumn).toBe('span 5 / span 5')
  })

  it('shows the stored size when another writer put it back before the refetch', async () => {
    // 6 to 8, then an agent sets 6 again before the save's refetch: the
    // refetch brings 6, so the props' widget does not change at all.
    let settle: (size: WidgetSize | null) => void = () => {}
    const arrange = actions({ resize: vi.fn(() => new Promise<WidgetSize | null>((resolve) => (settle = resolve))) })
    renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    await act(async () => settle(wide(6)))
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')
  })

  it('keeps the newest size when an older save settles after it', async () => {
    const settle: ((size: WidgetSize | null) => void)[] = []
    const resize = vi.fn(() => new Promise<WidgetSize | null>((resolve) => settle.push(resolve)))
    const arrange = actions({ resize })
    const { client, rerender } = renderGrid([note(1)], arrange)
    const serverHas = (width: number) =>
      rerender(
        <QueryClientProvider client={client}>
          <TooltipProvider>
            <WidgetGrid widgets={[note(1, { width })]} paramsFor={() => ({})} arrange={arrange} />
          </TooltipProvider>
        </QueryClientProvider>
      )
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 2, clientX: 202, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 2, clientX: 303, clientY: 0 })
    // The newer one (9) settles first, while the props still carry 6, so it
    // is still held when the older one (8) settles after it with its own.
    await act(async () => settle[1](wide(9)))
    expect(cell('Note 1').style.gridColumn).toBe('span 9 / span 9')
    await act(async () => settle[0](wide(8)))
    expect(cell('Note 1').style.gridColumn).toBe('span 9 / span 9')
    serverHas(9)
    expect(cell('Note 1').style.gridColumn).toBe('span 9 / span 9')
    serverHas(3)
    expect(cell('Note 1').style.gridColumn).toBe('span 3 / span 3')
  })

  it('snaps back at once when the newest save is refused, whatever the older one does', async () => {
    const settle: ((size: WidgetSize | null) => void)[] = []
    const resize = vi.fn(() => new Promise<WidgetSize | null>((resolve) => settle.push(resolve)))
    renderGrid([note(1)], actions({ resize }))
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 2, clientX: 202, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 2, clientX: 303, clientY: 0 })
    expect(cell('Note 1').style.gridColumn).toBe('span 9 / span 9')
    await act(async () => settle[1](null))
    expect(cell('Note 1').style.gridColumn).toBe('span 6 / span 6')
  })

  it('shows the server size once the save and its refetch are done', async () => {
    let settle: (size: WidgetSize | null) => void = () => {}
    const arrange = actions({ resize: vi.fn(() => new Promise<WidgetSize | null>((resolve) => (settle = resolve))) })
    const { client, rerender } = renderGrid([note(1)], arrange)
    const corner = screen.getByRole('button', { name: 'Resize Note 1' })
    fireEvent.pointerDown(corner, { button: 0, pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerUp(corner, { pointerId: 1, clientX: 202, clientY: 0 })
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    // The size asked for stays while the save is on its way, whatever
    // else the props carry.
    rerender(
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <WidgetGrid widgets={[note(1, { width: 3 })]} paramsFor={() => ({})} arrange={arrange} />
        </TooltipProvider>
      </QueryClientProvider>
    )
    expect(cell('Note 1').style.gridColumn).toBe('span 8 / span 8')
    // Resolved with the server's size, which is neither the one asked for
    // nor the one the props carry: it shows at once, and stays when the
    // props bring it.
    await act(async () => settle(wide(5)))
    expect(cell('Note 1').style.gridColumn).toBe('span 5 / span 5')
    rerender(
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <WidgetGrid widgets={[note(1, { width: 5 })]} paramsFor={() => ({})} arrange={arrange} />
        </TooltipProvider>
      </QueryClientProvider>
    )
    expect(cell('Note 1').style.gridColumn).toBe('span 5 / span 5')
  })

  it('redraws no card body while a card is picked up, moved and put back', async () => {
    vi.spyOn(endpoints, 'widgetData').mockImplementation(async (id) => statAnswer(id))
    renderGrid([stat(1), stat(2)], actions())
    await waitFor(() => expect(screen.getAllByTestId('stat-body')).toHaveLength(2))
    const before = new Map(statRenders)

    // Every sortable cell redraws on each change of dnd-kit's context: the
    // pick-up, each step and the drop. The cards' bodies must not.
    const grip = screen.getByRole('button', { name: 'Move Stat 1' })
    grip.focus()
    await userEvent.keyboard(' ')
    await waitFor(() => expect(grip.closest('[data-slot="widget-cell"]')).toHaveClass('opacity-40'))
    await userEvent.keyboard('{ArrowRight}')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(grip.closest('[data-slot="widget-cell"]')).not.toHaveClass('opacity-40'))

    expect(statRenders).toEqual(before)
  })

  it('redraws no card body when the grid redraws with the same parameters, and each one whose parameters change', async () => {
    const spy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (id) => statAnswer(id))
    const widgets = [stat(1), { ...stat(2), follows_project: true }]
    // One function per selection, as the pages' useCallback makes it, and
    // a new object per call, as their widgetParams does.
    const for7 = (w: Widget): WidgetDataQuery => (w.follows_project ? { project_id: 7 } : {})
    const for8 = (w: Widget): WidgetDataQuery => (w.follows_project ? { project_id: 8 } : {})
    const arrange = actions()
    const grid = (project: number) => (
      <WidgetGrid widgets={[...widgets]} paramsFor={project === 7 ? for7 : for8} arrange={arrange} />
    )
    const { client, rerender } = renderWithProviders(grid(7))
    const wrap = (ui: ReactElement) => (
      <QueryClientProvider client={client}>
        <TooltipProvider>{ui}</TooltipProvider>
      </QueryClientProvider>
    )
    await waitFor(() => expect(screen.getAllByTestId('stat-body')).toHaveLength(2))
    const before = new Map(statRenders)

    rerender(wrap(grid(7)))
    expect(statRenders).toEqual(before)

    // Another project: only the card that follows it asks again.
    rerender(wrap(grid(8)))
    expect(spy).toHaveBeenCalledWith(2, { project_id: 8 })
    await waitFor(() => expect(statRenders.get('twillingate.widget.1.2')).toBeGreaterThan(before.get('twillingate.widget.1.2')!))
    expect(statRenders.get('twillingate.widget.1.1')).toBe(before.get('twillingate.widget.1.1'))
  })
})
