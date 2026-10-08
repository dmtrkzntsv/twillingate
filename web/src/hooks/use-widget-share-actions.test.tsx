import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { ApiError, endpoints, type WidgetShare } from '@/lib/api'
import { testClient } from '@/test/render'
import { useWidgetShareActions } from './use-widget-share-actions'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const SHARE = { id: 'abc', title: 'Views' } as WidgetShare

function setup() {
  const client = testClient()
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  const { result } = renderHook(() => useWidgetShareActions(), {
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  })
  return { result, invalidate }
}

const invalidated = (invalidate: ReturnType<typeof setup>['invalidate']) =>
  invalidate.mock.calls.map((c) => (c[0] as { queryKey: string[] }).queryKey)

beforeEach(() => vi.restoreAllMocks())

describe('useWidgetShareActions', () => {
  it('creates a share without a toast, its button says so, and invalidates the shares', async () => {
    const create = vi.spyOn(endpoints, 'createWidgetShare').mockResolvedValue(SHARE)
    const { result, invalidate } = setup()
    const form = new FormData()
    let out: unknown
    await act(async () => {
      out = await result.current.create(form)
    })
    expect(create).toHaveBeenCalledWith(form)
    expect(out).toBe(SHARE)
    expect(toast.success).not.toHaveBeenCalled()
    expect(invalidated(invalidate)).toEqual([['widget-shares']])
    expect(result.current.pending).toBe(false)
  })

  it('changes the archive date, archives and restores, each with its toast', async () => {
    const update = vi.spyOn(endpoints, 'updateWidgetShare').mockResolvedValue(SHARE)
    const archive = vi.spyOn(endpoints, 'archiveWidgetShare').mockResolvedValue(SHARE)
    const restore = vi.spyOn(endpoints, 'restoreWidgetShare').mockResolvedValue(SHARE)
    const { result, invalidate } = setup()
    await act(async () => {
      await result.current.setArchiveAfter('abc', '90d')
      await result.current.archive('abc')
      await result.current.restore('abc', 'project')
    })
    expect(update).toHaveBeenCalledWith('abc', '90d')
    expect(archive).toHaveBeenCalledWith('abc')
    expect(restore).toHaveBeenCalledWith('abc', 'project')
    expect(vi.mocked(toast.success).mock.calls.map((c) => c[0])).toEqual(['Archive date changed', 'Share archived', 'Share restored'])
    expect(invalidated(invalidate)).toEqual([['widget-shares'], ['widget-shares'], ['widget-shares']])
  })

  it('toasts the message of a refusal, invalidates nothing and resolves undefined', async () => {
    vi.spyOn(endpoints, 'archiveWidgetShare').mockRejectedValue(new ApiError(409, 'already archived', 'conflict'))
    const { result, invalidate } = setup()
    let out: unknown = 'unset'
    await act(async () => {
      out = await result.current.archive('abc')
    })
    expect(out).toBeUndefined()
    expect(toast.error).toHaveBeenCalledWith('already archived')
    expect(toast.success).not.toHaveBeenCalled()
    expect(invalidate).not.toHaveBeenCalled()
    expect(result.current.pending).toBe(false)
  })
})
