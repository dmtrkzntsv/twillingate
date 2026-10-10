import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { PreviewResult } from '@/lib/api'
import ResultsPane from './ResultsPane'

const xy: PreviewResult = { columns: ['x', 'y'], rows: [['a', '1']], truncated: false, elapsed_ms: 2 }

// React reports a caught error on the console; these tests only care that it is caught.
beforeEach(() => void vi.spyOn(console, 'error').mockImplementation(() => {}))
afterEach(() => vi.restoreAllMocks())

describe('ResultsPane', () => {
  it("says the visualization can't draw rows it throws on, and offers them as a table", async () => {
    render(<ResultsPane component="calendar" props={{}} height={4} state={{ kind: 'done', result: xy }} stale={false} />)
    expect(screen.getByText("This visualization can't draw these rows")).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'See them as a table' }))
    expect(screen.getByRole('cell', { name: 'a' })).toBeInTheDocument()
  })

  it('catches a day that is not one', () => {
    const result: PreviewResult = { columns: ['day', 'value'], rows: [['x', '1']], truncated: false, elapsed_ms: 2 }
    render(<ResultsPane component="calendar" props={{}} height={4} state={{ kind: 'done', result }} stale={false} />)
    expect(screen.getByText("This visualization can't draw these rows")).toBeInTheDocument()
  })
})
