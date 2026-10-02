import { focusManager } from '@tanstack/react-query'

/**
 * Makes TanStack's "focused" mean the window has focus, not merely that
 * the tab is visible (its default): a dashboard left open behind another
 * window stops refetching (`refetchInterval` waits for focus) and catches
 * up when it is brought back (`refetchOnWindowFocus`).
 */
export function trackWindowFocus(): void {
  focusManager.setEventListener((setFocused) => {
    const update = () => setFocused(document.visibilityState === 'visible' && document.hasFocus())
    update()
    window.addEventListener('focus', update)
    window.addEventListener('blur', update)
    document.addEventListener('visibilitychange', update)
    return () => {
      window.removeEventListener('focus', update)
      window.removeEventListener('blur', update)
      document.removeEventListener('visibilitychange', update)
    }
  })
}
