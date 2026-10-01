import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import { focusManager } from '@tanstack/react-query'
import { useAutoRefresh } from './use-auto-refresh'

beforeEach(() => {
  vi.useFakeTimers()
  focusManager.setFocused(true)
})

afterEach(() => {
  focusManager.setFocused(undefined)
  vi.useRealTimers()
})

describe('useAutoRefresh', () => {
  it('refreshes every interval while the window has focus, not at once', () => {
    const refresh = vi.fn()
    renderHook(() => useAutoRefresh(true, 60, refresh))

    vi.advanceTimersByTime(55_000)
    expect(refresh).not.toHaveBeenCalled()
    vi.advanceTimersByTime(5_000)
    expect(refresh).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(60_000)
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('waits while the window has no focus, then catches up when it returns', () => {
    const refresh = vi.fn()
    renderHook(() => useAutoRefresh(true, 60, refresh))

    focusManager.setFocused(false)
    vi.advanceTimersByTime(600_000)
    expect(refresh).not.toHaveBeenCalled()

    focusManager.setFocused(true)
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('does not catch up on focus before a whole interval has passed', () => {
    const refresh = vi.fn()
    renderHook(() => useAutoRefresh(true, 60, refresh))

    focusManager.setFocused(false)
    vi.advanceTimersByTime(30_000)
    focusManager.setFocused(true)
    expect(refresh).not.toHaveBeenCalled()
  })

  it('does nothing when off, or with no interval', () => {
    const refresh = vi.fn()
    renderHook(() => useAutoRefresh(false, 60, refresh))
    renderHook(() => useAutoRefresh(true, 0, refresh))

    vi.advanceTimersByTime(600_000)
    expect(refresh).not.toHaveBeenCalled()
  })

  it('stops when turned off', () => {
    const refresh = vi.fn()
    const { rerender } = renderHook(({ on }) => useAutoRefresh(on, 60, refresh), { initialProps: { on: true } })
    rerender({ on: false })

    vi.advanceTimersByTime(600_000)
    expect(refresh).not.toHaveBeenCalled()
  })
})
