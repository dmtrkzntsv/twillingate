import { renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
  it('moves with after alone and refetches the dashboards shown', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue({} as Widget)
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.move(5, 0)).resolves.toBe(true)
    expect(update).toHaveBeenCalledWith(5, { after: 0 })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard'] })
  })

  it('resizes with width and height alone', async () => {
    const update = vi.spyOn(endpoints, 'updateWidget').mockResolvedValue({} as Widget)
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.resize(5, { width: 4, height: 6 })).resolves.toBe(true)
    expect(update).toHaveBeenCalledWith(5, { width: 4, height: 6 })
  })

  it('toasts a refusal, resolves false and still refetches', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new ApiError(400, 'width is columns out of 12, from 1 to 12', 'invalid'))
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.resize(5, { width: 13, height: 6 })).resolves.toBe(false)
    expect(toast.error).toHaveBeenCalledWith('width is columns out of 12, from 1 to 12')
    expect(invalidate).toHaveBeenCalled()
  })

  it('says so when the server cannot be reached', async () => {
    vi.spyOn(endpoints, 'updateWidget').mockRejectedValue(new TypeError('Failed to fetch'))
    const { result } = renderHook(() => useWidgetActions(), { wrapper })
    await expect(result.current.move(5, 3)).resolves.toBe(false)
    expect(toast.error).toHaveBeenCalledWith("Couldn't reach the server")
  })
})
