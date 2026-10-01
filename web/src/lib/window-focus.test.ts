import { afterEach, describe, expect, it, vi } from 'vitest'
import { focusManager } from '@tanstack/react-query'
import { trackWindowFocus } from './window-focus'

describe('trackWindowFocus', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    focusManager.setFocused(undefined)
  })

  it('follows the window gaining and losing focus, not just visibility', () => {
    const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    trackWindowFocus()
    expect(focusManager.isFocused()).toBe(true)

    hasFocus.mockReturnValue(false)
    window.dispatchEvent(new Event('blur'))
    expect(focusManager.isFocused()).toBe(false)

    hasFocus.mockReturnValue(true)
    window.dispatchEvent(new Event('focus'))
    expect(focusManager.isFocused()).toBe(true)
  })
})
