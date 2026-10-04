import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { ApiError, endpoints } from '@/lib/api'
import { testClient } from '@/test/render'
import { useProjectActions } from './use-project-actions'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

function wrapper(client = testClient()) {
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  }
}

beforeEach(() => vi.restoreAllMocks())

describe('useProjectActions', () => {
  it('issues a key, toasts and invalidates the keys', async () => {
    vi.spyOn(endpoints, 'issueKey').mockResolvedValue({ key: 'ak_x', status: 'issued' })
    const { client, wrapper: w } = wrapper()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    let issued: unknown
    await act(async () => {
      issued = await result.current.issueKey(7, 'web')
    })
    expect(issued).toEqual({ key: 'ak_x', status: 'issued' })
    expect(toast.success).toHaveBeenCalled()
    expect(invalidate.mock.calls.map((c) => (c[0] as { queryKey: string[] }).queryKey[0])).toEqual(['keys'])
  })

  it('refetches projects, keys, stats and received attributes after a project write, never cap usage', async () => {
    vi.spyOn(endpoints, 'archiveProject').mockResolvedValue({ status: 'archived' } as never)
    const { client, wrapper: w } = wrapper()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    await act(async () => {
      await result.current.archive(7)
    })
    const keys = invalidate.mock.calls.map((c) => (c[0] as { queryKey: string[] }).queryKey[0])
    expect(keys.sort()).toEqual(['keys', 'projects', 'received-attributes', 'usage'])
  })

  it('stays pending until the project list has refetched, so a next write builds on the saved list', async () => {
    vi.spyOn(endpoints, 'updateProject').mockResolvedValue({ project_id: 7 })
    const { client, wrapper: w } = wrapper()
    let finish!: () => void
    vi.spyOn(client, 'invalidateQueries').mockImplementation(((filters: { queryKey: string[] }) =>
      filters.queryKey[0] === 'projects' ? new Promise<void>((r) => { finish = r }) : Promise.resolve()) as never)
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    let saved: Promise<boolean> | undefined
    act(() => {
      saved = result.current.update(7, { attributes: ['plan'] })
    })
    await vi.waitFor(() => expect(finish).toBeDefined())
    expect(result.current.pending).toBe(true)
    await act(async () => {
      finish()
      await saved
    })
    expect(result.current.pending).toBe(false)
  })

  it('refetches nothing when the action failed', async () => {
    vi.spyOn(endpoints, 'disableKey').mockRejectedValue(new ApiError(409, 'already disabled', 'conflict'))
    const { client, wrapper: w } = wrapper()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    await act(async () => {
      await result.current.disableKey(7, 'web')
    })
    expect(invalidate).not.toHaveBeenCalled()
  })

  it('shows a refusal as a toast and resolves undefined', async () => {
    vi.spyOn(endpoints, 'archiveProject').mockRejectedValue(new ApiError(409, 'project is already archived', 'conflict'))
    const { wrapper: w } = wrapper()
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    let out: unknown = 'unset'
    await act(async () => {
      out = await result.current.archive(7)
    })
    expect(out).toBeUndefined()
    expect(toast.error).toHaveBeenCalledWith('project is already archived')
  })
})
