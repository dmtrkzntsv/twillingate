import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import { ApiError, endpoints, type Alert, type AlertPatch, type Project } from '@/lib/api'
import { alert, serveAlerts } from '@/test/alerts'
import { renderWithProviders } from '@/test/render'
import AlertPage from './AlertPage'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))

const project: Project = { project_id: 1, name: 'shop', allowed_origins: [] }
const DAY = 86_400_000

beforeEach(() => {
  vi.restoreAllMocks()
  vi.mocked(toast).mockClear()
  // The backtest reads on its own: answered empty.
  vi.spyOn(endpoints, 'previewAlert').mockResolvedValue({ days: [], gaps: [], fired: 0 })
  vi.mocked(toast.error).mockClear()
})

function renderPage(alertId = '7') {
  return renderWithProviders(
    <MemoryRouter initialEntries={[`/projects/1/alerts/${alertId}`]}>
      <AlertPage project={project} alertId={alertId} />
    </MemoryRouter>
  )
}

/** Serves one live alert, and answers updateAlert as the server would, changing the list the refetch reads. */
function serveOne(a: Alert) {
  const served = serveAlerts([a])
  const update = vi.spyOn(endpoints, 'updateAlert').mockImplementation(async (_p: number, _id: number, patch: AlertPatch) => {
    const paused = patch.paused ?? a.paused
    const snoozed = patch.snoozed_until === undefined ? a.snoozed_until : (patch.snoozed_until ?? '')
    const next = alert({ ...a, paused, snoozed_until: snoozed, state: paused ? 'paused' : snoozed ? 'snoozed' : 'pending' })
    served.lists.live = [next]
    return next
  })
  return { ...served, update }
}

describe('AlertActions', () => {
  it.each<[string, Partial<Alert>]>([
    ['Paused alerts are not checked', { paused: true, state: 'paused' }],
    ['Snoozed alerts are not checked', { state: 'snoozed', snoozed_until: '2026-12-01T00:00:00Z' }],
  ])('keeps Check now from a check the server refuses, saying why: %s', async (why, over) => {
    serveOne(alert({ id: 7, ...over }))
    const check = vi.spyOn(endpoints, 'checkAlert')
    const user = userEvent.setup()
    renderPage()
    const button = await screen.findByRole('button', { name: 'Check now' })
    expect(button).toHaveAttribute('aria-disabled', 'true')
    await user.hover(button)
    expect(await screen.findByRole('tooltip')).toHaveTextContent(why)
    await user.click(button)
    expect(check).not.toHaveBeenCalled()
  })

  it("keeps Check now from an archived project's alert, saying why", async () => {
    serveOne(alert({ id: 7 }))
    const check = vi.spyOn(endpoints, 'checkAlert')
    const user = userEvent.setup()
    renderWithProviders(
      <MemoryRouter initialEntries={['/projects/1/alerts/7']}>
        <AlertPage project={{ ...project, archived: true, archived_at: '2026-11-20T10:00:00Z' }} alertId="7" />
      </MemoryRouter>
    )
    const button = await screen.findByRole('button', { name: 'Check now' })
    expect(button).toHaveAttribute('aria-disabled', 'true')
    await user.hover(button)
    expect(await screen.findByRole('tooltip')).toHaveTextContent('Alerts of an archived project are not checked')
    await user.click(button)
    expect(check).not.toHaveBeenCalled()
  })

  it('offers Resume and Wake, and no Snooze, on a paused or a snoozed alert', async () => {
    serveOne(alert({ id: 7, paused: true, state: 'paused' }))
    const { unmount } = renderPage()
    expect(await screen.findByRole('button', { name: 'Resume' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Snooze' })).toBeNull()
    unmount()
    serveOne(alert({ id: 7, state: 'snoozed', snoozed_until: '2026-12-01T00:00:00Z' }))
    renderPage()
    expect(await screen.findByRole('button', { name: 'Wake' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Snooze' })).toBeNull()
  })

  it('checks now: Checking… while the list says pending, then Check now again and a toast of the state', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const { lists } = serveAlerts([alert({ id: 7 })])
      const check = vi.spyOn(endpoints, 'checkAlert').mockImplementation(async () => {
        const pending = alert({ id: 7, check_pending: true, check_requested_at: '2026-11-26T10:00:00Z' })
        lists.live = [pending]
        return pending
      })
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTimeAsync })
      renderPage()
      await user.click(await screen.findByRole('button', { name: 'Check now' }))
      expect(check).toHaveBeenCalledWith(1, 7)
      const checking = await screen.findByRole('button', { name: 'Checking…' })
      expect(checking).toHaveAttribute('aria-disabled', 'true')
      // Still pending after a poll: still Checking…, no toast yet.
      await vi.advanceTimersByTimeAsync(2_100)
      expect(screen.getByRole('button', { name: 'Checking…' })).toBeInTheDocument()
      expect(toast).not.toHaveBeenCalled()
      lists.live = [alert({ id: 7, state: 'breached' })]
      await vi.advanceTimersByTimeAsync(2_100)
      expect(await screen.findByRole('button', { name: 'Check now' })).not.toHaveAttribute('aria-disabled')
      expect(toast).toHaveBeenCalledTimes(1)
      expect(toast).toHaveBeenCalledWith('Checked: Breached')
    } finally {
      vi.useRealTimers()
    }
  })

  it('says nothing of a check that a pause cancelled', async () => {
    const { lists } = serveAlerts([alert({ id: 7 })])
    vi.spyOn(endpoints, 'checkAlert').mockImplementation(async () => {
      const pending = alert({ id: 7, check_pending: true, check_requested_at: '2026-11-26T10:00:00Z' })
      lists.live = [pending]
      return pending
    })
    // The server drops the check request with the pause: the answer is no longer pending.
    vi.spyOn(endpoints, 'updateAlert').mockImplementation(async () => {
      const paused = alert({ id: 7, paused: true, state: 'paused' })
      lists.live = [paused]
      return paused
    })
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Check now' }))
    await screen.findByRole('button', { name: 'Checking…' })
    await user.click(screen.getByRole('button', { name: 'Pause' }))
    expect(await screen.findByRole('button', { name: 'Resume' })).toBeInTheDocument()
    await vi.waitFor(() => expect(endpoints.alerts).toHaveBeenCalledTimes(3))
    expect(toast).not.toHaveBeenCalled()
  })

  it('snoozes for a day from now', async () => {
    const { update } = serveOne(alert({ id: 7 }))
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Snooze' }))
    const before = Date.now()
    await user.click(await screen.findByRole('menuitem', { name: '1 day' }))
    const after = Date.now()
    expect(update).toHaveBeenCalledTimes(1)
    const [projectId, id, patch] = update.mock.calls[0]
    expect([projectId, id]).toEqual([1, 7])
    const until = Date.parse(patch.snoozed_until as string)
    expect(until).toBeGreaterThanOrEqual(Math.floor((before + DAY) / 1000) * 1000)
    expect(until).toBeLessThanOrEqual(after + DAY)
    expect(await screen.findByRole('button', { name: 'Wake' })).toBeInTheDocument()
  })

  it('wakes a snoozed alert', async () => {
    const { update } = serveOne(alert({ id: 7, state: 'snoozed', snoozed_until: '2026-12-01T00:00:00Z' }))
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Wake' }))
    expect(update).toHaveBeenCalledWith(1, 7, { snoozed_until: null })
  })

  it('pauses, and resumes', async () => {
    const { update } = serveOne(alert({ id: 7 }))
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Pause' }))
    expect(update).toHaveBeenCalledWith(1, 7, { paused: true })
    await user.click(await screen.findByRole('button', { name: 'Resume' }))
    expect(update).toHaveBeenLastCalledWith(1, 7, { paused: false })
  })

  it('archives, and the page then shows the alert archived with Restore; Restore brings it back', async () => {
    const live = alert({ id: 7 })
    const archived = alert({ id: 7, archived_at: '2026-11-26T10:00:00Z' })
    const { lists } = serveAlerts([live])
    vi.spyOn(endpoints, 'archiveAlert').mockImplementation(async () => {
      lists.live = []
      lists.archived = [archived]
      return archived
    })
    vi.spyOn(endpoints, 'restoreAlert').mockImplementation(async () => {
      lists.live = [live]
      lists.archived = []
      return live
    })
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    expect(endpoints.archiveAlert).toHaveBeenCalledWith(1, 7)
    expect(await screen.findByRole('button', { name: 'Restore' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Restore' }))
    expect(endpoints.restoreAlert).toHaveBeenCalledWith(1, 7)
    expect(await screen.findByRole('button', { name: 'Check now' })).toBeInTheDocument()
  })

  it("toasts the server's refusal and reads the list again", async () => {
    const { spy } = serveAlerts([alert({ id: 7 })])
    vi.spyOn(endpoints, 'updateAlert').mockRejectedValue(new ApiError(409, 'the alert changed meanwhile', 'conflict'))
    const user = userEvent.setup()
    renderPage()
    const pause = await screen.findByRole('button', { name: 'Pause' })
    const reads = spy.mock.calls.length
    await user.click(pause)
    await vi.waitFor(() => expect(toast.error).toHaveBeenCalledWith('the alert changed meanwhile'))
    // A refusal says the page is stale: the list is read again.
    await vi.waitFor(() => expect(spy.mock.calls.length).toBeGreaterThan(reads))
    expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
  })
})
