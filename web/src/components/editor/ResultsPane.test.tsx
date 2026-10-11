import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { PreviewResult } from '@/lib/api'
import { Preview, Rows, type RunState } from './ResultsPane'

const xy: PreviewResult = { columns: ['x', 'y'], rows: [['a', '1']], truncated: false, elapsed_ms: 2 }
const onCalendar = (result: PreviewResult): RunState => ({ kind: 'done', result, component: 'calendar', props: {} })

// React reports a caught error on the console; these tests only care that it is caught.
beforeEach(() => void vi.spyOn(console, 'error').mockImplementation(() => {}))
afterEach(() => vi.restoreAllMocks())

describe('Preview', () => {
  it("says the visualization can't draw rows it throws on", () => {
    render(<Preview component="calendar" props={{}} titleSlot="Days" height={4} state={onCalendar(xy)} stale={false} />)
    expect(screen.getByText("This visualization can't draw these rows")).toBeInTheDocument()
  })

  it('catches a day that is not one', () => {
    const result: PreviewResult = { columns: ['day', 'value'], rows: [['x', '1']], truncated: false, elapsed_ms: 2 }
    render(<Preview component="calendar" props={{}} titleSlot="Days" height={4} state={onCalendar(result)} stale={false} />)
    expect(screen.getByText("This visualization can't draw these rows")).toBeInTheDocument()
  })

  it('draws in a card with the title row it is given', () => {
    render(<Preview component="calendar" props={{}} titleSlot="Days" height={4} state={{ kind: 'idle' }} stale={false} />)
    expect(screen.getByRole('region', { name: 'Preview' })).toHaveTextContent('Days')
    expect(screen.getByText(/Run the query to see the preview/)).toBeInTheDocument()
  })

  it('says so when the query returned no rows', () => {
    const empty: PreviewResult = { columns: ['x', 'y'], rows: [], truncated: false, elapsed_ms: 2 }
    const state: RunState = { kind: 'done', result: empty, component: 'line', props: {} }
    render(<Preview component="line" props={{}} titleSlot="Days" height={4} state={state} stale={false} />)
    expect(screen.getByText('The query returned no rows')).toBeInTheDocument()
  })
})

describe('Rows', () => {
  it('says there are none', () => {
    render(<Rows state={onCalendar({ columns: ['x'], rows: [], truncated: false, elapsed_ms: 1 })} stale={false} />)
    expect(screen.getByRole('cell', { name: 'No rows' })).toBeInTheDocument()
  })

  it('shows the rows bare, and a refusal in their place', () => {
    const { rerender } = render(<Rows state={onCalendar(xy)} stale={false} />)
    expect(screen.getByRole('cell', { name: 'a' })).toBeInTheDocument()
    rerender(<Rows state={{ kind: 'failed', message: 'no such table: v' }} stale={false} />)
    expect(screen.getByText('no such table: v')).toBeInTheDocument()
  })
})
