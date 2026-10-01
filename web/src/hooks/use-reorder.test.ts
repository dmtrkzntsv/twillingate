import { act, renderHook } from '@testing-library/react'
import type { DragEndEvent } from '@dnd-kit/core'
import { describe, expect, it, vi } from 'vitest'
import { useReorder } from './use-reorder'

// jsdom has no layout, so drive the drop the way DndContext would call it.
function drop(onDragEnd: ((e: DragEndEvent) => void) | undefined, active: number, over: number) {
  act(() => onDragEnd?.({ active: { id: active }, over: { id: over } } as unknown as DragEndEvent))
}

describe('useReorder', () => {
  it('shows the dropped order and turns dragging off until the props carry the new one', async () => {
    let resolve: (ok: boolean) => void = () => {}
    const onMove = vi.fn(() => new Promise<boolean>((r) => (resolve = r)))
    const { result, rerender } = renderHook(({ ids }) => useReorder(ids, onMove, 'y'), { initialProps: { ids: [10, 20, 30] } })

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
    const { result } = renderHook(() => useReorder([10, 20, 30], onMove, 'x'))

    drop(result.current.context.onDragEnd, 30, 10)
    await act(async () => {})

    expect(onMove).toHaveBeenCalledWith(30, 0)
    expect(result.current.order).toEqual([10, 20, 30])
    expect(result.current.busy).toBe(false)
  })

  it('does not move on a drop in place', () => {
    const onMove = vi.fn(async () => true)
    const { result } = renderHook(() => useReorder([10, 20, 30], onMove, 'y'))

    drop(result.current.context.onDragEnd, 20, 20)

    expect(onMove).not.toHaveBeenCalled()
    expect(result.current.busy).toBe(false)
  })

  it('forgets a dropped order once other props arrive, even if they later match again', async () => {
    const onMove = vi.fn(async () => true)
    const { result, rerender } = renderHook(({ ids }) => useReorder(ids, onMove, 'y'), { initialProps: { ids: [10, 20] } })

    drop(result.current.context.onDragEnd, 10, 20)
    await act(async () => {})
    rerender({ ids: [20, 10] })
    rerender({ ids: [10, 20] })

    expect(result.current.order).toEqual([10, 20])
  })
})
