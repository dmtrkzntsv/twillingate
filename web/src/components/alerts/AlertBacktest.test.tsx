import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { draftOf, type AlertDraft } from '@/lib/alert-draft'
import { ApiError, endpoints, type AlertPreview } from '@/lib/api'
import { alert } from '@/test/alerts'
import { renderWithProviders } from '@/test/render'
import AlertBacktest from './AlertBacktest'

const DAY = 86_400_000

beforeEach(() => {
  vi.restoreAllMocks()
})

/** 90 days ending 2026-11-25, ok but for two runs of breached days and one with no data. */
function ninetyDays(): AlertPreview {
  return { days: okDays(90), gaps: [], fired: 2 }
}

/** `n` days ending 2026-11-25, ok but for breached days 10, 11 and 50 and a day 20 with no data. */
function okDays(n: number): AlertPreview['days'] {
  const start = Date.parse('2026-11-25T00:00:00Z') - (n - 1) * DAY
  return Array.from({ length: n }, (_, i) => ({
    day: new Date(start + i * DAY).toISOString().slice(0, 10),
    value: 100 + i,
    compare: null,
    volume: null,
    outcome: [10, 11, 50].includes(i) ? ('breached' as const) : i === 20 ? ('no_data' as const) : ('ok' as const),
  }))
}

const strip = () => screen.getAllByTestId('backtest-strip')

describe('AlertBacktest', () => {
  it("runs the saved alert's backtest: one cell a day, coloured by outcome, and how often it would have fired", async () => {
    const preview = vi.spyOn(endpoints, 'previewAlert').mockResolvedValue(ninetyDays())
    renderWithProviders(<AlertBacktest projectId={1} alert={alert({ id: 7 })} draft={null} />)
    expect(screen.getByRole('heading', { name: 'Backtest' })).toBeInTheDocument()
    expect(await screen.findByText('Would have fired 2 times in the last 90 days')).toBeInTheDocument()
    expect(preview).toHaveBeenCalledWith(1, { id: 7 })
    const cells = within(strip()[0]).getAllByRole('listitem')
    expect(cells).toHaveLength(90)
    expect(cells[10]).toHaveAttribute('title', '2026-09-07: 110 — Breached')
    expect(cells[10]).toHaveClass('bg-destructive')
    expect(cells[20]).toHaveAttribute('title', '2026-09-17: 120 — No data')
    expect(cells[0]).toHaveAttribute('title', '2026-08-28: 100 — OK')
  })

  it('says a funnel would not have fired in its 30 days', async () => {
    vi.spyOn(endpoints, 'previewAlert').mockResolvedValue({ days: okDays(30).map((d) => ({ ...d, outcome: 'ok' })), gaps: [], fired: 0 })
    renderWithProviders(<AlertBacktest projectId={1} alert={alert({ id: 7, kind: 'funnel', unit: 'fraction' })} draft={null} />)
    expect(await screen.findByText('Would not have fired in the last 30 days')).toBeInTheDocument()
  })

  it('counts the days the answer has, whatever the kind', async () => {
    vi.spyOn(endpoints, 'previewAlert').mockResolvedValue({ days: okDays(60), gaps: [], fired: 1 })
    renderWithProviders(<AlertBacktest projectId={1} alert={alert({ id: 7 })} draft={null} />)
    expect(await screen.findByText('Would have fired 1 time in the last 60 days')).toBeInTheDocument()
  })

  it('runs the saved backtest again once the definition was edited elsewhere', async () => {
    const preview = vi.spyOn(endpoints, 'previewAlert').mockResolvedValueOnce(ninetyDays()).mockResolvedValue({ ...ninetyDays(), fired: 4 })
    const a = alert({ id: 7 })
    const { client, rerender } = renderWithProviders(<AlertBacktest projectId={1} alert={a} draft={null} />)
    await screen.findByText('Would have fired 2 times in the last 90 days')
    rerender(
      <QueryClientProvider client={client}>
        <AlertBacktest projectId={1} alert={{ ...a, window_days: 7, updated_at: '2026-11-26T09:00:00Z' }} draft={null} />
      </QueryClientProvider>
    )
    expect(await screen.findByText('Would have fired 4 times in the last 90 days')).toBeInTheDocument()
    expect(preview).toHaveBeenCalledTimes(2)
  })

  it('reads the saved backtest no more for an edit that leaves the definition (a rename, a pause, a snooze)', async () => {
    const preview = vi.spyOn(endpoints, 'previewAlert').mockResolvedValue(ninetyDays())
    const a = alert({ id: 7 })
    const { client, rerender } = renderWithProviders(<AlertBacktest projectId={1} alert={a} draft={null} />)
    await screen.findByText('Would have fired 2 times in the last 90 days')
    const edits: Partial<typeof a>[] = [
      { name: 'Sign-ups', updated_at: '2026-11-26T09:00:00Z' },
      { paused: true, state: 'paused', updated_at: '2026-11-26T09:01:00Z' },
      { snoozed_until: '2026-11-27T09:00:00Z', state: 'snoozed', updated_at: '2026-11-26T09:02:00Z' },
    ]
    for (const e of edits) {
      rerender(
        <QueryClientProvider client={client}>
          <AlertBacktest projectId={1} alert={{ ...a, ...e }} draft={null} />
        </QueryClientProvider>
      )
      await new Promise((r) => setTimeout(r, 20))
    }
    expect(screen.getByText('Would have fired 2 times in the last 90 days')).toBeInTheDocument()
    expect(preview).toHaveBeenCalledTimes(1)
  })

  it("lists a silence alert's gaps in local time", async () => {
    const gaps = [
      { from: '2026-11-10T02:00:00Z', to: '2026-11-10T14:00:00Z', hours: 12 },
      { from: '2026-11-20T00:00:00Z', to: '2026-11-21T01:00:00Z', hours: 25 },
    ]
    vi.spyOn(endpoints, 'previewAlert').mockResolvedValue({ days: [], gaps, fired: 1 })
    renderWithProviders(
      <AlertBacktest projectId={1} alert={alert({ id: 7, kind: 'silence', unit: 'hours', condition: { type: 'silence', hours: 6 } })} draft={null} />
    )
    expect(await screen.findByText('Would have fired 1 time in the last 30 days')).toBeInTheDocument()
    const local = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
    expect(screen.getByText(`${local(gaps[0].from)} – ${local(gaps[0].to)} · 12h`)).toBeInTheDocument()
    expect(screen.getByText(`${local(gaps[1].from)} – ${local(gaps[1].to)} · 25h`)).toBeInTheDocument()
    expect(screen.queryByTestId('backtest-strip')).toBeNull()
  })

  it('shows a failed backtest with a retry that runs it again', async () => {
    const preview = vi.spyOn(endpoints, 'previewAlert').mockRejectedValueOnce(new ApiError(504, 'the query took too long'))
    const user = userEvent.setup()
    renderWithProviders(<AlertBacktest projectId={1} alert={alert({ id: 7 })} draft={null} />)
    expect(await screen.findByText("Couldn't load the backtest. the query took too long")).toBeInTheDocument()
    preview.mockResolvedValue(ninetyDays())
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Would have fired 2 times in the last 90 days')).toBeInTheDocument()
  })

  it('previews unsaved changes on request, beside the saved backtest', async () => {
    const a = alert({ id: 7 })
    const preview = vi.spyOn(endpoints, 'previewAlert').mockImplementation(async (_p, body) => ('id' in body ? ninetyDays() : { ...ninetyDays(), fired: 5 }))
    const user = userEvent.setup()
    const { client, rerender: rerenderRaw } = renderWithProviders(<AlertBacktest projectId={1} alert={a} draft={draftOf(a)} />)
    const rerender = (draft: AlertDraft) =>
      rerenderRaw(
        <QueryClientProvider client={client}>
          <AlertBacktest projectId={1} alert={a} draft={draft} />
        </QueryClientProvider>
      )
    await screen.findByText('Would have fired 2 times in the last 90 days')
    // A draft with no definition change has nothing to preview.
    expect(screen.queryByRole('button', { name: 'Preview changes' })).toBeNull()
    rerender({ ...draftOf(a), name: 'Renamed' })
    expect(screen.queryByRole('button', { name: 'Preview changes' })).toBeNull()
    const changed = { ...draftOf(a), value: '200' }
    rerender(changed)
    await user.click(screen.getByRole('button', { name: 'Preview changes' }))
    expect(preview).toHaveBeenLastCalledWith(1, { query: a.query, read: a.read, condition: { type: 'threshold', op: 'below', value: 200 }, window_days: 1 })
    const mine = await screen.findByRole('region', { name: 'With your changes' })
    expect(await within(mine).findByText('Would have fired 5 times in the last 90 days')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Preview changes' })).toBeNull()
    // Saved or discarded: the draft is the alert again, and the preview goes.
    rerender(draftOf(a))
    expect(screen.queryByRole('region', { name: 'With your changes' })).toBeNull()
  })

  it('keeps Preview changes from a draft with errors', async () => {
    const a = alert({ id: 7 })
    vi.spyOn(endpoints, 'previewAlert').mockResolvedValue(ninetyDays())
    renderWithProviders(<AlertBacktest projectId={1} alert={a} draft={{ ...draftOf(a), windowDays: '40' }} />)
    expect(await screen.findByRole('button', { name: 'Preview changes' })).toBeDisabled()
  })
})
