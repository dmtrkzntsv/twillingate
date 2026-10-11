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

  it('moves a project without a success toast and refetches the list', async () => {
    const moveProject = vi.spyOn(endpoints, 'moveProject').mockResolvedValue({ status: 'moved' })
    const { client, wrapper: w } = wrapper()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    let ok: boolean | undefined
    await act(async () => {
      ok = await result.current.move(7, 3)
    })
    expect(ok).toBe(true)
    expect(moveProject).toHaveBeenCalledWith(7, 3)
    expect(toast.success).not.toHaveBeenCalled()
    expect(invalidate.mock.calls.map((c) => (c[0] as { queryKey: string[] }).queryKey[0])).toEqual(['projects'])
  })

  it('toasts a refused move and resolves false', async () => {
    vi.spyOn(endpoints, 'moveProject').mockRejectedValue(new ApiError(404, 'unknown project 3', 'not_found'))
    const { result } = renderHook(() => useProjectActions(), { wrapper: wrapper().wrapper })
    let ok: boolean | undefined
    await act(async () => {
      ok = await result.current.move(7, 3)
    })
    expect(ok).toBe(false)
    expect(toast.error).toHaveBeenCalledWith('unknown project 3')
  })

  it('renames, blocks and removes event rules with their toasts, refetching rules, names and usage', async () => {
    vi.spyOn(endpoints, 'renameEvent').mockResolvedValue({ rule: { family: 'product', name: 'a', action: 'rename', to: 'b' }, rewritten: {} })
    vi.spyOn(endpoints, 'blockEvent').mockResolvedValue({ rule: { family: 'product', name: 'x', action: 'block' } })
    vi.spyOn(endpoints, 'removeEventRule').mockResolvedValue({ status: 'removed' })
    const { client, wrapper: w } = wrapper()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    const out: (boolean | string)[] = []
    await act(async () => {
      out.push(await result.current.renameEvent(7, { family: 'product', from: 'a', to: 'b' }))
      out.push(await result.current.renameEvent(7, { family: 'product', from: 'a', to: 'b', merge: true }))
      out.push(await result.current.blockEvent(7, { family: 'product', name: 'x' }))
      out.push(await result.current.removeEventRule(7, { family: 'product', name: 'a' }))
      out.push(await result.current.addEventAlias(7, { family: 'product', alias: 'c', to: 'b' }))
    })
    expect(out).toEqual([true, true, true, true, true])
    expect(vi.mocked(toast.success).mock.calls.map((c) => c[0])).toEqual([
      'Renamed a to b', 'Merged a into b', 'Blocked x', 'Removed the rule for a', 'Added alias c to b',
    ])
    expect(endpoints.renameEvent).toHaveBeenLastCalledWith(7, { family: 'product', from: 'c', to: 'b', merge: true })
    expect(invalidate.mock.calls.slice(0, 3).map((c) => (c[0] as { queryKey: string[] }).queryKey[0])).toEqual(['event-rules', 'event-names', 'usage'])
  })

  it('resolves conflict without a toast or a refetch on a 409 rename, and toasts a 409 merge', async () => {
    const spy = vi.spyOn(endpoints, 'renameEvent').mockRejectedValue(new ApiError(409, 'b has events', 'conflict'))
    const { client, wrapper: w } = wrapper()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useProjectActions(), { wrapper: w })
    let out: unknown
    await act(async () => {
      out = await result.current.renameEvent(7, { family: 'product', from: 'a', to: 'b' })
    })
    expect(out).toBe('conflict')
    expect(toast.error).not.toHaveBeenCalled()
    expect(invalidate).not.toHaveBeenCalled()
    expect(result.current.pending).toBe(false)
    await act(async () => {
      out = await result.current.renameEvent(7, { family: 'product', from: 'a', to: 'b', merge: true })
    })
    expect(spy).toHaveBeenCalledTimes(2)
    expect(out).toBe(false)
    expect(toast.error).toHaveBeenCalledWith('b has events')
  })

  it('resolves false and toasts the refusal when an event rule is refused', async () => {
    vi.spyOn(endpoints, 'renameEvent').mockRejectedValue(new ApiError(400, '$b is reserved', 'invalid'))
    const { result } = renderHook(() => useProjectActions(), { wrapper: wrapper().wrapper })
    let ok: boolean | "conflict" | undefined
    await act(async () => {
      ok = await result.current.renameEvent(7, { family: 'product', from: 'a', to: 'b' })
    })
    expect(ok).toBe(false)
    expect(toast.error).toHaveBeenCalledWith('$b is reserved')
  })
})
