import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider, QueryObserver } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type DashboardDetail, type Widget } from '@/lib/api'
import { useWidgetActions } from './use-widget-actions'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}))

let client: QueryClient

/** Widget `id` as the server answers it, at `width` by `height`. */
function widget(id: number, width: number, height: number): Widget {
  return { widget_id: id, width, height } as Widget
}

/** Dashboard `id` as the server answers it, holding `widgets`. */
function dashboard(id: number, widgets: Widget[]): DashboardDetail {
  return { dashboard_id: id, widgets } as DashboardDetail
}

/** Shows dashboard `id` as a page does, so an invalidation refetches it; resolves once it has loaded. */
async function observe(id: number, queryFn: () => Promise<unknown>) {
  let fetched = 0
  new QueryObserver(client, {
    queryKey: ['dashboard', id],
    queryFn: () => {
      fetched++
      return queryFn()
    },
  }).subscribe(() => {})
  await waitFor(() => expect(fetched).toBe(1))
}

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.restoreAllMocks()
  vi.clearAllMocks()
})

describe('useWidgetActions', () => {
  it('moves with after alone and refetches the widget\'s dashboard', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue(widget(5, 4, 6))
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.move(9, 5, 0)).resolves.toBe(true)
    expect(update).toHaveBeenCalledWith(5, { after: 0 })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 9] })
  })

  it('resizes with width and height alone, and resolves with the size saved', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue(widget(5, 4, 6))
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.resize(9, 5, { width: 4, height: 6 })).resolves.toEqual({ width: 4, height: 6 })
    expect(update).toHaveBeenCalledWith(5, { width: 4, height: 6 })
  })

  it('resolves a resize with the size the refetched dashboard holds, not the one it wrote', async () => {
    let width = 6
    await observe(9, async () => dashboard(9, [widget(5, width, 4)]))
    // Saved at 8, then set to 5 by another writer before the refetch.
    vi.spyOn(endpoints, 'updateWidget').mockResolvedValue(widget(5, 8, 4))
    width = 5
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.resize(9, 5, { width: 8, height: 4 })).resolves.toEqual({ width: 5, height: 4 })
  })

  it('resolves a resize with the size it wrote when the refetch fails', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockResolvedValue(widget(5, 8, 4))
    let fail = false
    await observe(9, async () => {
      if (fail) throw new TypeError('Failed to fetch')
      return dashboard(9, [widget(5, 6, 4)])
    })
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    fail = true
    // The cache still holds the size from before the save, which the
    // server no longer has.
    await expect(result.current.resize(9, 5, { width: 8, height: 4 })).resolves.toEqual({ width: 8, height: 4 })
  })

  it('toasts a refusal, resolves false or null and still refetches', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new ApiError(400, 'width is columns out of 12, from 1 to 12', 'invalid'))
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.resize(9, 5, { width: 13, height: 6 })).resolves.toBeNull()
    await expect(result.current.move(9, 5, 3)).resolves.toBe(false)
    expect(toast.error).toHaveBeenCalledWith('width is columns out of 12, from 1 to 12')
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 9] })
  })

  it('refetches only the dashboard it was saved on, and resolves after that refetch', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockResolvedValue(widget(5, 4, 6))
    const fetches = { 9: 0, 10: 0 }
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    // Two dashboards shown, each with an observer, so each would refetch
    // if it were invalidated.
    for (const id of [9, 10] as const) {
      new QueryObserver(client, {
        queryKey: ['dashboard', id],
        queryFn: async () => {
          fetches[id]++
          if (id === 9 && fetches[id] > 1) await gate
          return dashboard(id, [])
        },
      }).subscribe(() => {})
      await waitFor(() => expect(fetches[id]).toBe(1))
    }
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    let done = false
    const saved = result.current.resize(9, 5, { width: 4, height: 6 }).then((ok) => {
      done = true
      return ok
    })
    await waitFor(() => expect(fetches[9]).toBe(2))
    // The refetch is still running: the action has not resolved.
    expect(done).toBe(false)
    release()
    await expect(saved).resolves.toEqual({ width: 4, height: 6 })
    expect(fetches).toEqual({ 9: 2, 10: 1 })
  })

  it('says so when the server cannot be reached', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new TypeError('Failed to fetch'))
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.move(9, 5, 3)).resolves.toBe(false)
    expect(toast.error).toHaveBeenCalledWith("Couldn't reach the server")
  })
})
