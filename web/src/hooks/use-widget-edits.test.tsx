import { act, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type Widget } from '@/lib/api'
import { addBody, type Draft } from '@/lib/widget-draft'
import { useWidgetEdits } from './use-widget-edits'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}))

let client: QueryClient

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const visits: Widget = {
  widget_id: 7,
  dashboard_id: 3,
  name: 'visits',
  component: 'line',
  title: 'Visits',
  width: 6,
  height: 8,
  props: {},
  source: { type: 'sql', content: 'SELECT day AS x, views AS y FROM v' },
  follows_project: false,
  follows_range: false,
}

const draft: Draft = { title: 'Signups', component: 'stat', props: {}, sql: 'SELECT 1 AS value', markdown: '' }

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.restoreAllMocks()
  vi.clearAllMocks()
})

describe('useWidgetEdits', () => {
  it('add posts the draft to the dashboard and refetches it and the list', async () => {
    const add = vi.spyOn(endpoints, 'addWidget').mockResolvedValue(visits)
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await act(() => result.current.add(3, draft))

    expect(add).toHaveBeenCalledWith(3, addBody(draft))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 3] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboards'] })
  })

  it('add throws a refusal for the editor to show, without a toast', async () => {
    vi.spyOn(endpoints, 'addWidget').mockRejectedValue(new ApiError(400, 'no such view'))
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(result.current.add(3, draft)).rejects.toThrow('no such view')
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('update sends only the fields that changed and refetches the dashboard and the widget', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue(visits)
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await act(() =>
      result.current.update(visits, { title: 'Visits', component: 'line', props: {}, sql: 'SELECT 2', markdown: '' })
    )

    expect(update).toHaveBeenCalledWith(7, { source: { type: 'sql', content: 'SELECT 2' } })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 3] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['widget', 7] })
  })

  it('update makes no request when nothing changed', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget')
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await act(() =>
      result.current.update(visits, { title: 'Visits', component: 'line', props: {}, sql: visits.source.content, markdown: '' })
    )

    expect(update).not.toHaveBeenCalled()
  })

  it('update throws a refusal for the editor to show', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new ApiError(400, 'bad sql'))
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(result.current.update(visits, { ...draft, component: 'line' })).rejects.toThrow('bad sql')
    expect(toast.error).not.toHaveBeenCalled()
  })

  it.each([
    ['blank', '  '],
    ['unchanged', 'Visits'],
    ['unchanged once trimmed', ' Visits '],
  ])('rename to a %s title makes no request and resolves true', async (_, title) => {
    const update = vi.spyOn(endpoints, 'updateWidget')
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(act(() => result.current.rename(visits, title))).resolves.toBe(true)

    expect(update).not.toHaveBeenCalled()
  })

  it('rename sends the title alone, never the name, and refetches the dashboard', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue({ ...visits, title: 'New' })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(act(() => result.current.rename(visits, ' New '))).resolves.toBe(true)

    expect(update).toHaveBeenCalledWith(7, { title: 'New' })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 3] })
  })

  it('a refused rename toasts its message and resolves false', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new ApiError(400, 'title too long'))
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(act(() => result.current.rename(visits, 'New'))).resolves.toBe(false)

    expect(toast.error).toHaveBeenCalledWith('title too long')
  })

  it('a rename over a dropped connection says the server could not be reached', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new TypeError('Failed to fetch'))
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(act(() => result.current.rename(visits, 'New'))).resolves.toBe(false)

    expect(toast.error).toHaveBeenCalledWith("Couldn't reach the server")
  })

  it('archive archives at once, refetches, and toasts "Widget archived" whose Undo restores it', async () => {
    const archive = vi.spyOn(endpoints, 'archiveWidget').mockResolvedValue({ status: 'archived' })
    const restore = vi.spyOn(endpoints, 'restoreWidget').mockResolvedValue({ status: 'restored' })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await act(() => result.current.archive(visits))

    expect(archive).toHaveBeenCalledWith(7)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 3] })
    expect(toast).toHaveBeenCalledWith('Widget archived', { action: { label: 'Undo', onClick: expect.any(Function) } })

    const { action } = vi.mocked(toast).mock.calls[0][1] as unknown as { action: { onClick: () => void } }
    await act(async () => action.onClick())
    expect(restore).toHaveBeenCalledWith(7)
  })

  it('a refused archive toasts its message and no success toast', async () => {
    vi.spyOn(endpoints, 'archiveWidget').mockRejectedValue(new ApiError(404, 'no such widget'))
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await act(() => result.current.archive(visits))

    expect(toast.error).toHaveBeenCalledWith('no such widget')
    expect(toast).not.toHaveBeenCalled()
  })

  it('restore refetches the archived widgets and the dashboard, and resolves true', async () => {
    vi.spyOn(endpoints, 'restoreWidget').mockResolvedValue({ status: 'restored' })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(act(() => result.current.restore({ widget_id: 7, dashboard_id: 3 }))).resolves.toBe(true)

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['widgets'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 3] })
  })

  it('a refused restore toasts and resolves false', async () => {
    vi.spyOn(endpoints, 'restoreWidget').mockRejectedValue(new ApiError(409, 'its dashboard is archived'))
    const { result } = renderHook(() => useWidgetEdits(), { wrapper })

    await expect(act(() => result.current.restore({ widget_id: 7, dashboard_id: 3 }))).resolves.toBe(false)

    expect(toast.error).toHaveBeenCalledWith('its dashboard is archived')
  })

  it('returns the same object across renders', () => {
    const { result, rerender } = renderHook(() => useWidgetEdits(), { wrapper })
    const first = result.current
    rerender()
    expect(result.current).toBe(first)
  })
})
