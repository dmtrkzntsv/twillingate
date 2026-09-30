import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useStoredState } from './use-stored-state'

const asNumber = (v: unknown) => (typeof v === 'number' ? v : null)

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('useStoredState', () => {
  it('starts from the stored value', () => {
    localStorage.setItem('k', '7')
    const { result } = renderHook(() => useStoredState('k', asNumber))
    expect(result.current[0]).toBe(7)
  })

  it('starts empty when nothing is stored', () => {
    const { result } = renderHook(() => useStoredState('k', asNumber))
    expect(result.current[0]).toBeNull()
  })

  it('writes a value and deletes the key on null', () => {
    const { result } = renderHook(() => useStoredState('k', asNumber))
    act(() => result.current[1](3))
    expect(result.current[0]).toBe(3)
    expect(localStorage.getItem('k')).toBe('3')
    act(() => result.current[1](null))
    expect(result.current[0]).toBeNull()
    expect(localStorage.getItem('k')).toBeNull()
  })

  it('ignores a stored value that is not JSON or that parse refuses', () => {
    localStorage.setItem('k', '{broken')
    expect(renderHook(() => useStoredState('k', asNumber)).result.current[0]).toBeNull()
    localStorage.setItem('k', '"text"')
    expect(renderHook(() => useStoredState('k', asNumber)).result.current[0]).toBeNull()
  })

  it('keeps working in memory when storage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    const { result } = renderHook(() => useStoredState('k', asNumber))
    expect(result.current[0]).toBeNull()
    act(() => result.current[1](5))
    expect(result.current[0]).toBe(5)
  })

  it('keeps the value in memory only without a key', () => {
    const { result } = renderHook(() => useStoredState(undefined, asNumber))
    act(() => result.current[1](9))
    expect(result.current[0]).toBe(9)
    expect(localStorage.length).toBe(0)
  })
})
