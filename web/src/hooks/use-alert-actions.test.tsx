import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { ApiError, endpoints, type AlertsResponse } from '@/lib/api'
import { alertsQuery } from '@/lib/queries'
import { alert } from '@/test/alerts'
import { testClient } from '@/test/render'
import { useAlertActions } from './use-alert-actions'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))

const LIVE = alertsQuery(1, false).queryKey
const ARCHIVED = alertsQuery(1, true).queryKey
/** When the check a pending answer shows was asked for. */
const REQUESTED = '2026-11-26T10:00:00Z'

/** The hook over a client holding the given lists, the refetches they trigger answered from `endpoints.alerts`. */
function setup(lists: { live?: AlertsResponse; archived?: AlertsResponse }) {
  const client = testClient()
  if (lists.live) client.setQueryData(LIVE, lists.live)
  if (lists.archived) client.setQueryData(ARCHIVED, lists.archived)
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const { result } = renderHook(() => useAlertActions(1), { wrapper })
  return { client, result }
}

const list = (...alerts: ReturnType<typeof alert>[]): AlertsResponse => ({ max_live: 50, alerts })

beforeEach(() => {
  vi.restoreAllMocks()
  vi.mocked(toast.error).mockClear()
  // Nothing observes the lists here, so invalidating them refetches nothing; the spy is for refusals' sake.
  vi.spyOn(endpoints, 'alerts').mockResolvedValue(list())
})

describe('useAlertActions', () => {
  it('marks a check asked, by its request time, when a save answers one pending, and puts the alert pending in the list', async () => {
    const pending = alert({ id: 7, name: 'Renamed', check_pending: true, check_requested_at: REQUESTED })
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(pending)
    const { client, result } = setup({ live: list(alert({ id: 7 })) })
    let answer: unknown
    await act(async () => {
      answer = await result.current.update(7, { window_days: 7 })
    })
    expect(answer).toEqual(pending)
    expect(endpoints.updateAlert).toHaveBeenCalledWith(1, 7, { window_days: 7 })
    expect(result.current.asked(7)).toBe(REQUESTED)
    expect(client.getQueryData<AlertsResponse>(LIVE)?.alerts).toEqual([pending])
    result.current.forget(7)
    expect(result.current.asked(7)).toBeUndefined()
  })

  it('marks nothing asked for a save that asks no check, nor for a pause answering a check someone else asked for', async () => {
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValueOnce(alert({ id: 7, name: 'Renamed' }))
    const { result } = setup({ live: list(alert({ id: 7 })) })
    await act(async () => {
      await result.current.update(7, { name: 'Renamed' })
    })
    expect(result.current.asked(7)).toBeUndefined()
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValueOnce(alert({ id: 7, paused: true, state: 'paused', check_pending: true, check_requested_at: REQUESTED }))
    await act(async () => {
      await result.current.pause(7)
    })
    expect(result.current.asked(7)).toBeUndefined()
  })

  it('moves an archived alert out of the live list, into the archived one only where that is loaded', async () => {
    const archived = alert({ id: 7, archived_at: '2026-11-26T10:00:00Z' })
    vi.spyOn(endpoints, 'archiveAlert').mockResolvedValue(archived)
    const one = setup({ live: list(alert({ id: 3, name: 'A' }), alert({ id: 7 })) })
    await act(async () => {
      await one.result.current.archive(7)
    })
    expect(one.client.getQueryData<AlertsResponse>(LIVE)?.alerts.map((a) => a.id)).toEqual([3])
    expect(one.client.getQueryData(ARCHIVED)).toBeUndefined()

    const both = setup({ live: list(alert({ id: 7 })), archived: list(alert({ id: 2, name: 'Old' }), alert({ id: 4, name: 'Zed' })) })
    await act(async () => {
      await both.result.current.archive(7)
    })
    expect(both.client.getQueryData<AlertsResponse>(LIVE)?.alerts).toEqual([])
    // In name order, as the server lists them.
    expect(both.client.getQueryData<AlertsResponse>(ARCHIVED)?.alerts.map((a) => a.id)).toEqual([2, 7, 4])
  })

  it("hands a save's refusal to onRefusal instead of toasting it, and still refetches the lists", async () => {
    vi.spyOn(endpoints, 'updateAlert').mockRejectedValue(new ApiError(409, 'a live alert is named Signups', 'conflict'))
    const { client, result } = setup({ live: list(alert({ id: 7 })) })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const onRefusal = vi.fn()
    let answer: unknown = 'unset'
    await act(async () => {
      answer = await result.current.update(7, { name: 'Signups' }, { onRefusal })
    })
    expect(answer).toBeNull()
    expect(onRefusal).toHaveBeenCalledTimes(1)
    const err = onRefusal.mock.calls[0][0]
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 409, message: 'a live alert is named Signups' })
    expect(toast.error).not.toHaveBeenCalled()
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['alerts', 1] })
  })

  it.each<[string, (r: ReturnType<typeof useAlertActions>) => Promise<unknown>]>([
    ['a pause', (r) => r.pause(7)],
    ['a snooze', (r) => r.snooze(7, '1h')],
    ['an archive', (r) => r.archive(7)],
  ])('forgets an asked check that %s drops', async (_what, drop) => {
    vi.spyOn(endpoints, 'checkAlert').mockResolvedValue(alert({ id: 7, check_pending: true, check_requested_at: REQUESTED }))
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(alert({ id: 7, paused: true, state: 'paused' }))
    vi.spyOn(endpoints, 'archiveAlert').mockResolvedValue(alert({ id: 7, archived_at: '2026-11-26T10:00:00Z' }))
    const { result } = setup({ live: list(alert({ id: 7 })) })
    await act(async () => {
      await result.current.check(7)
    })
    expect(result.current.asked(7)).toBe(REQUESTED)
    await act(async () => {
      await drop(result.current)
    })
    expect(result.current.asked(7)).toBeUndefined()
  })

  it("marks nothing asked for a rename whose answer shows a check asked for elsewhere", async () => {
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(alert({ id: 7, name: 'Renamed', check_pending: true, check_requested_at: REQUESTED }))
    const { result } = setup({ live: list(alert({ id: 7 })) })
    await act(async () => {
      await result.current.update(7, { name: 'Renamed' })
    })
    expect(result.current.asked(7)).toBeUndefined()
  })

  it.each<[string, (r: ReturnType<typeof useAlertActions>) => Promise<unknown>]>([
    ['a resume', (r) => r.resume(7)],
    ['a wake', (r) => r.wake(7)],
    ['a new condition', (r) => r.update(7, { condition: { type: 'threshold', op: 'below', value: 40 } })],
    ['new template fields', (r) => r.update(7, { fields: { event: 'paid' } })],
    ['a new minimum volume', (r) => r.update(7, { min_volume: 10 })],
  ])('marks a check asked by %s, which asks for one', async (_what, ask) => {
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(alert({ id: 7, check_pending: true, check_requested_at: REQUESTED }))
    const { result } = setup({ live: list(alert({ id: 7 })) })
    await act(async () => {
      await ask(result.current)
    })
    expect(result.current.asked(7)).toBe(REQUESTED)
  })

  it('keeps an asked check across a save whose answer has none pending, for the page to settle', async () => {
    vi.spyOn(endpoints, 'checkAlert').mockResolvedValue(alert({ id: 7, check_pending: true, check_requested_at: REQUESTED }))
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(alert({ id: 7, name: 'Renamed' }))
    const { result } = setup({ live: list(alert({ id: 7 })) })
    await act(async () => {
      await result.current.check(7)
    })
    await act(async () => {
      await result.current.update(7, { name: 'Renamed' })
    })
    expect(result.current.asked(7)).toBe(REQUESTED)
  })

  it('toasts a refusal, resolves null and refetches the lists, which the refusal says are stale', async () => {
    vi.spyOn(endpoints, 'updateAlert').mockRejectedValue(new ApiError(409, 'the alert changed meanwhile', 'conflict'))
    const { client, result } = setup({ live: list(alert({ id: 7 })) })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    let answer: unknown = 'unset'
    await act(async () => {
      answer = await result.current.update(7, { name: 'x' })
    })
    expect(answer).toBeNull()
    expect(toast.error).toHaveBeenCalledWith('the alert changed meanwhile')
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['alerts', 1] })
    expect(result.current.asked(7)).toBeUndefined()
  })

  it('creates an alert into the live list, in name order, and refetches the lists', async () => {
    const made = alert({ id: 12, name: 'Paid' })
    vi.spyOn(endpoints, 'createAlert').mockResolvedValue(made)
    const { client, result } = setup({ live: list(alert({ id: 7, name: 'Activations' }), alert({ id: 8, name: 'Signups' })) })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const body = { name: 'Paid', template: 'event_count' as const, fields: { event: 'paid' }, condition: { type: 'threshold' as const, op: 'below' as const, value: 5 }, window_days: 1 }
    let answer: unknown
    await act(async () => {
      answer = await result.current.create(body)
    })
    expect(answer).toEqual(made)
    expect(endpoints.createAlert).toHaveBeenCalledWith(1, body)
    expect(client.getQueryData<AlertsResponse>(LIVE)?.alerts.map((a) => a.id)).toEqual([7, 12, 8])
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['alerts', 1] })
  })

  it("hands a create's refusal to onRefusal instead of a toast, and resolves null", async () => {
    const err = new ApiError(409, 'an alert named Paid exists', 'conflict')
    vi.spyOn(endpoints, 'createAlert').mockRejectedValue(err)
    const { result } = setup({ live: list() })
    const onRefusal = vi.fn()
    let answer: unknown
    await act(async () => {
      answer = await result.current.create({ name: 'Paid', template: 'no_events', fields: { event: 'paid' }, condition: { type: 'silence', hours: 6 } }, { onRefusal })
    })
    expect(answer).toBeNull()
    expect(onRefusal).toHaveBeenCalledWith(err)
    expect(toast.error).not.toHaveBeenCalled()
  })
})
