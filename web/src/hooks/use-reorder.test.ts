import { act, renderHook } from '@testing-library/react'
import type { DragEndEvent } from '@dnd-kit/core'
import { describe, expect, it, vi } from 'vitest'
import { announcements, useReorder } from './use-reorder'

// jsdom has no layout, so drive the drop the way DndContext would call it.
function drop(onDragEnd: ((e: DragEndEvent) => void) | undefined, active: number, over: number) {
  act(() => onDragEnd?.({ active: { id: active }, over: { id: over } } as unknown as DragEndEvent))
}

describe('useReorder', () => {
  it('shows the dropped order and turns dragging off until the props carry the new one', async () => {
    let resolve: (ok: boolean) => void = () => {}
    const onMove = vi.fn(() => new Promise<boolean>((r) => (resolve = r)))
    const { result, rerender } = renderHook(({ ids }) => useReorder(ids, onMove, 'y', String), { initialProps: { ids: [10, 20, 30] } })

    drop(result.current.context.onDragEnd, 10, 30)

    expect(onMove).toHaveBeenCalledWith(10, 2)
    expect(result.current.order).toEqual([20, 30, 10])
    expect(result.current.busy).toBe(true)

    await act(async () => resolve(true))
    expect(result.current.order).toEqual([20, 30, 10])

    rerender({ ids: [20, 30, 10] })
    expect(result.current.order).toEqual([20, 30, 10])
    expect(result.current.busy).toBe(false)
  })

  it('snaps back at once when the move is refused', async () => {
    const onMove = vi.fn(async () => false)
    const { result } = renderHook(() => useReorder([10, 20, 30], onMove, 'x', String))

    drop(result.current.context.onDragEnd, 30, 10)
    await act(async () => {})

    expect(onMove).toHaveBeenCalledWith(30, 0)
    expect(result.current.order).toEqual([10, 20, 30])
    expect(result.current.busy).toBe(false)
  })

  it('does not move on a drop in place', () => {
    const onMove = vi.fn(async () => true)
    const { result } = renderHook(() => useReorder([10, 20, 30], onMove, 'y', String))

    drop(result.current.context.onDragEnd, 20, 20)

    expect(onMove).not.toHaveBeenCalled()
    expect(result.current.busy).toBe(false)
  })

  it('forgets a dropped order once other props arrive, even if they later match again', async () => {
    const onMove = vi.fn(async () => true)
    const { result, rerender } = renderHook(({ ids }) => useReorder(ids, onMove, 'y', String), { initialProps: { ids: [10, 20] } })

    drop(result.current.context.onDragEnd, 10, 20)
    await act(async () => {})
    rerender({ ids: [20, 10] })
    rerender({ ids: [10, 20] })

    expect(result.current.order).toEqual([10, 20])
  })

  it('lets a late refusal clear only its own drop, not a newer one', async () => {
    let refuse: (ok: boolean) => void = () => {}
    const onMove = vi.fn().mockImplementationOnce(() => new Promise<boolean>((r) => (refuse = r))).mockImplementation(() => new Promise(() => {}))
    const { result, rerender } = renderHook(({ ids }) => useReorder(ids, onMove, 'y', String), { initialProps: { ids: [10, 20, 30] } })

    drop(result.current.context.onDragEnd, 10, 30)
    // Someone else's change arrives before the answer, and the user drops again.
    rerender({ ids: [30, 20, 10] })
    drop(result.current.context.onDragEnd, 30, 10)
    expect(result.current.order).toEqual([20, 10, 30])

    await act(async () => refuse(false))
    expect(result.current.order).toEqual([20, 10, 30])
  })
})

describe('announcements', () => {
  const titles: Record<number, string> = { 10: 'Launch week', 20: 'Marketing', 30: 'Sales' }
  const say = announcements((id) => titles[id], [10, 20, 30])
  const item = (id: number) => ({ id }) as never

  it('names items by title and places by position', () => {
    expect(say.onDragStart({ active: item(20) })).toBe('Picked up Marketing, position 2 of 3.')
    expect(say.onDragOver({ active: item(20), over: item(30) })).toBe('Marketing moved to position 3 of 3.')
    expect(say.onDragEnd({ active: item(20), over: item(10) })).toBe('Marketing dropped at position 1 of 3.')
    expect(say.onDragCancel({ active: item(20), over: null })).toBe('Moving Marketing was cancelled.')
  })
})
