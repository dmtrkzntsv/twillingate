import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useCopy } from './use-copy'

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true })
}

class FakeClipboardItem {
  readonly items: Record<string, Promise<Blob>>
  constructor(items: Record<string, Promise<Blob>>) {
    this.items = items
  }
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useCopy', () => {
  it('copies a string, says copied, then idles again after a moment', async () => {
    vi.useFakeTimers()
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ writeText })
    const { result } = renderHook(() => useCopy())
    let out: string | undefined
    await act(async () => {
      out = await result.current.copy('hello')
    })
    expect(writeText).toHaveBeenCalledWith('hello')
    expect(out).toBe('copied')
    expect(result.current.state).toBe('copied')
    act(() => vi.advanceTimersByTime(2000))
    expect(result.current.state).toBe('idle')
  })

  it('fails without a clipboard', async () => {
    setClipboard(undefined)
    const { result } = renderHook(() => useCopy())
    await act(async () => {
      expect(await result.current.copy('hello')).toBe('failed')
    })
    expect(result.current.state).toBe('failed')
  })

  it('hands a text still to come to the clipboard at once, as a ClipboardItem, so the click still counts', async () => {
    vi.stubGlobal('ClipboardItem', FakeClipboardItem)
    let written: FakeClipboardItem | undefined
    const write = vi.fn(async ([item]: FakeClipboardItem[]) => {
      written = item
      await item.items['text/plain']
    })
    setClipboard({ write, writeText: vi.fn() })
    const { result } = renderHook(() => useCopy())
    let resolve!: (s: string) => void
    let done!: Promise<string>
    act(() => {
      done = result.current.copy(new Promise<string | undefined>((r) => (resolve = r)))
    })
    // Called before the text exists: that is what keeps the user's click.
    expect(write).toHaveBeenCalledTimes(1)
    await act(async () => resolve('https://x/share/1'))
    expect(await done).toBe('copied')
    expect(await (await written!.items['text/plain']).text()).toBe('https://x/share/1')
  })

  it('falls back to writeText once the text is there, when ClipboardItem is refused', async () => {
    vi.stubGlobal('ClipboardItem', FakeClipboardItem)
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ write: vi.fn().mockRejectedValue(new Error('no promises here')), writeText })
    const { result } = renderHook(() => useCopy())
    await act(async () => {
      expect(await result.current.copy(Promise.resolve('later'))).toBe('copied')
    })
    expect(writeText).toHaveBeenCalledWith('later')
  })

  it('uses writeText where there is no ClipboardItem', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ writeText })
    const { result } = renderHook(() => useCopy())
    await act(async () => {
      expect(await result.current.copy(Promise.resolve('later'))).toBe('copied')
    })
    expect(writeText).toHaveBeenCalledWith('later')
  })

  it('copies nothing and stays idle when the text never comes', async () => {
    vi.stubGlobal('ClipboardItem', FakeClipboardItem)
    const write = vi.fn(async ([item]: FakeClipboardItem[]) => {
      await item.items['text/plain']
    })
    const writeText = vi.fn()
    setClipboard({ write, writeText })
    const { result } = renderHook(() => useCopy())
    await act(async () => {
      expect(await result.current.copy(Promise.resolve(undefined))).toBe('none')
    })
    expect(writeText).not.toHaveBeenCalled()
    expect(result.current.state).toBe('idle')
  })
})
