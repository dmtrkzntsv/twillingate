import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider, QueryObserver } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type Widget } from '@/lib/api'
import { useWidgetActions } from './use-widget-actions'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}))

let client: QueryClient

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
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue({} as Widget)
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.move(9, 5, 0)).resolves.toBe(true)
    expect(update).toHaveBeenCalledWith(5, { after: 0 })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 9] })
  })

  it('resizes with width and height alone', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue({} as Widget)
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.resize(9, 5, { width: 4, height: 6 })).resolves.toBe(true)
    expect(update).toHaveBeenCalledWith(5, { width: 4, height: 6 })
  })

  it('toasts a refusal, resolves false and still refetches', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new ApiError(400, 'width is columns out of 12, from 1 to 12', 'invalid'))
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.resize(9, 5, { width: 13, height: 6 })).resolves.toBe(false)
    expect(toast.error).toHaveBeenCalledWith('width is columns out of 12, from 1 to 12')
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard', 9] })
  })

  it('refetches only the dashboard it was saved on, and resolves after that refetch', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockResolvedValue({} as Widget)
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
          return { id }
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
    await expect(saved).resolves.toBe(true)
    expect(fetches).toEqual({ 9: 2, 10: 1 })
  })

  it('says so when the server cannot be reached', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new TypeError('Failed to fetch'))
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.move(9, 5, 3)).resolves.toBe(false)
    expect(toast.error).toHaveBeenCalledWith("Couldn't reach the server")
  })
})
