import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { endpoints, type AlertEvaluation } from '@/lib/api'
import { alert } from '@/test/alerts'
import { renderWithProviders, testClient } from '@/test/render'
import AlertHistory from './AlertHistory'

beforeEach(() => {
  vi.restoreAllMocks()
})

function evaluation(over: Partial<AlertEvaluation> = {}): AlertEvaluation {
  return {
    id: 1,
    at: '2026-11-25T06:00:00Z',
    from: '2026-11-24',
    to: '2026-11-24',
    value: 1240,
    compare: 1500,
    volume: 2000,
    outcome: 'ok',
    error: '',
    ...over,
  }
}

const rows = () => within(screen.getByRole('region', { name: 'History' })).getAllByRole('row').slice(1)

describe('AlertHistory', () => {
  it('lists the evaluations with values formatted by the alert unit', async () => {
    const spy = vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({
      evaluations: [
        evaluation({ id: 2, outcome: 'breached', value: 1240 }),
        evaluation({ id: 1, from: '2026-11-18', to: '2026-11-24', value: null, compare: null, volume: null, outcome: 'no_data' }),
      ],
    })
    renderWithProviders(<AlertHistory projectId={3} alert={alert({ id: 7, unit: 'count' })} />)
    expect(screen.getByRole('heading', { name: 'History' })).toBeInTheDocument()
    expect(await screen.findByText('Breached')).toBeInTheDocument()
    expect(spy).toHaveBeenCalledWith(3, 7, undefined)
    const [first, second] = rows()
    expect(within(first).getByText('2026-11-24 to 2026-11-24')).toBeInTheDocument()
    expect(within(first).getByText('1,240')).toBeInTheDocument()
    expect(within(first).getByText('1,500')).toBeInTheDocument()
    expect(within(first).getByText('2,000')).toBeInTheDocument()
    expect(within(first).getByText('Breached')).toHaveClass('text-destructive')
    expect(within(second).getByText('No data')).toBeInTheDocument()
    expect(within(second).getAllByText('—')).toHaveLength(3)
  })

  it('formats a fraction as a percent', async () => {
    vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({ evaluations: [evaluation({ value: 0.034, compare: null, volume: null })] })
    renderWithProviders(<AlertHistory projectId={3} alert={alert({ unit: 'fraction' })} />)
    expect(await screen.findByText('3.4%')).toBeInTheDocument()
  })

  it('shows a dash for the days of a silence check', async () => {
    vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({ evaluations: [evaluation({ from: '', to: '', value: 7, compare: null, volume: null })] })
    renderWithProviders(<AlertHistory projectId={3} alert={alert({ unit: 'hours' })} />)
    expect(await screen.findByText('7h')).toBeInTheDocument()
    const days = within(rows()[0]).getAllByRole('cell')[1]
    expect(days).toHaveTextContent('—')
  })

  it('shows the error under an Error outcome and Broken in amber', async () => {
    vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({
      evaluations: [evaluation({ id: 2, outcome: 'error', error: 'query timed out' }), evaluation({ id: 1, outcome: 'broken' })],
    })
    renderWithProviders(<AlertHistory projectId={3} alert={alert()} />)
    expect(await screen.findByText('Error')).toBeInTheDocument()
    expect(screen.getByText('query timed out')).toHaveClass('text-muted-foreground', '[overflow-wrap:anywhere]')
    expect(screen.getByText('Broken')).toHaveClass('text-amber-600')
  })

  it('loads the next page with the cursor and appends it', async () => {
    const spy = vi
      .spyOn(endpoints, 'alertHistory')
      .mockResolvedValueOnce({ evaluations: [evaluation({ id: 2, value: 10 })], next_cursor: 'c1' })
      .mockResolvedValueOnce({ evaluations: [evaluation({ id: 1, value: 20 })] })
    renderWithProviders(<AlertHistory projectId={3} alert={alert({ id: 7 })} />)
    await screen.findByText('10')
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('20')).toBeInTheDocument()
    expect(spy).toHaveBeenLastCalledWith(3, 7, 'c1')
    expect(rows()).toHaveLength(2)
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  })

  it('shows the error when the next page fails, with Retry in place of Load more', async () => {
    const spy = vi
      .spyOn(endpoints, 'alertHistory')
      .mockResolvedValueOnce({ evaluations: [evaluation({ id: 2, value: 10 })], next_cursor: 'c1' })
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce({ evaluations: [evaluation({ id: 1, value: 20 })] })
    renderWithProviders(<AlertHistory projectId={3} alert={alert({ id: 7 })} />)
    await screen.findByText('10')
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText(/boom/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
    expect(screen.getByText('10')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('20')).toBeInTheDocument()
    expect(spy).toHaveBeenLastCalledWith(3, 7, 'c1')
  })

  it('has no Load more without a next cursor', async () => {
    vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({ evaluations: [evaluation()] })
    renderWithProviders(<AlertHistory projectId={3} alert={alert()} />)
    await screen.findByText('1,240')
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  })

  it('says so when it was never evaluated', async () => {
    vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({ evaluations: [] })
    renderWithProviders(<AlertHistory projectId={3} alert={alert()} />)
    expect(await screen.findByText('Not evaluated yet')).toBeInTheDocument()
  })

  it('hides the Days, Compared with and Volume columns below sm', async () => {
    vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({ evaluations: [evaluation()] })
    renderWithProviders(<AlertHistory projectId={3} alert={alert()} />)
    await screen.findByText('1,240')
    for (const name of ['Days', 'Compared with', 'Volume']) {
      expect(screen.getByRole('columnheader', { name })).toHaveClass('hidden', 'sm:table-cell')
    }
    for (const name of ['When', 'Value', 'Outcome']) {
      expect(screen.getByRole('columnheader', { name })).not.toHaveClass('hidden')
    }
    const cells = within(rows()[0]).getAllByRole('cell')
    expect(cells.map((c) => c.classList.contains('hidden'))).toEqual([false, true, false, true, true, false])
  })

  it('reads the history again when the alert was checked again', async () => {
    const spy = vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({ evaluations: [evaluation()] })
    const client = testClient()
    const tree = (at: string) => (
      <QueryClientProvider client={client}>
        <AlertHistory projectId={3} alert={alert({ id: 7, last_evaluated_at: at })} />
      </QueryClientProvider>
    )
    const { rerender } = render(tree('2026-11-25T06:00:00Z'))
    await screen.findByText('1,240')
    expect(spy).toHaveBeenCalledTimes(1)
    rerender(tree('2026-11-25T06:00:00Z'))
    expect(spy).toHaveBeenCalledTimes(1)
    rerender(tree('2026-11-25T06:01:00Z'))
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(2))
  })
})
