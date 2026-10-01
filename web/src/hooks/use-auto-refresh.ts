import { useEffect, useRef } from 'react'
import { focusManager } from '@tanstack/react-query'

/**
 * Calls `refresh` every `seconds` while `enabled`, but only while the
 * window has focus (`trackWindowFocus`): a dashboard left behind another
 * window waits, and catches up as soon as it is focused again if a whole
 * interval passed meanwhile. Turning it on starts the clock; it does not
 * refresh at once.
 */
export function useAutoRefresh(enabled: boolean, seconds: number, refresh: () => void): void {
  const latest = useRef(refresh)
  useEffect(() => {
    latest.current = refresh
  })

  useEffect(() => {
    if (!enabled || seconds <= 0) return
    const ms = seconds * 1000
    let last = Date.now()
    const tick = () => {
      if (!focusManager.isFocused() || Date.now() - last < ms) return
      last = Date.now()
      latest.current()
    }
    const id = window.setInterval(tick, Math.min(ms, 5_000))
    const unsubscribe = focusManager.subscribe((focused) => {
      if (focused) tick()
    })
    return () => {
      window.clearInterval(id)
      unsubscribe()
    }
  }, [enabled, seconds])
}
